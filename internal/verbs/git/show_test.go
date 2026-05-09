package git

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunShow_RefRequired(t *testing.T) {
	_, perr := runShow(&Args{Op: "show", Path: "."}, nil)
	if perr == nil || perr.Code != "args" {
		t.Fatalf("expected args error for missing ref, got %+v", perr)
	}
}

func TestShowRunError_Classification(t *testing.T) {
	cases := []struct {
		stderr string
		ref    string
		code   string
	}{
		{"fatal: bad revision 'zzz'", "zzz", "ref_not_found"},
		{"fatal: ambiguous argument 'zzz': unknown revision or path", "zzz", "ref_not_found"},
		{"fatal: not a git repository (or any of the parent directories)", "HEAD", "not_a_repo"},
		{"fatal: something else weird", "HEAD", "git_failed"},
		{"", "HEAD", "git_failed"},
	}
	for _, c := range cases {
		perr := showRunError("/some/path", []byte(c.stderr), c.ref)
		if perr.Code != c.code {
			t.Errorf("stderr=%q ref=%q: code=%q want %q (msg=%q)", c.stderr, c.ref, perr.Code, c.code, perr.Msg)
		}
	}
}

func TestPrettyShow_RootCommit(t *testing.T) {
	s := &ShowResult{
		Commit: Commit{
			ShortSHA:    "abc1234",
			Subject:     "init",
			AuthorName:  "Alice",
			AuthorEmail: "a@example.com",
			AuthorTime:  1_700_000_000_000_000_000, // unix ns
			Parents:     nil,                       // root commit
		},
		Diff: DiffResult{
			TotalAdditions: 5,
			TotalDeletions: 0,
			Files: []DiffFile{
				{Path: "a.txt", Status: "A", Additions: 5, Patch: "diff --git a/a.txt b/a.txt\n+hello\n"},
			},
		},
	}
	out := prettyShow(s)
	for _, want := range []string{
		"abc1234 — init",
		"author: Alice <a@example.com>",
		"parents: (root commit)",
		"+5 -0 in 1 file",
		"+hello",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("pretty output missing %q\nactual:\n%s", want, out)
		}
	}
}

func TestPrettyShow_NormalCommit_StatMode(t *testing.T) {
	s := &ShowResult{
		Commit: Commit{
			ShortSHA:    "deadbee",
			Subject:     "ASH-X: do thing",
			AuthorName:  "Chris",
			AuthorEmail: "c@example.com",
			AuthorTime:  1_700_000_000_000_000_000,
			Parents:     []string{"f00f00f00f00f00f00f00f00f00f00f00f00f00f"},
		},
		Diff: DiffResult{
			StatOnly:       true,
			TotalAdditions: 12,
			TotalDeletions: 3,
			Files: []DiffFile{
				{Path: "a.go", Additions: 10, Deletions: 2},
				{Path: "b.go", Additions: 2, Deletions: 1},
			},
		},
	}
	out := prettyShow(s)
	for _, want := range []string{
		"deadbee — ASH-X: do thing",
		"parents: f00f00f", // first 7 chars of parent SHA
		"+12 -3 in 2 files",
		"+10",
		"a.go",
		"b.go",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("pretty output missing %q\nactual:\n%s", want, out)
		}
	}
	// Stat-mode pretty must NOT include patch text.
	if strings.Contains(out, "diff --git") {
		t.Errorf("stat-mode pretty leaked diff body:\n%s", out)
	}
}

// -- integration: real repo via git init ----------------------------------

func TestRunShow_Integration(t *testing.T) {
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
	mustRun(gitBin, "init", "-q", "-b", "main")
	mustRun(gitBin, "config", "user.email", "test@example.com")
	mustRun(gitBin, "config", "user.name", "Test")

	// First commit (root): introduces a.txt with two lines.
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(gitBin, "add", "a.txt")
	mustRun(gitBin, "commit", "-q", "-m", "first commit")

	// Second commit: modifies a.txt and adds b.txt.
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(gitBin, "add", "a.txt", "b.txt")
	mustRun(gitBin, "commit", "-q", "-m", "second commit")

	// show HEAD (the second commit, has a parent)
	res, perr := runShow(&Args{
		Op:         "show",
		Path:       dir,
		Ref:        "HEAD",
		Context:    DiffDefaultContext,
		LimitBytes: DiffDefaultLimitBytes,
	}, nil)
	if perr != nil {
		t.Fatalf("runShow HEAD: %+v", perr)
	}
	if res.Commit.Subject != "second commit" {
		t.Errorf("subject=%q want 'second commit'", res.Commit.Subject)
	}
	if len(res.Commit.Parents) != 1 {
		t.Errorf("expected 1 parent, got %v", res.Commit.Parents)
	}
	if len(res.Diff.Files) != 2 {
		t.Errorf("expected 2 changed files, got %d: %+v", len(res.Diff.Files), res.Diff.Files)
	}
	// a.txt: +1, b.txt: +1 (new file)
	if res.Diff.TotalAdditions != 2 {
		t.Errorf("total additions=%d want 2", res.Diff.TotalAdditions)
	}

	// show HEAD~1 (the root commit, no parent — exercises empty-tree path)
	root, perr := runShow(&Args{
		Op:         "show",
		Path:       dir,
		Ref:        "HEAD~1",
		Context:    DiffDefaultContext,
		LimitBytes: DiffDefaultLimitBytes,
	}, nil)
	if perr != nil {
		t.Fatalf("runShow HEAD~1: %+v", perr)
	}
	if root.Commit.Subject != "first commit" {
		t.Errorf("root subject=%q want 'first commit'", root.Commit.Subject)
	}
	if len(root.Commit.Parents) != 0 {
		t.Errorf("expected root commit (no parents), got %v", root.Commit.Parents)
	}
	if len(root.Diff.Files) != 1 || root.Diff.Files[0].Path != "a.txt" {
		t.Errorf("root diff: expected 1 file (a.txt), got %+v", root.Diff.Files)
	}
	if root.Diff.TotalAdditions != 2 {
		t.Errorf("root additions=%d want 2 (one\\ntwo\\n)", root.Diff.TotalAdditions)
	}

	// stat mode: same commit, no patch body
	statRes, perr := runShow(&Args{
		Op:         "show",
		Path:       dir,
		Ref:        "HEAD",
		StatOnly:   true,
		Context:    DiffDefaultContext,
		LimitBytes: DiffDefaultLimitBytes,
	}, nil)
	if perr != nil {
		t.Fatalf("runShow stat: %+v", perr)
	}
	if !statRes.Diff.StatOnly {
		t.Errorf("expected StatOnly=true")
	}
	for _, f := range statRes.Diff.Files {
		if f.Patch != "" {
			t.Errorf("stat mode leaked patch on %s: %q", f.Path, f.Patch)
		}
	}

	// bad ref: ref_not_found
	_, perr = runShow(&Args{Op: "show", Path: dir, Ref: "no-such-ref-zzz"}, nil)
	if perr == nil || perr.Code != "ref_not_found" {
		t.Errorf("expected ref_not_found, got %+v", perr)
	}
}
