package git

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// -- parseStatus unit tests -----------------------------------------------
//
// These don't shell out — they feed canned porcelain v2 strings to the
// parser and assert the structured shape. This keeps the bulk of the
// parser surface CI-runnable without git installed.

func TestParseStatus_CleanRepo(t *testing.T) {
	out := []byte(`# branch.oid d31dd0a
# branch.head main
# branch.upstream origin/main
# branch.ab +0 -0
`)
	s, perr := parseStatus(out)
	if perr != nil {
		t.Fatal(perr)
	}
	if s.Branch != "main" {
		t.Errorf("branch=%q want main", s.Branch)
	}
	if s.Upstream != "origin/main" {
		t.Errorf("upstream=%q want origin/main", s.Upstream)
	}
	if s.Ahead != 0 || s.Behind != 0 {
		t.Errorf("ahead/behind=%d/%d want 0/0", s.Ahead, s.Behind)
	}
	if !s.Clean {
		t.Errorf("expected clean=true on empty status")
	}
	if s.Detached || s.Initial {
		t.Errorf("clean repo shouldn't be detached or initial")
	}
}

func TestParseStatus_AheadBehind(t *testing.T) {
	out := []byte(`# branch.oid abc
# branch.head topic
# branch.upstream origin/topic
# branch.ab +3 -2
`)
	s, _ := parseStatus(out)
	if s.Ahead != 3 || s.Behind != 2 {
		t.Errorf("ahead/behind=%d/%d want 3/2", s.Ahead, s.Behind)
	}
}

func TestParseStatus_DetachedAndInitial(t *testing.T) {
	t.Run("detached", func(t *testing.T) {
		s, _ := parseStatus([]byte("# branch.oid abc\n# branch.head (detached)\n"))
		if !s.Detached || s.Branch != "" {
			t.Errorf("expected detached, got %+v", s)
		}
	})
	t.Run("initial", func(t *testing.T) {
		s, _ := parseStatus([]byte("# branch.oid (initial)\n# branch.head main\n"))
		if !s.Initial {
			t.Errorf("expected initial=true, got %+v", s)
		}
	})
}

func TestParseStatus_Head(t *testing.T) {
	t.Run("on branch", func(t *testing.T) {
		s, _ := parseStatus([]byte("# branch.oid abc1234def5678abc1234def5678abc1234def5678\n# branch.head main\n"))
		if s.Head != "main" {
			t.Errorf("Head=%q want main", s.Head)
		}
	})
	t.Run("detached", func(t *testing.T) {
		s, _ := parseStatus([]byte("# branch.oid 45b4bfb1234567890123456789012345678901234\n# branch.head (detached)\n"))
		if s.Head != "45b4bfb" {
			t.Errorf("Head=%q want 45b4bfb", s.Head)
		}
	})
	t.Run("initial commit", func(t *testing.T) {
		s, _ := parseStatus([]byte("# branch.oid (initial)\n# branch.head main\n"))
		if s.Head != "" {
			t.Errorf("Head=%q want empty for initial commit", s.Head)
		}
	})
}

func TestParseStatus_TrackedSplit(t *testing.T) {
	// "M." means index modified, worktree clean -> Staged.
	// ".M" means index clean, worktree modified -> Unstaged.
	// "MM" means both -> Staged AND Unstaged.
	// "A." means index added, worktree clean -> Staged.
	// ".D" means worktree-deleted -> Unstaged.
	out := []byte(`# branch.oid abc
# branch.head main
1 M. N... 100644 100644 100644 aaa bbb staged_modified.go
1 .M N... 100644 100644 100644 ccc ddd unstaged_modified.go
1 MM N... 100644 100644 100644 eee fff both.go
1 A. N... 100644 100644 100644 ggg hhh new_added.go
1 .D N... 100644 100644 100644 iii jjj deleted_in_worktree.go
`)
	s, _ := parseStatus(out)

	stagedPaths := pathsOf(s.Staged)
	unstagedPaths := pathsOf(s.Unstaged)

	wantStaged := []string{"staged_modified.go", "both.go", "new_added.go"}
	wantUnstaged := []string{"unstaged_modified.go", "both.go", "deleted_in_worktree.go"}
	if !sameSet(stagedPaths, wantStaged) {
		t.Errorf("staged=%v want %v", stagedPaths, wantStaged)
	}
	if !sameSet(unstagedPaths, wantUnstaged) {
		t.Errorf("unstaged=%v want %v", unstagedPaths, wantUnstaged)
	}

	// Status codes preserved as single chars.
	for _, fc := range s.Staged {
		if len(fc.Status) != 1 {
			t.Errorf("status %q for %s should be single char", fc.Status, fc.Path)
		}
	}
	if s.Clean {
		t.Errorf("clean should be false with changes present")
	}
}

