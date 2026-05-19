package bench

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestRunBash_EmptyArgv(t *testing.T) {
	res := RunBash(context.Background(), nil)
	if res.RunErr == "" {
		t.Error("empty argv should set RunErr")
	}
}

func TestRunBash_LookupFailure(t *testing.T) {
	res := RunBash(context.Background(), []string{"definitely-not-on-path-xyzzy"})
	if !strings.Contains(res.RunErr, "lookup") {
		t.Errorf("expected lookup error in RunErr: got %q", res.RunErr)
	}
}

func TestRunBash_SuccessCapturesStdoutAndLatency(t *testing.T) {
	res := RunBash(context.Background(), []string{"sh", "-c", "printf hello"})
	if res.RunErr != "" {
		t.Fatalf("unexpected RunErr: %q", res.RunErr)
	}
	if res.ExitCode != 0 {
		t.Errorf("ExitCode: got %d, want 0", res.ExitCode)
	}
	if string(res.Stdout) != "hello" {
		t.Errorf("Stdout: got %q, want %q", string(res.Stdout), "hello")
	}
	if res.Latency <= 0 {
		t.Errorf("Latency should be positive: got %v", res.Latency)
	}
	if res.Truncate {
		t.Error("small output should not be truncated")
	}
}

// TestRunBash_NonZeroExitNotAnError — bash tools like grep return 1 on
// no-match. The runner must surface this as ExitCode, not RunErr.
func TestRunBash_NonZeroExitNotAnError(t *testing.T) {
	res := RunBash(context.Background(), []string{"sh", "-c", "exit 7"})
	if res.RunErr != "" {
		t.Errorf("non-zero exit should not produce RunErr: got %q", res.RunErr)
	}
	if res.ExitCode != 7 {
		t.Errorf("ExitCode: got %d, want 7", res.ExitCode)
	}
}

// TestRunBash_StderrCaptured — pin that stderr is captured separately
// from stdout. Diagnosis depends on it.
func TestRunBash_StderrCaptured(t *testing.T) {
	res := RunBash(context.Background(), []string{"sh", "-c", "printf out; printf err >&2"})
	if string(res.Stdout) != "out" {
		t.Errorf("Stdout: got %q, want %q", string(res.Stdout), "out")
	}
	if string(res.Stderr) != "err" {
		t.Errorf("Stderr: got %q, want %q", string(res.Stderr), "err")
	}
}

// TestRunBash_TimeoutFromContext pins what the runner DOES on context
// cancellation, not what it SHOULD do. Today the SIGKILL from
// context cancellation manifests as exec.ExitError, which the runner
// classifies as a "normal" non-zero exit — so RunErr stays empty and
// only ExitCode != 0 / short elapsed signal the timeout. This is a
// known limitation worth tracking separately; see ASH-192 for the
// follow-up note. The test exists to (a) cover the timeout path and
// (b) catch a future regression that makes the runner hang past the
// deadline.
func TestRunBash_TimeoutFromContext(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	res := RunBash(ctx, []string{"sh", "-c", "sleep 5"})
	elapsed := time.Since(start)
	if elapsed >= 2*time.Second {
		t.Fatalf("runner did not honor context timeout: elapsed %s", elapsed)
	}
	timedOut := strings.Contains(res.RunErr, "timeout") || res.ExitCode != 0
	if !timedOut {
		t.Errorf("expected RunErr=timeout or non-zero ExitCode after ctx cancel; got RunErr=%q ExitCode=%d",
			res.RunErr, res.ExitCode)
	}
}
