package git

// Integration tests for the go-git backend. Build a small fixture repo
// in a t.TempDir() and exercise each op end-to-end. Skip when system git
// is unavailable since we use it to seed the fixture (fast and avoids
// re-implementing git init / commit in test code).

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// withGogit ensures the test runs against gogit and restores the prior
// backend on cleanup so a parallel test that pinned shellout isn't
// disrupted.
func withGogit(t *testing.T) {
	t.Helper()
	prev := currentBackend()
	if err := SetBackend("go-git"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		switch prev {
		case backendShellout:
			_ = SetBackend("shellout")
		default:
			_ = SetBackend("go-git")
		}
	})
}

// fixtureRepo builds a small repo with one base commit and an unstaged
// modification. Returns the repo dir.
func fixtureRepo(t *testing.T) string {
	t.Helper()
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not on PATH, skipping go-git integration (need git to seed the fixture)")
	}
	dir := t.TempDir()
	mustRun := func(args ...string) {
		t.Helper()
		cmd := exec.Command(gitBin, args...)
		cmd.Dir = dir
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, stderr.String())
		}
	}
	mustRun("init", "-q", "-b", "main")
	mustRun("config", "user.email", "test@example.com")
	mustRun("config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(dir, "main.go"),
		[]byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun("add", "main.go")
	mustRun("commit", "-q", "-m", "init")
	if err := os.WriteFile(filepath.Join(dir, "main.go"),
		[]byte("package main\n\nfunc main() {\n\tprintln(\"hello\")\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestRunStatus_Gogit(t *testing.T) {
	withGogit(t)
	dir := fixtureRepo(t)
	a := &Args{Op: "status", Path: dir, Untracked: true}
	res, perr := runStatus(a, nil)
	if perr != nil {
		t.Fatalf("runStatus: %+v", perr)
	}
	if res.Branch != "main" {
		t.Errorf("branch=%q want main", res.Branch)
	}
	if res.Initial {
		t.Error("repo has a commit; initial should be false")
	}
	if res.Clean {
		t.Error("we modified main.go; status should not be clean")
	}
	found := false
	for _, fc := range res.Unstaged {
		if fc.Path == "main.go" && fc.Status == "M" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("unstaged main.go (M) not found: %+v", res.Unstaged)
	}
}

func TestRunLog_Gogit(t *testing.T) {
	withGogit(t)
	dir := fixtureRepo(t)
	a := &Args{Op: "log", Path: dir, Limit: 10}
	res, perr := runLog(a, nil)
	if perr != nil {
		t.Fatalf("runLog: %+v", perr)
	}
	if res.Count != 1 {
		t.Errorf("want 1 commit, got %d", res.Count)
	}
	if len(res.Commits) == 0 || res.Commits[0].Subject != "init" {
		t.Errorf("first commit subject mismatch: %+v", res.Commits)
	}
	if res.Commits[0].AuthorName != "Test" {
		t.Errorf("author name=%q", res.Commits[0].AuthorName)
	}
	if len(res.Commits[0].SHA) != 40 {
		t.Errorf("expected full 40-char SHA, got %q", res.Commits[0].SHA)
	}
	if res.Commits[0].ShortSHA == "" {
		t.Error("ShortSHA empty")
	}
}

func TestRunDiff_Gogit_RangeFullPatch(t *testing.T) {
	withGogit(t)
	dir := fixtureRepo(t)
	// Add a second commit so we have a usable range.
	gitBin, _ := exec.LookPath("git")
	cmd := exec.Command(gitBin, "add", "main.go")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command(gitBin, "commit", "-q", "-m", "second")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}

	a := &Args{Op: "diff", Path: dir, Range: "HEAD~1..HEAD",
		Context: DiffDefaultContext, LimitBytes: DiffDefaultLimitBytes}
	res, perr := runDiff(a, nil)
	if perr != nil {
		t.Fatalf("runDiff range: %+v", perr)
	}
	if len(res.Files) != 1 {
		t.Fatalf("want 1 file, got %d (%+v)", len(res.Files), res.Files)
	}
	if res.Files[0].Patch == "" {
		t.Error("range diff should produce non-empty patch text")
	}
	if !strings.Contains(res.Files[0].Patch, "+\tprintln(\"hello\")") {
		t.Errorf("patch missing expected addition: %s", res.Files[0].Patch)
	}
}

func TestRunDiff_Gogit_UnstagedCountsOnly(t *testing.T) {
	withGogit(t)
	dir := fixtureRepo(t)
	a := &Args{Op: "diff", Path: dir, Context: DiffDefaultContext, LimitBytes: DiffDefaultLimitBytes}
	res, perr := runDiff(a, nil)
	if perr != nil {
		t.Fatalf("runDiff unstaged: %+v", perr)
	}
	if len(res.Files) != 1 || res.Files[0].Path != "main.go" {
		t.Fatalf("expected one file main.go, got %+v", res.Files)
	}
	if res.Files[0].Additions == 0 {
		t.Error("expected additions > 0 for unstaged change")
	}
	// Documented divergence: gogit unstaged returns counts only.
	if res.Files[0].Patch != "" {
		t.Errorf("gogit unstaged should NOT produce patch text (use shellout for that); got %q", res.Files[0].Patch)
	}
}

func TestRunShow_Gogit(t *testing.T) {
	withGogit(t)
	dir := fixtureRepo(t)
	a := &Args{Op: "show", Path: dir, Ref: "HEAD",
		Context: DiffDefaultContext, LimitBytes: DiffDefaultLimitBytes}
	res, perr := runShow(a, nil)
	if perr != nil {
		t.Fatalf("runShow: %+v", perr)
	}
	if res.Commit.Subject != "init" {
		t.Errorf("commit subject=%q want init", res.Commit.Subject)
	}
	if len(res.Diff.Files) == 0 {
		t.Error("expected at least one file in show diff")
	}
}