func TestParseStatus_RenameRecordsOldPath(t *testing.T) {
	// "2 R. ... R100 newname.go\toldname.go"
	out := []byte("# branch.oid abc\n# branch.head main\n2 R. N... 100644 100644 100644 aaa bbb R100 newname.go\toldname.go\n")
	s, _ := parseStatus(out)
	if len(s.Staged) != 1 {
		t.Fatalf("want 1 staged rename, got %+v", s.Staged)
	}
	if s.Staged[0].Path != "newname.go" || s.Staged[0].OldPath != "oldname.go" {
		t.Errorf("rename: %+v", s.Staged[0])
	}
	if s.Staged[0].Status != "R" {
		t.Errorf("rename status=%q want R", s.Staged[0].Status)
	}
}

func TestParseStatus_UntrackedAndIgnored(t *testing.T) {
	out := []byte(`# branch.oid abc
# branch.head main
? new_file.go
? subdir/another.go
! ignored.log
`)
	s, _ := parseStatus(out)
	if !sameSet(s.Untracked, []string{"new_file.go", "subdir/another.go"}) {
		t.Errorf("untracked=%v", s.Untracked)
	}
	if !sameSet(s.Ignored, []string{"ignored.log"}) {
		t.Errorf("ignored=%v", s.Ignored)
	}
	if s.Clean {
		t.Errorf("untracked files mean not clean")
	}
}

func TestParseStatus_Conflict(t *testing.T) {
	// Unmerged entry: "u <XY> <sub> <m1> <m2> <m3> <mW> <h1> <h2> <h3> <path>"
	out := []byte("# branch.oid abc\n# branch.head main\nu UU N... 100644 100644 100644 100644 aaa bbb ccc conflict.go\n")
	s, _ := parseStatus(out)
	if !sameSet(s.Conflicts, []string{"conflict.go"}) {
		t.Errorf("conflicts=%v", s.Conflicts)
	}
}

// -- ParseArgs ------------------------------------------------------------

func TestParseArgs_RequiresOp(t *testing.T) {
	_, perr := ParseArgs(map[string]any{})
	if perr == nil || perr.Code != "args" {
		t.Fatalf("expected args error for missing op, got %+v", perr)
	}
}

func TestParseArgs_DefaultsAndStatus(t *testing.T) {
	a, perr := ParseArgs(map[string]any{"op": "status"})
	if perr != nil {
		t.Fatalf("unexpected: %+v", perr)
	}
	if a.Path != "." || !a.Untracked || a.Ignored {
		t.Errorf("defaults wrong: %+v", a)
	}
}

// TestParseArgs_WireShape verifies that every numeric and bool arg accepts
// string-typed values (the wire shape from CLI parseFlags) and rejects
// garbage. Guards against a future verb skipping argutil and silently
// breaking the string-coercion path.
func TestParseArgs_WireShape(t *testing.T) {
	a, perr := ParseArgs(map[string]any{
		"op":          "log",
		"limit":       "5",
		"context":     "2",
		"bytes": "512",
		"untracked":   "false",
		"ignored":     "true",
		"staged":      "true",
		"stat":        "true",
	})
	if perr != nil {
		t.Fatalf("valid string args rejected: %v", perr)
	}
	if a.Limit != 5 {
		t.Errorf("limit: got %d, want 5", a.Limit)
	}
	if a.Context != 2 {
		t.Errorf("context: got %d, want 2", a.Context)
	}
	if a.LimitBytes != 512 {
		t.Errorf("limit_bytes: got %d, want 512", a.LimitBytes)
	}
	if a.Untracked {
		t.Error("untracked: want false")
	}
	if !a.Ignored {
		t.Error("ignored: want true")
	}
	if !a.Staged {
		t.Error("staged: want true")
	}
	if !a.StatOnly {
		t.Error("stat: want true")
	}

	for _, bad := range []struct{ key, val string }{
		{"limit", "abc"},
		{"context", "abc"},
		{"bytes", "abc"},
		{"untracked", "maybe"},
		{"ignored", "maybe"},
		{"staged", "maybe"},
		{"stat", "maybe"},
	} {
		_, perr := ParseArgs(map[string]any{"op": "status", bad.key: bad.val})
		if perr == nil {
			t.Errorf("expected error for %s=%q", bad.key, bad.val)
		}
	}
}

