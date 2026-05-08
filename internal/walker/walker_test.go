package walker

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// makeTree builds a small fixture used across walker tests.
//
//	root/
//	  a.go
//	  b.go
//	  README.md
//	  .gitignore       (leaf dotfile, findable)
//	  src/
//	    main.go
//	    util.go
//	    deep/
//	      x.go
//	  .git/            (hidden dir, skipped by default)
//	    HEAD
//	  vendor/
//	    pkg/
//	      v.go
func makeTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"a.go":            "package a",
		"b.go":            "package b",
		"README.md":       "# r",
		".gitignore":      "ignore",
		"src/main.go":     "package main",
		"src/util.go":     "package main",
		"src/deep/x.go":   "package deep",
		".git/HEAD":       "ref: refs/heads/main",
		"vendor/pkg/v.go": "package pkg",
	}
	for rel, body := range files {
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func collect(t *testing.T, root string, opts Options) []Entry {
	t.Helper()
	var out []Entry
	err := Walk(root, opts, func(e Entry) (Action, error) {
		out = append(out, e)
		return Continue, nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RelPath < out[j].RelPath })
	return out
}

func relPaths(es []Entry) []string {
	out := make([]string, 0, len(es))
	for _, e := range es {
		out = append(out, e.RelPath)
	}
	return out
}

func contains(xs []string, target string) bool {
	for _, x := range xs {
		if x == target {
			return true
		}
	}
	return false
}

func TestWalk_DefaultsSkipHiddenDirsAndIncludeLeafDotfiles(t *testing.T) {
	root := makeTree(t)
	got := relPaths(collect(t, root, Options{}))
	if !contains(got, ".gitignore") {
		t.Errorf(".gitignore (leaf dotfile) should be visited, got %v", got)
	}
	for _, p := range got {
		if p == ".git" || p == ".git/HEAD" {
			t.Errorf("hidden dir leaked: %s", p)
		}
	}
}

func TestWalk_IncludeHiddenRecursesIntoDotDirs(t *testing.T) {
	root := makeTree(t)
	got := relPaths(collect(t, root, Options{IncludeHidden: true}))
	if !contains(got, ".git/HEAD") {
		t.Errorf("expected .git/HEAD with IncludeHidden=true, got %v", got)
	}
}

func TestWalk_GlobIsVisitGateNotDescentGate(t *testing.T) {
	root := makeTree(t)
	got := relPaths(collect(t, root, Options{Glob: "**/*.go"}))
	want := []string{"a.go", "b.go", "src/deep/x.go", "src/main.go", "src/util.go", "vendor/pkg/v.go"}
	if len(got) != len(want) {
		t.Fatalf("count mismatch\n got: %v\nwant: %v", got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("idx %d: got %q want %q", i, got[i], w)
		}
	}
}

func TestWalk_MaxDepth(t *testing.T) {
	root := makeTree(t)
	got := relPaths(collect(t, root, Options{MaxDepth: 1}))
	for _, p := range got {
		if filepath.Dir(p) != "." {
			t.Errorf("max_depth=1 leak: %s", p)
		}
	}
	if !contains(got, "src") || !contains(got, "vendor") {
		t.Errorf("expected direct children src and vendor: %v", got)
	}
}

// Regression: with root="." WalkDir hands back bare child paths
// ("src" not "./src"), so a separator-counting depth calc was
// off-by-one and let grandchildren ("src/main.go") through MaxDepth=1.
// Surfaced via the find_shallow bench case.
func TestWalk_MaxDepthWithDotRoot(t *testing.T) {
	root := makeTree(t)
	t.Chdir(root)
	got := relPaths(collect(t, ".", Options{MaxDepth: 1}))
	for _, p := range got {
		if filepath.Dir(p) != "." {
			t.Errorf("max_depth=1 leak with . root: %s", p)
		}
	}
	if !contains(got, "src") || !contains(got, "vendor") {
		t.Errorf("expected direct children src and vendor: %v", got)
	}
	if contains(got, "src/main.go") || contains(got, "vendor/pkg") {
		t.Errorf("grandchildren leaked through MaxDepth=1: %v", got)
	}
}

func TestWalk_RespectsGitignoreByDefault(t *testing.T) {
	root := makeTree(t)
	if err := os.WriteFile(filepath.Join(root, ".gitignore"),
		[]byte("vendor/\n*.go\n!a.go\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := relPaths(collect(t, root, Options{RespectGitignore: true}))
	for _, p := range got {
		if p == "vendor" || filepath.HasPrefix(p, "vendor/") {
			t.Errorf("gitignored vendor leaked: %s", p)
		}
	}
	if !contains(got, "a.go") {
		t.Errorf("negated !a.go should keep a.go visible, got %v", got)
	}
	if !contains(got, "README.md") {
		t.Errorf("non-matching paths should still appear: %v", got)
	}
}

func TestWalk_Exclude(t *testing.T) {
	root := makeTree(t)
	got := relPaths(collect(t, root, Options{Exclude: "vendor/**"}))
	for _, p := range got {
		if filepath.HasPrefix(p, "vendor/") {
			t.Errorf("exclude leak: %s", p)
		}
	}
}

func TestWalk_StopShortcircuits(t *testing.T) {
	root := makeTree(t)
	count := 0
	err := Walk(root, Options{}, func(e Entry) (Action, error) {
		count++
		if count >= 2 {
			return Stop, nil
		}
		return Continue, nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if count != 2 {
		t.Errorf("Stop should halt after 2 visits, got %d", count)
	}
}

func TestWalk_SkipDirSkipsSubtree(t *testing.T) {
	root := makeTree(t)
	visited := []string{}
	err := Walk(root, Options{}, func(e Entry) (Action, error) {
		visited = append(visited, e.RelPath)
		if e.RelPath == "src" {
			return SkipDir, nil
		}
		return Continue, nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	for _, p := range visited {
		if filepath.HasPrefix(p, "src/") {
			t.Errorf("SkipDir at src/ should prevent src/* visits, got %s", p)
		}
	}
}

func TestWalk_VisitorErrorAborts(t *testing.T) {
	root := makeTree(t)
	want := errors.New("boom")
	err := Walk(root, Options{}, func(e Entry) (Action, error) {
		return Continue, want
	})
	if !errors.Is(err, want) {
		t.Errorf("expected visitor error to surface, got %v", err)
	}
}

func TestWalk_BadGlob(t *testing.T) {
	err := Walk(t.TempDir(), Options{Glob: "[bad"}, func(Entry) (Action, error) { return Continue, nil })
	if err == nil {
		t.Fatal("expected error for bad glob")
	}
}

func TestWalk_PathFormMirrorsInput_Absolute(t *testing.T) {
	root := makeTree(t) // t.TempDir is absolute
	es := collect(t, root, Options{Glob: "a.go"})
	if len(es) == 0 {
		t.Fatal("expected at least one match")
	}
	if !filepath.IsAbs(es[0].Path) {
		t.Errorf("absolute root should yield absolute Entry.Path, got %q", es[0].Path)
	}
}

func TestWalk_TypeIsDerived(t *testing.T) {
	root := makeTree(t)
	got := collect(t, root, Options{IncludeHidden: false})
	hasFile, hasDir := false, false
	for _, e := range got {
		switch e.Type {
		case "file":
			hasFile = true
		case "dir":
			hasDir = true
		case "symlink":
			// fine
		default:
			t.Errorf("unexpected type %q for %s", e.Type, e.RelPath)
		}
	}
	if !hasFile || !hasDir {
		t.Errorf("expected both file and dir entries, got file=%v dir=%v", hasFile, hasDir)
	}
}
