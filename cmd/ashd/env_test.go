package main

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stazelabs/ash/internal/ledger"
	"github.com/stazelabs/ash/internal/proto"
	"github.com/stazelabs/ash/internal/verbs"
	testverb "github.com/stazelabs/ash/internal/verbs/test"
)

// TestIntegration_TestVerbEnvPassthrough is the wire-level proof that
// ASH-132 plumbs the client's shell env all the way to the `go test`
// subprocess.
//
// Why this matters: ashd inherits its env once at startup, so env vars
// the agent's shell sets *after* the daemon launched (UPDATE_GOLDEN,
// GO* toggles, DEBUG, etc.) silently never reach `go test`. The fix
// threads Request.Env → Tracer → cmd.Env on the test runner.
//
// Method: drive the test verb against internal/envprobe, whose
// TestEnvProbe asserts the exact value of ASH_132_PROBE. Two runs:
//
//   - Env carries ASH_132_PROBE=expected → Result.OK=true (the
//     subprocess saw the override).
//   - Env carries ASH_132_PROBE=wrong → Result.OK=false (TestEnvProbe
//     fails). This asymmetric arm is load-bearing: a plumbing path that
//     accepts the field but quietly drops the contents would still pass
//     the "expected" case via the parent's env, so we send a value the
//     parent could not be sourcing.
//
// The fixture's t.Skip branch keeps `go test ./...` green for
// developers who don't know this fixture exists.
func TestIntegration_TestVerbEnvPassthrough(t *testing.T) {
	if v, ok := os.LookupEnv("ASH_132_PROBE"); ok {
		t.Fatalf("ASH_132_PROBE is set in test process (=%q); the env-passthrough proof requires it absent so we control its value via the wire", v)
	}

	// /tmp prefix dodges the macOS 104-byte SUN_PATH cap; the
	// canonical t.TempDir() under /var/folders busts the limit once
	// we append "ash.sock".
	tmp, err := os.MkdirTemp("/tmp", "ash132-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(tmp) })

	// `go test` resolves package patterns against the active module,
	// which is found from CWD. The unit test's default CWD is
	// cmd/ashd/, where "./internal/envprobe" doesn't exist. chdir to
	// the repo root so the test verb's `go test ./internal/envprobe`
	// resolves against the actual module.
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("repoRoot: %v", err)
	}
	origCWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(repoRoot); err != nil {
		t.Fatalf("chdir to repo root: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(origCWD) })

	led, err := ledger.Open(filepath.Join(tmp, "ledger.db"), tmp, "env-passthrough-test")
	if err != nil {
		t.Fatalf("ledger.Open: %v", err)
	}
	defer led.Close()

	runners := verbs.Runners(led, nil, time.Time{}, "", nil, nil)
	pretty := verbs.PrettyHandlers()
	sockPath := filepath.Join(tmp, "ash.sock")
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
	send := func(t *testing.T, env []string) *testverb.Result {
		t.Helper()
		c, err := net.DialTimeout("unix", sockPath, 2*time.Second)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		defer c.Close()
		req := &proto.Request{
			V:    proto.ProtocolVersion,
			ID:   reqID.Add(1),
			Verb: "test",
			Args: map[string]any{
				"packages": "internal/envprobe",
				"run":      "TestEnvProbe",
				"timeout":  "60s",
			},
			Env: env,
		}
		buf, err := proto.EncodeRequest(req)
		if err != nil {
			t.Fatalf("EncodeRequest: %v", err)
		}
		_ = c.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if err := proto.WriteFrame(c, buf); err != nil {
			t.Fatalf("WriteFrame: %v", err)
		}
		_ = c.SetReadDeadline(time.Now().Add(60 * time.Second))
		rspBuf, err := proto.ReadFrame(c)
		if err != nil {
			t.Fatalf("ReadFrame: %v", err)
		}
		rsp, err := proto.DecodeResponse(rspBuf)
		if err != nil {
			t.Fatalf("DecodeResponse: %v", err)
		}
		if !rsp.OK {
			t.Fatalf("verb-level OK=false: %+v", rsp.Err)
		}
		var res testverb.Result
		if err := proto.UnmarshalData(rsp, &res); err != nil {
			t.Fatalf("UnmarshalData: %v", err)
		}
		return &res
	}

	// withProbe returns a copy of the parent env with ASH_132_PROBE
	// stripped and the supplied value overlaid. Stripping the existing
	// entry guarantees the appended value is what `getenv` sees on
	// platforms where first-match wins, even if some future test (or
	// developer) sets ASH_132_PROBE in their own env.
	withProbe := func(val string) []string {
		out := make([]string, 0, len(os.Environ())+1)
		for _, kv := range os.Environ() {
			if !strings.HasPrefix(kv, "ASH_132_PROBE=") {
				out = append(out, kv)
			}
		}
		return append(out, "ASH_132_PROBE="+val)
	}

	t.Run("expected_value_passes", func(t *testing.T) {
		res := send(t, withProbe("expected"))
		if !res.OK {
			t.Fatalf("expected Result.OK=true with ASH_132_PROBE=expected, got OK=false totals=%+v packages=%+v", res.Total, res.Packages)
		}
		if res.Total.Pass == 0 {
			t.Errorf("expected at least one passing test, got totals=%+v", res.Total)
		}
	})

	t.Run("wrong_value_fails", func(t *testing.T) {
		res := send(t, withProbe("wrong"))
		if res.OK {
			t.Fatalf("expected Result.OK=false with ASH_132_PROBE=wrong, got OK=true totals=%+v", res.Total)
		}
		if res.Total.Fail == 0 {
			t.Errorf("expected at least one failing test, got totals=%+v", res.Total)
		}
	})
}
