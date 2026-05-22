package main

import (
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/stazelabs/ash/internal/ledger"
	"github.com/stazelabs/ash/internal/proto"
	"github.com/stazelabs/ash/internal/verbs"
)

// drive runs one `help` request through handle() over an in-process
// net.Pipe conn pair and returns the decoded response. It exercises the
// full request-decode -> dispatch -> encode -> frame-write -> ledger-record
// path without spawning a daemon process.
//
// By the time drive returns, handle()'s post-write ledger I/O has
// completed: ASH-214 moved Record after the frame write, but Record still
// runs before handle() loops back to ReadFrame. Closing the client conn
// makes that next ReadFrame return EOF, so <-done is a happens-after
// barrier — when the ledger is healthy, the row is already on disk.
func drive(t *testing.T, led *ledger.Ledger) *proto.Response {
	t.Helper()
	runners := verbs.Runners(led, nil, time.Time{}, "", nil, nil)
	pretty := verbs.PrettyHandlers()

	srv, cli := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		handle(srv, led, runners, pretty, 0, nil)
	}()

	// `help` needs no filesystem state and does not touch the ledger
	// itself, so it isolates the request/response/record path cleanly.
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

	// Closing the client conn unblocks handle()'s next ReadFrame so the
	// goroutine exits cleanly — and that read happens only after the
	// current request's ledger writes, so <-done implies Record is done.
	_ = cli.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handle goroutine did not exit after client close")
	}
	return rsp
}

// TestHandle_LedgerFailureDoesNotBreakResponse guards the ASH-214
// reordering: the response frame is written before any ledger I/O, so a
// ledger that cannot persist the call must not affect what the client
// receives. The "broken ledger" is a real ledger Close()d before the
// request, mirroring ledger.TestRecord_DetectsClosedDB. The verb result
// must arrive intact; the Record failure goes to ashd.log only and no
// longer rides the wire (LedgerError was retired in ASH-214).
func TestHandle_LedgerFailureDoesNotBreakResponse(t *testing.T) {
	dir := t.TempDir()
	led, err := ledger.Open(filepath.Join(dir, "ledger.db"), dir, "test")
	if err != nil {
		t.Fatalf("ledger.Open: %v", err)
	}
	if err := led.Close(); err != nil {
		t.Fatalf("ledger.Close: %v", err)
	}

	rsp := drive(t, led)

	if !rsp.OK {
		t.Fatalf("expected ok=true (the verb itself succeeded); got err=%+v", rsp.Err)
	}
	if rsp.Metrics == nil {
		t.Fatal("expected metrics on response, got nil")
	}
	if rsp.Metrics.LedgerError != "" {
		t.Errorf("LedgerError must stay empty post-ASH-214; got %q", rsp.Metrics.LedgerError)
	}
}

// TestHandle_RecordsRowOffResponsePath is the positive control: with a
// healthy ledger the call is still recorded — after the frame write, per
// ASH-214 — with bytes_out and latency_serialize_us folded into the single
// INSERT. It guards against the reorder dropping the Record entirely.
func TestHandle_RecordsRowOffResponsePath(t *testing.T) {
	dir := t.TempDir()
	led, err := ledger.Open(filepath.Join(dir, "ledger.db"), dir, "test")
	if err != nil {
		t.Fatalf("ledger.Open: %v", err)
	}
	defer led.Close()

	rsp := drive(t, led)

	if !rsp.OK {
		t.Fatalf("expected ok=true; got err=%+v", rsp.Err)
	}
	if rsp.Metrics == nil {
		t.Fatal("expected metrics, got nil")
	}

	calls, err := led.QueryRecent(10, "help")
	if err != nil {
		t.Fatalf("QueryRecent: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("expected exactly 1 recorded help row, got %d", len(calls))
	}
	// bytes_out is written by Record itself now (folded from the retired
	// UpdateSerializeStats patch); a non-zero value proves the fold.
	if calls[0].BytesOut <= 0 {
		t.Errorf("recorded row has bytes_out=%d; want > 0", calls[0].BytesOut)
	}
}
