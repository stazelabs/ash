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

	runners := verbs.Runners(led)
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
			go handle(conn, led, runners, pretty)
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
		verb string
		args map[string]any
	}{
		{"help", map[string]any{}},
		{"read", map[string]any{"path": goMod}},
		{"find", map[string]any{"path": repoRoot, "max_depth": "1"}},
		{"grep", map[string]any{"pattern": "module", "path": goMod}},
		{"git", map[string]any{"op": "status", "path": repoRoot}},
		{"git", map[string]any{"op": "show", "ref": "HEAD", "path": repoRoot, "stat": "true"}},
		{"stat", map[string]any{"paths": goMod}},
		{"write", map[string]any{"path": writeTarget, "content": "hello"}},
		{"edit", map[string]any{"path": writeTarget, "old_string": "hello", "new_string": "world"}},
		{"diff", map[string]any{"path": goMod, "content": "different", "stat": "true"}},
		{"metrics", map[string]any{}},
		{"report", map[string]any{}},
		{"hook", map[string]any{"tool_name": "Bash", "command": "grep foo bar.txt"}},
		{"bench", map[string]any{"limit": "1"}},
		// test: invoke with a regex that matches no test names so the
		// verb returns quickly. Uses internal/runner (no test files) to
		// avoid recursive go-test work in the integration suite.
		{"test", map[string]any{"packages": "internal/runner", "run": "NoSuchTestZZZ", "timeout": "30s"}},
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
			if !rsp.OK {
				t.Fatalf("OK=false err=%+v", rsp.Err)
			}
			if rsp.Data == nil {
				t.Fatal("OK=true but Data is nil")
			}
		})
	}
}
