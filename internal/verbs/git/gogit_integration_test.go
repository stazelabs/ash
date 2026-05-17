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

// TestRunDiff_Gogit_UnstagedFullPatch verifies the ASH-66 contract:
// gogit unstaged diffs now produce full unified-diff patch text via
// our custom FilePatch + UnifiedEncoder path, matching shellout's
// behavior. The pre-ASH-66 "counts only" divergence is gone.
func TestRunDiff_Gogit_UnstagedFullPatch(t *testing.T) {
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
	if res.Files[0].Patch == "" {
		t.Fatal("ASH-66: gogit unstaged should now produce patch text")
	}
	if !strings.Contains(res.Files[0].Patch, "+\tprintln(\"hello\")") {
		t.Errorf("patch missing expected addition: %s", res.Files[0].Patch)
	}
	if !strings.Contains(res.Files[0].Patch, "-func main() {}") {
		t.Errorf("patch missing expected deletion: %s", res.Files[0].Patch)
	}
}

// TestRunDiff_Gogit_StagedFullPatch covers staged-mode patch text:
// add the modification to the index and check that --staged emits
// the full diff against HEAD.
func TestRunDiff_Gogit_StagedFullPatch(t *testing.T) {
	withGogit(t)
	dir := fixtureRepo(t)
	gitBin, _ := exec.LookPath("git")
	cmd := exec.Command(gitBin, "add", "main.go")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	a := &Args{Op: "diff", Path: dir, Staged: true,
		Context: DiffDefaultContext, LimitBytes: DiffDefaultLimitBytes}
	res, perr := runDiff(a, nil)
	if perr != nil {
		t.Fatalf("runDiff staged: %+v", perr)
	}
	if len(res.Files) != 1 || res.Files[0].Path != "main.go" {
		t.Fatalf("expected one file main.go, got %+v", res.Files)
	}
	if res.Files[0].Patch == "" {
		t.Fatal("ASH-66: gogit staged should produce patch text")
	}
	if !strings.Contains(res.Files[0].Patch, "+\tprintln(\"hello\")") {
		t.Errorf("staged patch missing expected addition: %s", res.Files[0].Patch)
	}
}

// TestRunDiff_Gogit_UntrackedFile verifies a never-tracked file shows
// up as a "new file mode" patch under gogit unstaged.
func TestRunDiff_Gogit_UntrackedFile(t *testing.T) {
	withGogit(t)
	dir := fixtureRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "new.go"),
		[]byte("package main\n\nfunc helper() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := &Args{Op: "diff", Path: dir, Context: DiffDefaultContext, LimitBytes: DiffDefaultLimitBytes}
	res, perr := runDiff(a, nil)
	if perr != nil {
		t.Fatalf("runDiff untracked: %+v", perr)
	}
	var fp *DiffFile
	for i := range res.Files {
		if res.Files[i].Path == "new.go" {
			fp = &res.Files[i]
			break
		}
	}
	if fp == nil {
		t.Fatalf("new.go not found in result: %+v", res.Files)
	}
	if fp.Status != "A" {
		t.Errorf("new.go status=%q want A", fp.Status)
	}
	if !strings.Contains(fp.Patch, "new file mode") {
		t.Errorf("untracked patch missing \"new file mode\": %s", fp.Patch)
	}
}

// TestRunDiff_Gogit_DeletedFile verifies a removed (but committed)
// file shows up as a "deleted file mode" staged patch.
func TestRunDiff_Gogit_DeletedFile(t *testing.T) {
	withGogit(t)
	dir := fixtureRepo(t)
	if err := os.Remove(filepath.Join(dir, "main.go")); err != nil {
		t.Fatal(err)
	}
	gitBin, _ := exec.LookPath("git")
	cmd := exec.Command(gitBin, "add", "-A")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	a := &Args{Op: "diff", Path: dir, Staged: true,
		Context: DiffDefaultContext, LimitBytes: DiffDefaultLimitBytes}
	res, perr := runDiff(a, nil)
	if perr != nil {
		t.Fatalf("runDiff deleted: %+v", perr)
	}
	if len(res.Files) != 1 || res.Files[0].Path != "main.go" {
		t.Fatalf("expected one file main.go, got %+v", res.Files)
	}
	if res.Files[0].Status != "D" {
		t.Errorf("main.go status=%q want D", res.Files[0].Status)
	}
	if !strings.Contains(res.Files[0].Patch, "deleted file mode") {
		t.Errorf("deleted patch missing \"deleted file mode\": %s", res.Files[0].Patch)
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