// TestParseArgs_RejectsDashPrefixedRevArgs covers the ASH-211 guard: a
// rev/pathspec value beginning with "-" is rejected before it can reach
// the git argv. git would otherwise read --range '--output=X' as an
// option, and `git log --output=FILE` writes attacker-chosen files.
func TestParseArgs_RejectsDashPrefixedRevArgs(t *testing.T) {
	for _, arg := range []string{"range", "author", "since", "until", "pathspec", "ref", "rev"} {
		_, perr := ParseArgs(map[string]any{"op": "log", arg: "--output=/tmp/pwned"})
		if perr == nil || perr.Code != "args" {
			t.Errorf("%s='--output=...': expected args error, got %+v", arg, perr)
		}
	}
	if _, perr := ParseArgs(map[string]any{"op": "log", "range": "HEAD~3..HEAD"}); perr != nil {
		t.Fatalf("legit range rejected: %+v", perr)
	}
}

// -- integration smoke test ----------------------------------------------
//
// Builds a real repo via `git init`, exercises Run, and asserts the
// shape. Skipped if git isn't on PATH so CI without git still passes.

func TestRunStatus_Integration(t *testing.T) {
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not on PATH, skipping integration")
	}

	dir := t.TempDir()
	mustRun := func(name string, args ...string) {
		t.Helper()
		cmd := exec.Command(name, args...)
		cmd.Dir = dir
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("%s %v: %v\n%s", name, args, err, stderr.String())
		}
	}
	// Create a minimal repo with a committed file, a staged change, an
	// unstaged change, and an untracked file. Covers the four buckets.
	mustRun(gitBin, "init", "-q", "-b", "main")
	mustRun(gitBin, "config", "user.email", "test@example.com")
	mustRun(gitBin, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("first\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(gitBin, "add", "a.txt")
	mustRun(gitBin, "commit", "-q", "-m", "init")

	// Staged change to a.txt
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("staged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(gitBin, "add", "a.txt")
	// Unstaged change on top
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("staged\nworktree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Untracked
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, perr := runStatus(&Args{Op: "status", Path: dir, Untracked: true}, nil)
	if perr != nil {
		t.Fatalf("runStatus: %+v", perr)
	}
	if res.Branch != "main" {
		t.Errorf("branch=%q want main", res.Branch)
	}
	if res.Clean {
		t.Errorf("expected dirty repo, got clean")
	}
	// a.txt has both index and worktree changes; should be in both buckets.
	if !hasPath(res.Staged, "a.txt") {
		t.Errorf("expected a.txt in staged, got %+v", res.Staged)
	}
	if !hasPath(res.Unstaged, "a.txt") {
		t.Errorf("expected a.txt in unstaged, got %+v", res.Unstaged)
	}
	if !sameSet(res.Untracked, []string{"new.txt"}) {
		t.Errorf("untracked=%v want [new.txt]", res.Untracked)
	}
}

func TestGitDirArg(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := gitDirArg(dir); got != dir {
		t.Errorf("gitDirArg(dir) = %q, want unchanged %q", got, dir)
	}
	if got := gitDirArg(file); got != dir {
		t.Errorf("gitDirArg(file) = %q, want parent %q", got, dir)
	}
	missing := filepath.Join(dir, "does-not-exist")
	if got := gitDirArg(missing); got != missing {
		t.Errorf("gitDirArg(missing) = %q, want unchanged %q", got, missing)
	}
}

func TestRunStatus_NotARepoErrors(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	_, perr := runStatus(&Args{Op: "status", Path: dir, Untracked: true}, nil)
	if perr == nil {
		t.Fatal("expected error for non-repo dir")
	}
	if perr.Code != "not_a_repo" {
		t.Errorf("code=%q want not_a_repo (msg=%q)", perr.Code, perr.Msg)
	}
}

// -- helpers --------------------------------------------------------------

func pathsOf(fs []FileChange) []string {
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		out = append(out, f.Path)
	}
	return out
}

func hasPath(fs []FileChange, p string) bool {
	for _, f := range fs {
		if f.Path == p {
			return true
		}
	}
	return false
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]int{}
	for _, x := range a {
		seen[x]++
	}
	for _, x := range b {
		seen[x]--
	}
	for _, v := range seen {
		if v != 0 {
			return false
		}
	}
	return true
}

// quiet unused-import guard for parts the tests don't reach yet.
var _ = strings.Builder{}
