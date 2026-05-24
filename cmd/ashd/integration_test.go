package main

import (
	"net"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stazelabs/ash/internal/ledger"
	"github.com/stazelabs/ash/internal/proto"
	"github.com/stazelabs/ash/internal/verbs"
)

// TestIntegration_AllVerbs is the smoke harness for the daemon dispatch loop.
// It starts a real ashd listener over a Unix domain socket, dials it, and
// fires one minimal request per registered verb. A failure here means
// something is wired wrong: a verb missing from Runners(), missing from
// PrettyHandlers(), or its ParseArgs rejecting valid wire args.
func TestIntegration_AllVerbs(t *testing.T) {
	tmp := t.TempDir()

	// go test runs with CWD = the package directory (cmd/ashd/).
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("repoRoot: %v", err)
	}

	led, err := ledger.Open(filepath.Join(tmp, "ledger.db"), tmp, "integration-test")
	if err != nil {
		t.Fatalf("ledger.Open: %v", err)
	}
	defer led.Close()

	runners := verbs.Runners(led, nil, time.Time{}, "", nil, nil)
	pretty := verbs.PrettyHandlers()

	sockPath := filepath.Join(tmp, "ash-test.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go handle(conn, led, runners, pretty, 0, nil)
		}
	}()

	var reqID atomic.Uint64

	// dial returns a fresh connection per subtest so a partial send in one
	// subtest cannot corrupt the read state for the next.
	dial := func(t *testing.T) net.Conn {
		t.Helper()
		c, err := net.DialTimeout("unix", sockPath, 2*time.Second)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		t.Cleanup(func() { c.Close() })
		return c
	}

	send := func(t *testing.T, c net.Conn, verb string, args map[string]any) *proto.Response {
		t.Helper()
		req := &proto.Request{
			V:    proto.ProtocolVersion,
			ID:   reqID.Add(1),
			Verb: verb,
			Args: args,
		}
		buf, err := proto.EncodeRequest(req)
		if err != nil {
			t.Fatalf("%s: EncodeRequest: %v", verb, err)
		}
		_ = c.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if err := proto.WriteFrame(c, buf); err != nil {
			t.Fatalf("%s: WriteFrame: %v", verb, err)
		}
		_ = c.SetReadDeadline(time.Now().Add(15 * time.Second))
		rspBuf, err := proto.ReadFrame(c)
		if err != nil {
			t.Fatalf("%s: ReadFrame: %v", verb, err)
		}
		rsp, err := proto.DecodeResponse(rspBuf)
		if err != nil {
			t.Fatalf("%s: DecodeResponse: %v", verb, err)
		}
		return rsp
	}

	// Every Runner must have a PrettyHandler and vice versa. A mismatch here
	// means the registry maps drifted — the verb uses a fallback renderer and
	// token counts are wrong.
	t.Run("registry_parity", func(t *testing.T) {
		for verb := range runners {
			if _, ok := pretty[verb]; !ok {
				t.Errorf("verb %q: in Runners but missing from PrettyHandlers", verb)
			}
		}
		for verb := range pretty {
			if _, ok := runners[verb]; !ok {
				t.Errorf("verb %q: in PrettyHandlers but missing from Runners", verb)
			}
		}
	})

	goMod := filepath.Join(repoRoot, "go.mod")
	writeTarget := filepath.Join(tmp, "integration-test.txt")

	// One minimal case per registered verb. Args use string values throughout
	// (matching wire shape from the CLI) so argutil coercions are exercised.
	// write must precede edit since edit reads the file write creates.
	cases := []struct {
		verb    string
		args    map[string]any
		wantErr string // if non-empty, expect OK=false with this error code
	}{
		{"help", map[string]any{}, ""},
		{"read", map[string]any{"path": goMod}, ""},
		{"find", map[string]any{"path": repoRoot, "depth": "1"}, ""},
		{"grep", map[string]any{"pattern": "module", "path": goMod}, ""},
		{"git", map[string]any{"op": "status", "path": repoRoot}, ""},
		{"git", map[string]any{"op": "show", "ref": "HEAD", "path": repoRoot, "stat": "true"}, ""},
		{"stat", map[string]any{"paths": goMod}, ""},
		{"write", map[string]any{"path": writeTarget, "content": "hello"}, ""},
		{"edit", map[string]any{"path": writeTarget, "old": "hello", "new": "world"}, ""},
		{"diff", map[string]any{"path": goMod, "content": "different", "stat": "true"}, ""},
		{"metrics", map[string]any{}, ""},
		{"report", map[string]any{}, ""},
		{"recap", map[string]any{}, ""},
		{"workspace", map[string]any{}, ""},
		// replay queries the same ledger this test created; with no
		// prior calls the result is just empty/zero (no skipped, no
		// replayed). Exercises the in-process dispatch closure.
		{"replay", map[string]any{"session": "all"}, ""},
		{"hook", map[string]any{"tool": "Bash", "command": "grep foo bar.txt"}, ""},
		{"bench", map[string]any{"limit": "1"}, ""},
		// init/uninit pass no_registry=true so the integration test does not
		// scribble entries into the registry. They still exercise the
		// settings.json + .gitignore code paths in tmp.
		{"init", map[string]any{"path": tmp, "no-registry": "true"}, ""},
		{"uninit", map[string]any{"path": tmp, "no-registry": "true"}, ""},
		// test: invoke with a regex that matches no test names so the
		// verb returns quickly. Uses internal/runner (no test files) to
		// avoid recursive go-test work in the integration suite.
		{"test", map[string]any{"packages": "internal/runner", "run": "NoSuchTestZZZ", "timeout": "30s"}, ""},
		// build: small non-main package that compiles quickly. The
		// integration test runs with cwd=cmd/ashd, so an absolute path
		// avoids "./internal/runner"-style resolution failures.
		// Non-main → no binary artifact written.
		{"build", map[string]any{"packages": filepath.Join(repoRoot, "internal/runner"), "timeout": "60s"}, ""},
		// stop is a client-only verb; the daemon returns client_only.
		{"stop", map[string]any{}, "client_only"},
		// usage (post-ASH-185) computes cache-friendliness stats from
		// ledger arg-repetition. Empty args use the defaults
		// (--since 24h --session current); even with no preceding
		// calls in this freshly-opened daemon session the verb
		// returns OK with Calls=0 / PerVerb empty.
		{"usage", map[string]any{}, ""},
		// ASH-188: turn ingests harness-reported Anthropic API turn
		// usage. The Stop hook normally feeds it; here we exercise
		// the verb directly with a minimal valid turn_id. INSERT OR
		// IGNORE keeps repeat runs idempotent.
		{"turn", map[string]any{"turn_id": "msg_integration_test"}, ""},
		// ASH-138: lang dispatches through the LSP broker. Runners is
		// invoked here with broker=nil, so RunWithDeps returns
		// lsp_disabled — exactly the contract we want for a default
		// (broker-off) daemon. The real LSP integration is covered by
		// internal/verbs/lang/lang_test.go.
		{"lang", map[string]any{"op": "outline", "path": goMod}, "lsp_disabled"},
		{"config", map[string]any{}, ""},
	}

	// Fail loudly if a new verb is added to Runners without a corresponding
	// integration case. This keeps the smoke harness exhaustive automatically.
	t.Run("coverage", func(t *testing.T) {
		tested := make(map[string]bool, len(cases))
		for _, tc := range cases {
			tested[tc.verb] = true
		}
		for verb := range runners {
			if !tested[verb] {
				t.Errorf("verb %q registered in Runners but has no integration case — add one", verb)
			}
		}
	})

	for _, tc := range cases {
		t.Run(tc.verb, func(t *testing.T) {
			rsp := send(t, dial(t), tc.verb, tc.args)
			if tc.wantErr != "" {
				if rsp.OK {
					t.Fatalf("expected OK=false (code=%q) but got OK=true", tc.wantErr)
				}
				if rsp.Err == nil || rsp.Err.Code != tc.wantErr {
					t.Fatalf("expected error code %q, got %+v", tc.wantErr, rsp.Err)
				}
				return
			}
			if !rsp.OK {
				t.Fatalf("OK=false err=%+v", rsp.Err)
			}
			if rsp.Data == nil {
				t.Fatal("OK=true but Data is nil")
			}
		})
	}

	// ASH-146: an MCP request with EmitFormat="pretty" must record
	// tokens_out_emit using the daemon-pretty render (== tokens_out),
	// not the JSON envelope. The default (EmitFormat="") path is
	// unchanged: tokens_out_emit tokenizes the JSON envelope, which
	// for structured-record verbs costs more than the pretty form.
	t.Run("emit_format_pretty", func(t *testing.T) {
		sendCustom := func(t *testing.T, c net.Conn, req *proto.Request) *proto.Response {
			t.Helper()
			buf, err := proto.EncodeRequest(req)
			if err != nil {
				t.Fatalf("EncodeRequest: %v", err)
			}
			_ = c.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if err := proto.WriteFrame(c, buf); err != nil {
				t.Fatalf("WriteFrame: %v", err)
			}
			_ = c.SetReadDeadline(time.Now().Add(15 * time.Second))
			rspBuf, err := proto.ReadFrame(c)
			if err != nil {
				t.Fatalf("ReadFrame: %v", err)
			}
			rsp, err := proto.DecodeResponse(rspBuf)
			if err != nil {
				t.Fatalf("DecodeResponse: %v", err)
			}
			return rsp
		}
		statReq := func(format string) *proto.Request {
			return &proto.Request{
				V:          proto.ProtocolVersion,
				ID:         reqID.Add(1),
				Verb:       "stat",
				Args:       map[string]any{"paths": goMod},
				Transport:  proto.TransportMCP,
				EmitFormat: format,
			}
		}
		jsonReq := statReq("")
		jsonRsp := sendCustom(t, dial(t), jsonReq)
		if !jsonRsp.OK {
			t.Fatalf("json mode: %+v", jsonRsp.Err)
		}
		prettyReq := statReq("pretty")
		prettyRsp := sendCustom(t, dial(t), prettyReq)
		if !prettyRsp.OK {
			t.Fatalf("pretty mode: %+v", prettyRsp.Err)
		}

		// Match the two MCP stat rows by request_id. Post-ASH-214 the
		// ledger Record runs after the response frame, so handler-
		// goroutine scheduling — not dispatch order — decides row id
		// order; matching by position is no longer safe. The retry also
		// waits for the pretty row's UpdateMCPEmit to land: Record writes
		// the row with tokens_out_emit still 0, and UpdateMCPEmit patches
		// it a moment later.
		var jsonRow, prettyRow *ledger.Call
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			calls, err := led.QueryRecent(20, "stat")
			if err != nil {
				t.Fatalf("QueryRecent: %v", err)
			}
			jsonRow, prettyRow = nil, nil
			for i := range calls {
				c := calls[i]
				switch c.RequestID {
				case jsonReq.ID:
					jsonRow = &c
				case prettyReq.ID:
					prettyRow = &c
				}
			}
			if jsonRow != nil && prettyRow != nil && prettyRow.TokensOutEmit != 0 {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if jsonRow == nil {
			t.Fatal("ledger missing the json-mode stat row")
		}
		if prettyRow == nil {
			t.Fatal("ledger missing the pretty-mode stat row")
		}
		// Post-ASH-156: json-mode success ships StructuredContent
		// only — no TextContent body. The daemon's emit accounting
		// mirrors that, so tokens_out_emit collapses to 0 for a
		// non-truncated json-mode success. The stat fixture above is
		// not truncated, so the json row should read exactly 0.
		if jsonRow.TokensOutEmit != 0 {
			t.Errorf("json mode (ASH-156): tokens_out_emit = %d; want 0 since success TextContent was dropped", jsonRow.TokensOutEmit)
		}
		if prettyRow.TokensOutEmit == 0 {
			t.Fatalf("pretty mode: tokens_out_emit unpopulated (Transport=mcp should populate it)")
		}
		// Pretty emit must equal pretty tokens_out by construction
		// (they tokenize the same text). Drift would mean the daemon
		// is using a different rendering for accounting than for the
		// wire — the exact fidelity bug ASH-123 set out to fix.
		if prettyRow.TokensOutEmit != prettyRow.TokensOut {
			t.Errorf("pretty: tokens_out_emit (%d) != tokens_out (%d); both should tokenize the daemon-pretty render", prettyRow.TokensOutEmit, prettyRow.TokensOut)
		}
	})
}
