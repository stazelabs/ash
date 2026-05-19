package bench

import (
	"os/exec"
	"runtime"
	"testing"
	"time"
)

// TestCaptureProvenance_NonRepoStillPopulatesPlatformAndUptime — when
// projectRoot isn't a git repo, the git-derived fields stay zero but
// platform / CPU / uptime / AshVersion / CaseSetVersion still
// populate. Pinning the shape so a missing field is loud.
func TestCaptureProvenance_NonRepoStillPopulatesPlatformAndUptime(t *testing.T) {
	tmp := t.TempDir()
	start := time.Now().Add(-2 * time.Second)
	p := CaptureProvenance(start, tmp)

	if p.Platform != runtime.GOOS+"/"+runtime.GOARCH {
		t.Errorf("Platform: got %q, want %s/%s", p.Platform, runtime.GOOS, runtime.GOARCH)
	}
	if p.CPUCount != runtime.NumCPU() {
		t.Errorf("CPUCount: got %d, want %d", p.CPUCount, runtime.NumCPU())
	}
	if p.DaemonUptimeUs <= 0 {
		t.Errorf("DaemonUptimeUs should be positive: got %d", p.DaemonUptimeUs)
	}
	if p.CaseSetVersion == "" {
		t.Error("CaseSetVersion should be populated")
	}
	if p.AshVersion == "" {
		t.Error("AshVersion should be populated")
	}
	if p.RepoSHA != "" {
		t.Errorf("RepoSHA should be empty for non-repo dir: got %q", p.RepoSHA)
	}
	if p.AshCommitSHA != "" {
		t.Errorf("AshCommitSHA should be empty for non-repo dir: got %q", p.AshCommitSHA)
	}
}

// TestReadRepoSHA_InitializedRepo creates a one-commit repo via the
// real git binary and verifies SHA capture + clean-state detection.
// Skips when git is not on PATH (rare on dev machines).
func TestReadRepoSHA_InitializedRepo(t *testing.T) {
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git binary not available")
	}
	tmp := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(gitBin, args...)
		cmd.Dir = tmp
		// Suppress identity warnings.
		cmd.Env = append(cmd.Env, "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
			"HOME="+tmp, "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	run("commit", "--allow-empty", "-m", "first", "-q")

	sha, dirty, ok := readRepoSHA(tmp)
	if !ok {
		t.Fatal("expected ok=true for initialized repo")
	}
	if len(sha) != 40 {
		t.Errorf("SHA should be 40 hex chars: got %q (len=%d)", sha, len(sha))
	}
	if dirty {
		t.Errorf("fresh empty-commit repo should be clean: dirty=%v", dirty)
	}
}

func TestReadRepoSHA_NotARepo(t *testing.T) {
	_, _, ok := readRepoSHA(t.TempDir())
	if ok {
		t.Error("readRepoSHA on plain tmpdir should return ok=false")
	}
}
