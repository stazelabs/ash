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

// blameFixtureRepo builds a 2-commit history on a 4-line file. Commit A
// introduces lines 1-3; commit B replaces line 2 and appends line 4. Blame
// at HEAD therefore attributes lines 1,3 to A and lines 2,4 to B.
func blameFixtureRepo(t *testing.T) string {
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
	mustRun("config", "user.email", "alice@example.com")
	mustRun("config", "user.name", "Alice")
	if err := os.WriteFile(filepath.Join(dir, "file.txt"),
		[]byte("alpha\nbravo\ncharlie\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun("add", "file.txt")
	mustRun("commit", "-q", "-m", "commit A")

	mustRun("config", "user.email", "bob@example.com")
	mustRun("config", "user.name", "Bob")
	if err := os.WriteFile(filepath.Join(dir, "file.txt"),
		[]byte("alpha\nBRAVO\ncharlie\ndelta\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun("add", "file.txt")
	mustRun("commit", "-q", "-m", "commit B")
	return dir
}

func TestRunBlame_Gogit(t *testing.T) {
	withGogit(t)
	dir := blameFixtureRepo(t)
	a := &Args{Op: "blame", Path: filepath.Join(dir, "file.txt")}
	res, perr := runBlame(a, nil)
	if perr != nil {
		t.Fatalf("runBlame: %+v", perr)
	}
	if res.Path != "file.txt" {
		t.Errorf("Path=%q want file.txt (repo-root-relative)", res.Path)
	}
	if len(res.Rev) != 40 {
		t.Errorf("Rev should be a full SHA, got %q", res.Rev)
	}
	if len(res.Hunks) != 4 {
		t.Fatalf("want 4 hunks (alpha-A, BRAVO-B, charlie-A, delta-B), got %d: %+v", len(res.Hunks), res.Hunks)
	}
	if res.Hunks[0].AuthorName != "Alice" || res.Hunks[0].StartLine != 1 || res.Hunks[0].Lines[0] != "alpha" {
		t.Errorf("hunk 0 wrong: %+v", res.Hunks[0])
	}
	if res.Hunks[1].AuthorName != "Bob" || res.Hunks[1].StartLine != 2 || res.Hunks[1].Lines[0] != "BRAVO" {
		t.Errorf("hunk 1 wrong: %+v", res.Hunks[1])
	}
	if res.Hunks[2].AuthorName != "Alice" || res.Hunks[2].StartLine != 3 || res.Hunks[2].Lines[0] != "charlie" {
		t.Errorf("hunk 2 wrong: %+v", res.Hunks[2])
	}
	if res.Hunks[3].AuthorName != "Bob" || res.Hunks[3].StartLine != 4 || res.Hunks[3].Lines[0] != "delta" {
		t.Errorf("hunk 3 wrong: %+v", res.Hunks[3])
	}
}

func TestRunBlame_Gogit_LineRange(t *testing.T) {
	withGogit(t)
	dir := blameFixtureRepo(t)
	a := &Args{Op: "blame", Path: filepath.Join(dir, "file.txt"), Lines: "2:3"}
	res, perr := runBlame(a, nil)
	if perr != nil {
		t.Fatalf("runBlame: %+v", perr)
	}
	if len(res.Hunks) != 2 {
		t.Fatalf("want 2 hunks (BRAVO-B, charlie-A), got %d: %+v", len(res.Hunks), res.Hunks)
	}
	if res.Hunks[0].StartLine != 2 || res.Hunks[0].AuthorName != "Bob" {
		t.Errorf("hunk 0: %+v", res.Hunks[0])
	}
	if res.Hunks[1].StartLine != 3 || res.Hunks[1].AuthorName != "Alice" {
		t.Errorf("hunk 1: %+v", res.Hunks[1])
	}
}

func TestRunBlame_Gogit_OpenEndRange(t *testing.T) {
	withGogit(t)
	dir := blameFixtureRepo(t)
	a := &Args{Op: "blame", Path: filepath.Join(dir, "file.txt"), Lines: "3:"}
	res, perr := runBlame(a, nil)
	if perr != nil {
		t.Fatalf("runBlame: %+v", perr)
	}
	if len(res.Hunks) != 2 {
		t.Fatalf("want 2 hunks (charlie-A, delta-B), got %d", len(res.Hunks))
	}
	if res.Hunks[0].StartLine != 3 || res.Hunks[1].StartLine != 4 {
		t.Errorf("start lines wrong: %+v / %+v", res.Hunks[0], res.Hunks[1])
	}
}

func TestRunBlame_Gogit_HistoricalRev(t *testing.T) {
	withGogit(t)
	dir := blameFixtureRepo(t)
	// At commit A (HEAD~1), the file had 3 lines, all by Alice.
	a := &Args{Op: "blame", Path: filepath.Join(dir, "file.txt"), Rev: "HEAD~1"}
	res, perr := runBlame(a, nil)
	if perr != nil {
		t.Fatalf("runBlame at HEAD~1: %+v", perr)
	}
	if len(res.Hunks) != 1 {
		t.Fatalf("expected single Alice hunk at HEAD~1, got %d: %+v", len(res.Hunks), res.Hunks)
	}
	if res.Hunks[0].AuthorName != "Alice" || len(res.Hunks[0].Lines) != 3 {
		t.Errorf("hunk: %+v", res.Hunks[0])
	}
}

func TestRunBlame_Gogit_FileNotInRev(t *testing.T) {
	withGogit(t)
	dir := blameFixtureRepo(t)
	// Add a new file at HEAD and blame it at HEAD~1 — should error
	// with path_not_in_rev because the file didn't exist yet.
	if err := os.WriteFile(filepath.Join(dir, "later.txt"), []byte("only at HEAD\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitBin, _ := exec.LookPath("git")
	for _, args := range [][]string{{"add", "later.txt"}, {"commit", "-q", "-m", "add later.txt"}} {
		cmd := exec.Command(gitBin, args...)
		cmd.Dir = dir
		if err := cmd.Run(); err != nil {
			t.Fatal(err)
		}
	}
	a := &Args{Op: "blame", Path: filepath.Join(dir, "later.txt"), Rev: "HEAD~1"}
	_, perr := runBlame(a, nil)
	if perr == nil || perr.Code != "path_not_in_rev" {
		t.Fatalf("want path_not_in_rev, got %+v", perr)
	}
}

func TestRunBlame_Gogit_BadRev(t *testing.T) {
	withGogit(t)
	dir := blameFixtureRepo(t)
	a := &Args{Op: "blame", Path: filepath.Join(dir, "file.txt"), Rev: "no-such-rev-zzz"}
	_, perr := runBlame(a, nil)
	if perr == nil || perr.Code != "ref_not_found" {
		t.Fatalf("want ref_not_found, got %+v", perr)
	}
}

func TestRunBlame_Gogit_PathIsDir(t *testing.T) {
	withGogit(t)
	dir := blameFixtureRepo(t)
	_, perr := runBlame(&Args{Op: "blame", Path: dir}, nil)
	if perr == nil || perr.Code != "args" {
		t.Fatalf("want args error for directory --path, got %+v", perr)
	}
}

func TestRunBlame_Gogit_PathNotFound(t *testing.T) {
	withGogit(t)
	dir := blameFixtureRepo(t)
	_, perr := runBlame(&Args{Op: "blame", Path: filepath.Join(dir, "does-not-exist.txt")}, nil)
	if perr == nil || perr.Code != "path_not_found" {
		t.Fatalf("want path_not_found, got %+v", perr)
	}
}

func TestRunBlame_Gogit_StartOutOfBounds(t *testing.T) {
	withGogit(t)
	dir := blameFixtureRepo(t)
	a := &Args{Op: "blame", Path: filepath.Join(dir, "file.txt"), Lines: "100:200"}
	_, perr := runBlame(a, nil)
	if perr == nil || perr.Code != "range_out_of_bounds" {
		t.Fatalf("want range_out_of_bounds, got %+v", perr)
	}
}
