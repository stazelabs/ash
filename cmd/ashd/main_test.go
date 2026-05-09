package main

import (
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stazelabs/ash/internal/ledger"
	"github.com/stazelabs/ash/internal/proto"
	"github.com/stazelabs/ash/internal/verbs"
)

// TestHandle_LedgerErrorOnWire is the end-to-end guard for the loud-failure
// path: when Record() fails inside a real handle() call, the response that
// reaches the client over the wire must carry a non-empty Metrics.LedgerError.
//
// We drive handle() through an in-process net.Pipe conn pair so the test
// exercises the full request-decode -> dispatch -> ledger-record -> encode
// path without spawning a daemon process. The "broken ledger" is a real
// ledger that we Close() before issuing the request, mirroring the contract
// proved by ledger.TestRecord_DetectsClosedDB.
func TestHandle_LedgerErrorOnWire(t *testing.T) {
	dir := t.TempDir()
	led, err := ledger.Open(filepath.Join(dir, "ledger.db"), dir, "test")
	if err != nil {
		t.Fatalf("ledger.Open: %v", err)
	}
	if err := led.Close(); err != nil {
		t.Fatalf("ledger.Close: %v", err)
	}

	runners := verbs.Runners(led, nil)
	pretty := verbs.PrettyHandlers()

	srv, cli := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		handle(srv, led, runners, pretty)
	}()

	// `help` is the simplest verb to drive: no filesystem state required and
	// it doesn't touch the ledger itself, so a Record failure is the only
	// reason LedgerError can appear.
	req := &proto.Request{
		V:    proto.ProtocolVersion,
		ID:   1,
		Verb: "help",
		Args: map[string]any{},
	}
	reqBuf, err := proto.EncodeRequest(req)
	if err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}
	_ = cli.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if err := proto.WriteFrame(cli, reqBuf); err != nil {
		t.Fatalf("WriteFrame: %v", err)
	}

	_ = cli.SetReadDeadline(time.Now().Add(2 * time.Second))
	rspBuf, err := proto.ReadFrame(cli)
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	rsp, err := proto.DecodeResponse(rspBuf)
	if err != nil {
		t.Fatalf("DecodeResponse: %v", err)
	}

	// Closing the client conn unblocks handle()'s next ReadFrame so the
	// goroutine exits cleanly.
	_ = cli.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handle goroutine did not exit after client close")
	}

	if !rsp.OK {
		t.Fatalf("expected ok=true (the verb itself succeeded); got err=%+v", rsp.Err)
	}
	if rsp.Metrics == nil {
		t.Fatal("expected metrics on response, got nil")
	}
	if rsp.Metrics.LedgerError == "" {
		t.Fatalf("expected non-empty Metrics.LedgerError after closed-ledger Record; metrics=%+v", rsp.Metrics)
	}
	// Sanity: the message should look like a SQL/DB error, not be a stray
	// non-empty placeholder.
	if !strings.Contains(strings.ToLower(rsp.Metrics.LedgerError), "closed") &&
		!strings.Contains(strings.ToLower(rsp.Metrics.LedgerError), "sql") {
		t.Errorf("LedgerError doesn't look like a DB error: %q", rsp.Metrics.LedgerError)
	}
}

// TestHandle_NoLedgerErrorOnHealthyDB is the negative control: with a healthy
// open ledger, the same call must produce an empty LedgerError. This guards
// against a future regression where LedgerError gets accidentally populated
// on every call.
func TestHandle_NoLedgerErrorOnHealthyDB(t *testing.T) {
	dir := t.TempDir()
	led, err := ledger.Open(filepath.Join(dir, "ledger.db"), dir, "test")
	if err != nil {
		t.Fatalf("ledger.Open: %v", err)
	}
	defer led.Close()

	runners := verbs.Runners(led, nil)
	pretty := verbs.PrettyHandlers()

	srv, cli := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		handle(srv, led, runners, pretty)
	}()

	req := &proto.Request{V: proto.ProtocolVersion, ID: 1, Verb: "help", Args: map[string]any{}}
	reqBuf, err := proto.EncodeRequest(req)
	if err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}
	_ = cli.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if err := proto.WriteFrame(cli, reqBuf); err != nil {
		t.Fatalf("WriteFrame: %v", err)
	}
	_ = cli.SetReadDeadline(time.Now().Add(2 * time.Second))
	rspBuf, err := proto.ReadFrame(cli)
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	rsp, err := proto.DecodeResponse(rspBuf)
	if err != nil {
		t.Fatalf("DecodeResponse: %v", err)
	}
	_ = cli.Close()
	<-done

	if !rsp.OK {
		t.Fatalf("expected ok=true; got err=%+v", rsp.Err)
	}
	if rsp.Metrics == nil {
		t.Fatal("expected metrics, got nil")
	}
	if rsp.Metrics.LedgerError != "" {
		t.Errorf("expected empty LedgerError on healthy DB, got %q", rsp.Metrics.LedgerError)
	}
}
