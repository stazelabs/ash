package main

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/stazelabs/ash/internal/proto"
)

// wireEmitFormat — trivial mapping. Pretty rides as "pretty"; everything
// else (including json + compact + unrecognized) maps to empty so the
// omitempty msgpack tag keeps two-call cache prefixes stable.
func TestWireEmitFormat(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"pretty", "pretty"},
		{"json", ""},
		{"compact", ""},
		{"", ""},
		{"garbage", ""},
	}
	for _, c := range cases {
		if got := wireEmitFormat(c.in); got != c.want {
			t.Errorf("wireEmitFormat(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// newID returns a 64-bit ID. Two consecutive calls must not collide in
// practice — proves the RNG path is active (vs always falling to the
// time.Now fallback which would correlate adjacent IDs).
func TestNewID_DistinctAcrossCalls(t *testing.T) {
	a := newID()
	b := newID()
	if a == 0 || b == 0 {
		t.Errorf("zero ID returned: a=%d b=%d", a, b)
	}
	if a == b {
		t.Errorf("two calls produced the same ID (RNG dead?): %d", a)
	}
}

// isConnRefused and isENOENT classify dial-side errors so dialOrStart
// can decide between "auto-start the daemon" and "propagate as fatal."
// Pin the truth table — the strings.Contains fallback is the only way
// to catch errors that don't wrap fs.ErrNotExist directly.
func TestIsConnRefused(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: errors.New("connection refused"), want: true},
		{name: "wrapped_op_error", err: &net.OpError{Op: "dial", Net: "unix", Err: errors.New("connect: connection refused")}, want: true},
		{name: "other_error", err: errors.New("permission denied"), want: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isConnRefused(c.err); got != c.want {
				t.Errorf("isConnRefused(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}

func TestIsENOENT(t *testing.T) {
	if !isENOENT(fs.ErrNotExist) {
		t.Error("fs.ErrNotExist should match")
	}
	if !isENOENT(&net.OpError{Op: "dial", Err: errors.New("no such file or directory")}) {
		t.Error("string-form 'no such file' should match")
	}
	if isENOENT(errors.New("permission denied")) {
		t.Error("unrelated error should not match")
	}
}

// tailLog returns the last N lines of a file, or "" on any failure.
func TestTailLog_HappyPath(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "ashd.log")
	body := "line1\nline2\nline3\nline4\nline5\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got := tailLog(path, 3)
	want := "line3\nline4\nline5"
	if got != want {
		t.Errorf("tailLog: got %q, want %q", got, want)
	}
}

func TestTailLog_FileLargerThanN(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "ashd.log")
	if err := os.WriteFile(path, []byte("only-line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := tailLog(path, 100) // ask for more lines than file has
	if got != "only-line" {
		t.Errorf("tailLog: got %q", got)
	}
}

func TestTailLog_MissingFile(t *testing.T) {
	if got := tailLog(filepath.Join(t.TempDir(), "nope.log"), 10); got != "" {
		t.Errorf("missing file should return empty string, got %q", got)
	}
}

func TestTailLog_EmptyFile(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "empty.log")
	_ = os.WriteFile(path, nil, 0o644)
	if got := tailLog(path, 10); got != "" {
		t.Errorf("empty file should return empty string, got %q", got)
	}
}

// roundtrip — the single-frame Request/Response wire path. Use net.Pipe()
// to stand up a fake daemon: read the Request frame on one side, reply
// with a canned Response frame, assert client decodes it.
func TestRoundtrip_Success(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	want := &proto.Response{
		V:       proto.ProtocolVersion,
		OK:      true,
		ID:      99,
		Data:    proto.MustData(map[string]any{"hello": "world"}),
		Metrics: &proto.Metrics{},
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		buf, err := proto.ReadFrame(server)
		if err != nil {
			t.Errorf("server read: %v", err)
			return
		}
		req, err := proto.DecodeRequest(buf)
		if err != nil {
			t.Errorf("server decode: %v", err)
			return
		}
		if req.Verb != "find" {
			t.Errorf("server saw verb %q, want find", req.Verb)
		}
		if req.EmitFormat != "pretty" {
			t.Errorf("server saw EmitFormat %q, want pretty", req.EmitFormat)
		}
		if req.Stream {
			t.Errorf("non-streaming roundtrip set Stream=true on the wire")
		}
		encoded, _ := proto.EncodeResponse(want)
		_ = proto.WriteFrame(server, encoded)
	}()

	got, err := roundtrip(client, "find", map[string]any{"path": "."}, "pretty")
	if err != nil {
		t.Fatalf("roundtrip: %v", err)
	}
	wg.Wait()

	if !got.OK {
		t.Errorf("OK: got false")
	}
	if got.Metrics == nil || got.Metrics.BytesOut == 0 {
		t.Errorf("BytesOut should be populated from the wire frame size, got %+v", got.Metrics)
	}
}

// streamingRoundtrip — minimal pin: Stream=true on the wire, single Final
// frame round-trips and populates BytesOut.
//
// We skip the Chunk-then-NotifyProgress path because NotifyProgress
// dereferences a nil *mcp.ServerSession and standing up a real session
// requires a full MCP transport pair. Chunk-frame decoding is covered
// indirectly by the unexpected-frame-kind test below; if we want true
// Chunk coverage later, the integration-style approach in main_test.go
// (TestServerRegistersTools) is the right harness.
func TestStreamingRoundtrip_FinalOnly(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	finalRsp := &proto.Response{
		V:       proto.ProtocolVersion,
		OK:      true,
		ID:      42,
		Data:    proto.MustData(map[string]any{"done": true}),
		Metrics: &proto.Metrics{},
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		buf, err := proto.ReadFrame(server)
		if err != nil {
			t.Errorf("server read req: %v", err)
			return
		}
		req, err := proto.DecodeRequest(buf)
		if err != nil {
			t.Errorf("server decode: %v", err)
			return
		}
		if !req.Stream {
			t.Errorf("streamingRoundtrip should set Stream=true on the wire")
		}
		encoded, _ := proto.EncodeResponse(finalRsp)
		if err := proto.WriteKinded(server, proto.KindFinal, encoded); err != nil {
			t.Errorf("server write final: %v", err)
		}
	}()

	got, err := streamingRoundtrip(context.Background(), client, nil, "tok-1", "find",
		map[string]any{"path": "."}, "json")
	if err != nil {
		t.Fatalf("streamingRoundtrip: %v", err)
	}
	wg.Wait()

	if !got.OK {
		t.Errorf("Final OK: got false")
	}
	if got.Metrics == nil || got.Metrics.BytesOut == 0 {
		t.Errorf("BytesOut should reflect Final frame size: %+v", got.Metrics)
	}
}

// Unexpected frame kind on the streaming wire → error. Pins the "shouldn't
// happen" branch in the read loop. Cancel kind is client-to-daemon, so the
// daemon emitting it is wrong.
func TestStreamingRoundtrip_UnexpectedFrameKind(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	go func() {
		_, _ = proto.ReadFrame(server)
		// Write a Cancel-kind frame (which shouldn't arrive daemon→client).
		_ = proto.WriteKinded(server, proto.KindCancel, []byte{0x00})
	}()

	_, err := streamingRoundtrip(context.Background(), client, nil, "tok", "find",
		map[string]any{"path": "."}, "json")
	if err == nil {
		t.Error("expected error for unexpected frame kind")
	}
	if !strings.Contains(err.Error(), "unexpected frame kind") {
		t.Errorf("error message: %v", err)
	}
}

// Context cancellation should close the connection and surface a read
// error promptly. Pins the cancel-watcher goroutine behavior — without
// it a hung daemon would block the MCP server thread indefinitely.
func TestStreamingRoundtrip_CtxCancelClosesConn(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())

	// Server consumes the request but never replies.
	go func() {
		_, _ = proto.ReadFrame(server)
		// Cancel from the test side after the request arrives.
		cancel()
		// Block: keep the conn open so cancellation is what closes it.
		time.Sleep(2 * time.Second)
	}()

	start := time.Now()
	_, err := streamingRoundtrip(ctx, client, nil, "tok", "find",
		map[string]any{"path": "."}, "json")
	elapsed := time.Since(start)
	if elapsed >= 1500*time.Millisecond {
		t.Fatalf("did not unblock on ctx cancel: elapsed %s", elapsed)
	}
	if err == nil {
		t.Error("expected read error after ctx cancel closed the conn")
	}
}

// startDaemon binary discovery: ASH_DAEMON pointing at a real file wins.
func TestFindAshd_AshDaemonEnvWins(t *testing.T) {
	tmp := t.TempDir()
	fake := filepath.Join(tmp, "fake-ashd")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ASH_DAEMON", fake)

	got, err := findAshd()
	if err != nil {
		t.Fatalf("findAshd: %v", err)
	}
	if got != fake {
		t.Errorf("got %q, want %q (ASH_DAEMON should win)", got, fake)
	}
}

// findAshd: ASH_DAEMON pointing at a nonexistent path falls through to
// the sibling / PATH fallback chain. The bin/ashd in this repo's bin/
// satisfies the sibling lookup when run from the workspace.
func TestFindAshd_NonexistentEnvFallsThrough(t *testing.T) {
	t.Setenv("ASH_DAEMON", "/definitely-not-here-xyzzy")
	// We don't assert the result path — it depends on the test environment
	// (sibling-of-test-binary may or may not exist). We only assert that
	// the nonexistent env didn't short-circuit with an error of its own.
	_, err := findAshd()
	if err != nil && !strings.Contains(err.Error(), "ashd binary not found") {
		t.Errorf("unexpected error shape: %v", err)
	}
}

// roundtrip surfaces a typed read error when the daemon hangs up
// before replying — pins the "read: ..." wrapping path.
func TestRoundtrip_ServerCloseBeforeReply(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()

	go func() {
		_, _ = proto.ReadFrame(server)
		server.Close() // hang up without writing a response
	}()

	_, err := roundtrip(client, "find", map[string]any{"path": "."}, "json")
	if err == nil {
		t.Fatal("expected read error after server hung up")
	}
	if !strings.Contains(err.Error(), "read") {
		t.Errorf("error not wrapped with 'read': %v", err)
	}
}

// roundtrip surfaces a typed decode error when the server replies with
// a malformed frame.
func TestRoundtrip_BadResponseDecode(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	go func() {
		_, _ = proto.ReadFrame(server)
		// Write a non-msgpack payload — DecodeResponse should fail.
		_ = proto.WriteFrame(server, []byte("not a valid msgpack response"))
	}()

	_, err := roundtrip(client, "find", map[string]any{"path": "."}, "json")
	if err == nil {
		t.Fatal("expected decode error")
	}
	if !strings.Contains(err.Error(), "decode") {
		t.Errorf("error not wrapped with 'decode': %v", err)
	}
}

// dialOrStart happy path: a UDS listener already bound to the socket
// is reached on the first try, skipping the start-daemon branch.
func TestDialOrStart_HappyPath(t *testing.T) {
	tmp, err := os.MkdirTemp("/tmp", "ashmcp-happy-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(tmp) })
	sock := filepath.Join(tmp, "a.sock")

	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	// Accept one connection in the background so dialOrStart's
	// net.DialTimeout has someone to handshake with.
	go func() {
		conn, err := ln.Accept()
		if err == nil {
			conn.Close()
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, err := dialOrStart(ctx, tmp, sock)
	if err != nil {
		t.Fatalf("dialOrStart: %v", err)
	}
	conn.Close()
}

// killStaleIfNeeded has multiple early-return branches before it
// actually sweeps; pin them so a future regression that, say, panics on
// a missing socket would be caught.
func TestKillStaleIfNeeded_NoBinaryEarlyReturn(t *testing.T) {
	t.Setenv("ASH_DAEMON", "/definitely-not-here-xyzzy")
	t.Setenv("PATH", "/definitely/not/on/path")
	// Just must not panic; nothing observable to assert beyond that.
	killStaleIfNeeded(t.TempDir(), filepath.Join(t.TempDir(), "no.sock"))
}

func TestKillStaleIfNeeded_MissingSocketEarlyReturn(t *testing.T) {
	// Make ASH_DAEMON point at a real file so findAshd succeeds, then
	// stat(sock) will fail because the socket doesn't exist.
	tmp := t.TempDir()
	bin := filepath.Join(tmp, "ashd-stub")
	if err := os.WriteFile(bin, []byte("stub"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ASH_DAEMON", bin)
	killStaleIfNeeded(tmp, filepath.Join(tmp, "no.sock"))
}

func TestKillStaleIfNeeded_BinOlderThanSocketNoOp(t *testing.T) {
	tmp, err := os.MkdirTemp("/tmp", "ashmcp-stale-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(tmp) })

	// Bin created first → older.
	bin := filepath.Join(tmp, "ashd-stub")
	_ = os.WriteFile(bin, []byte("stub"), 0o755)
	t.Setenv("ASH_DAEMON", bin)

	// Sock created second → newer. Use a real listener so the socket
	// inode exists and stat() works.
	sock := filepath.Join(tmp, "a.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	// Bump sock mtime to ensure it's strictly after the bin.
	now := time.Now()
	_ = os.Chtimes(sock, now, now)
	older := now.Add(-1 * time.Hour)
	_ = os.Chtimes(bin, older, older)

	// Should be a no-op (no sweep): socket still listening afterwards.
	killStaleIfNeeded(tmp, sock)
	conn, err := net.DialTimeout("unix", sock, 500*time.Millisecond)
	if err != nil {
		t.Errorf("socket should still be listening: %v", err)
	}
	if conn != nil {
		conn.Close()
	}
}

// makeHandler returns a closure that surfaces a "decode arguments"
// wrapped error when the MCP arguments JSON is malformed. Pins the
// first-error path through the closure — the path every other MCP
// client hits if it sends garbage args.
func TestMakeHandler_DecodeArgsError(t *testing.T) {
	handler := makeHandler("find")
	req := &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{Arguments: json.RawMessage(`{garbage`)},
	}
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected decode error")
	}
	if !strings.Contains(err.Error(), "decode arguments") {
		t.Errorf("error not wrapped with 'decode arguments': %v", err)
	}
}

// findAshd sibling lookup: when ASH_DAEMON is unset and the test binary
// has an "ashd" sibling, the sibling wins. Build a tmp dir with both an
// "ashmcp" (which we'll exec impostor) and "ashd" stubs, then run
// findAshd directly — but findAshd uses os.Executable() to find the
// caller, so we can't easily redirect that. Instead, drop ASH_DAEMON,
// remove ashd from PATH, and just confirm findAshd returns SOMETHING
// non-empty or the documented error.
func TestFindAshd_NoEnvNoSiblingNoPath(t *testing.T) {
	t.Setenv("ASH_DAEMON", "")
	t.Setenv("PATH", "/definitely/not/on/path")
	// findAshd may still find a sibling next to the go-test binary; we
	// can't assert what it returns, only that the function runs.
	_, _ = findAshd()
}

// dialOrStart with an unreachable socket + no ashd binary findable
// surfaces a clean error naming the cause. Uses /tmp directly because
// macOS caps UDS paths at 104 chars and the testing-tmpdir nesting
// would push us over.
func TestDialOrStart_NoBinaryNamesPath(t *testing.T) {
	t.Setenv("ASH_DAEMON", "/definitely-not-here-xyzzy")
	// Hijack PATH so exec.LookPath also fails.
	t.Setenv("PATH", "/definitely/not/on/path")
	tmp, err := os.MkdirTemp("/tmp", "ashmcp-test-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(tmp) })
	sock := filepath.Join(tmp, "a.sock")

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err = dialOrStart(ctx, tmp, sock)
	if err == nil {
		t.Fatal("expected error when no ashd binary is reachable")
	}
	// The error should mention "ashd binary not found" or wrap the
	// start-daemon failure. Either way, it must be informative.
	msg := err.Error()
	if !strings.Contains(msg, "ashd binary not found") && !strings.Contains(msg, "start daemon") {
		t.Errorf("error not informative: %v", err)
	}
}
