package runner

import (
	"os/exec"
	"testing"
)

func requireSh(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not on PATH")
	}
}

func TestRun_BinaryNotFound(t *testing.T) {
	_, perr := Run("ash-no-such-binary-xyzzy", nil, Opts{})
	if perr == nil {
		t.Fatal("expected a proto.Error for a missing binary")
	}
	if perr.Code != "ash-no-such-binary-xyzzy_not_found" {
		t.Errorf("code = %q, want ...-xyzzy_not_found", perr.Code)
	}
}

func TestRun_Success(t *testing.T) {
	requireSh(t)
	res, perr := Run("sh", []string{"-c", "printf hello"}, Opts{})
	if perr != nil {
		t.Fatalf("Run: %+v", perr)
	}
	if string(res.Stdout) != "hello" {
		t.Errorf("stdout = %q, want hello", res.Stdout)
	}
	if res.ExitCode != 0 {
		t.Errorf("exit = %d, want 0", res.ExitCode)
	}
}

func TestRun_NonZeroExitIsNotAnError(t *testing.T) {
	requireSh(t)
	res, perr := Run("sh", []string{"-c", "exit 3"}, Opts{})
	if perr != nil {
		t.Fatalf("Run returned a proto.Error for a non-zero exit: %+v", perr)
	}
	if res.ExitCode != 3 {
		t.Errorf("exit = %d, want 3", res.ExitCode)
	}
}

func TestRun_StderrCaptured(t *testing.T) {
	requireSh(t)
	res, perr := Run("sh", []string{"-c", "printf oops 1>&2"}, Opts{})
	if perr != nil {
		t.Fatalf("Run: %+v", perr)
	}
	if string(res.Stderr) != "oops" {
		t.Errorf("stderr = %q, want oops", res.Stderr)
	}
}

func TestRun_MaxStdoutTruncates(t *testing.T) {
	requireSh(t)
	res, perr := Run("sh", []string{"-c", "printf aaaaaaaaaa"}, Opts{MaxStdout: 4})
	if perr != nil {
		t.Fatalf("Run: %+v", perr)
	}
	if !res.Truncated {
		t.Error("Truncated = false, want true")
	}
	if len(res.Stdout) != 4 {
		t.Errorf("len(stdout) = %d, want 4 (capped)", len(res.Stdout))
	}
}

func TestRun_MaxStdoutExactlyAtCapNotTruncated(t *testing.T) {
	requireSh(t)
	res, perr := Run("sh", []string{"-c", "printf aaaa"}, Opts{MaxStdout: 4})
	if perr != nil {
		t.Fatalf("Run: %+v", perr)
	}
	if res.Truncated {
		t.Error("Truncated = true for output exactly at the cap")
	}
	if string(res.Stdout) != "aaaa" {
		t.Errorf("stdout = %q, want aaaa", res.Stdout)
	}
}
