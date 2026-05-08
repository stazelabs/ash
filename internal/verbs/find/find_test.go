package find

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// makeTree builds a small fixture for tests:
//
//	root/
//	  a.go
//	  b.go
//	  README.md
//	  .gitignore           (leaf dotfile, should be findable)
//	  src/
//	    main.go
//	    util.go
//	    deep/
//	      x.go
//	  .git/                (hidden dir, skipped by default)
//	    HEAD
//	  vendor/
//	    pkg/
//	      v.go
func makeTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"a.go":              "package a",
		"b.go":              "package b",
		"README.md":         "# r",
		".gitignore":        "ignore",
		"src/main.go":       "package main",
		"src/util.go":       "package main",
		"src/deep/x.go":     "package deep",
		".git/HEAD":         "ref: refs/heads/main",
		"vendor/pkg/v.go":   "package pkg",
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

// pathsByType returns sorted paths from a Result, made relative to root and
// optionally filtered by type. The verb's path-form-mirrors-input rule
// produces absolute paths when given an absolute --path (which t.TempDir is),
// so tests strip the root prefix to keep assertions readable.
func pathsByType(t *testing.T, r *Result, root, typ string) []string {
	t.Helper()
	var out []string
	for _, rec := range r.Records {
		if typ != "" && rec.Type != typ {
			continue
		}
		rel, err := filepath.Rel(root, rec.Path)
		if err != nil {
			t.Fatalf("rel %q from %q: %v", rec.Path, root, err)
		}
		out = append(out, filepath.ToSlash(rel))
	}
	sort.Strings(out)
	return out
}

func TestRun_DefaultsFindAllNonHidden(t *testing.T) {
	root := makeTree(t)
	res, perr := Run(&Args{Path: root, Glob: DefaultGlob, Type: "any", Limit: 100}, nil)
	if perr != nil {
		t.Fatalf("unexpected error: %+v", perr)
	}
	got := pathsByType(t, res, root, "")
	// .git and its children are skipped (hidden dir). Everything else present.
	wantContains := []string{
		"a.go", "b.go", "README.md", ".gitignore",
		"src", "src/main.go", "src/util.go",
		"src/deep", "src/deep/x.go",
		"vendor", "vendor/pkg", "vendor/pkg/v.go",
	}
	wantAbsent := []string{".git", ".git/HEAD"}
	for _, w := range wantContains {
		if !contains(got, w) {
			t.Errorf("missing: %s\nresults: %v", w, got)
		}
	}
	for _, w := range wantAbsent {
		if contains(got, w) {
			t.Errorf("hidden dir leaked: %s\nresults: %v", w, got)
		}
	}
}

func TestRun_IncludeHiddenRecurses(t *testing.T) {
	root := makeTree(t)
	res, perr := Run(&Args{Path: root, Glob: DefaultGlob, Type: "any", Limit: 100, IncludeHidden: true}, nil)
	if perr != nil {
		t.Fatalf("unexpected error: %+v", perr)
	}
	got := pathsByType(t, res, root, "")
	if !contains(got, ".git/HEAD") {
		t.Errorf("expected .git/HEAD with include_hidden=true, got: %v", got)
	}
}

func TestRun_GlobFilters(t *testing.T) {
	root := makeTree(t)
	res, perr := Run(&Args{Path: root, Glob: "**/*.go", Type: "file", Limit: 100}, nil)
	if perr != nil {
		t.Fatalf("unexpected error: %+v", perr)
	}
	got := pathsByType(t, res, root, "file")
	want := []string{"a.go", "b.go", "src/deep/x.go", "src/main.go", "src/util.go", "vendor/pkg/v.go"}
	if !equalSlices(got, want) {
		t.Errorf("glob mismatch\n got: %v\nwant: %v", got, want)
	}
}

func TestRun_TypeFilter(t *testing.T) {
	root := makeTree(t)
	res, perr := Run(&Args{Path: root, Glob: DefaultGlob, Type: "dir", Limit: 100}, nil)
	if perr != nil {
		t.Fatalf("unexpected error: %+v", perr)
	}
	got := pathsByType(t, res, root, "")
	for _, p := range got {
		// Spot-check that no .go files leaked through the type filter.
		if strings.HasSuffix(p, ".go") {
			t.Errorf("file leaked into dir type filter: %s", p)
		}
	}
	if !contains(got, "src") || !contains(got, "src/deep") || !contains(got, "vendor") {
		t.Errorf("expected src, src/deep, vendor; got %v", got)
	}
}

func TestRun_MaxDepth(t *testing.T) {
	root := makeTree(t)
	res, perr := Run(&Args{Path: root, Glob: DefaultGlob, Type: "any", MaxDepth: 1, Limit: 100}, nil)
	if perr != nil {
		t.Fatalf("unexpected error: %+v", perr)
	}
	got := pathsByType(t, res, root, "")
	// Depth 1 = direct children of root only. src/main.go (depth 2) excluded.
	for _, p := range got {
		if strings.Contains(p, "/") {
			t.Errorf("deeper than 1 leaked through max_depth=1: %s", p)
		}
	}
	if !contains(got, "src") || !contains(got, "vendor") {
		t.Errorf("expected direct children src and vendor: %v", got)
	}
}

func TestRun_RespectsGitignoreByDefault(t *testing.T) {
	root := makeTree(t)
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("vendor/\n*.go\n!a.go\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Default args -> RespectGitignore=true. vendor/ disappears, *.go disappears
	// except for the negated a.go.
	res, perr := Run(&Args{Path: root, Glob: DefaultGlob, Type: "any", Limit: 100, RespectGitignore: true}, nil)
	if perr != nil {
		t.Fatalf("unexpected error: %+v", perr)
	}
	got := pathsByType(t, res, root, "")
	for _, p := range got {
		if strings.HasPrefix(p, "vendor/") || p == "vendor" {
			t.Errorf("gitignored path leaked: %s", p)
		}
		if strings.HasSuffix(p, ".go") && p != "a.go" {
			t.Errorf(".go file should have been gitignored (only a.go is negated): %s", p)
		}
	}
	if !contains(got, "a.go") {
		t.Errorf("negated pattern (!a.go) should keep a.go visible; got %v", got)
	}
	if !contains(got, "README.md") {
		t.Errorf("non-matching files should still be present; got %v", got)
	}
}

func TestRun_OptOutOfGitignore(t *testing.T) {
	root := makeTree(t)
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("vendor/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, perr := Run(&Args{Path: root, Glob: DefaultGlob, Type: "any", Limit: 100, RespectGitignore: false}, nil)
	if perr != nil {
		t.Fatalf("unexpected error: %+v", perr)
	}
	got := pathsByType(t, res, root, "")
	if !contains(got, "vendor") || !contains(got, "vendor/pkg/v.go") {
		t.Errorf("opt-out should walk vendor/, got %v", got)
	}
}

func TestRun_NoGitignoreFileIsNoop(t *testing.T) {
	root := makeTree(t)
	// no .gitignore written; default-on should still work fine.
	res, perr := Run(&Args{Path: root, Glob: DefaultGlob, Type: "any", Limit: 100, RespectGitignore: true}, nil)
	if perr != nil {
		t.Fatalf("unexpected error: %+v", perr)
	}
	got := pathsByType(t, res, root, "")
	if !contains(got, "vendor/pkg/v.go") {
		t.Errorf("with no .gitignore, vendor should still appear; got %v", got)
	}
}

func TestParseArgs_DefaultsRespectGitignoreTrue(t *testing.T) {
	a, perr := ParseArgs(map[string]any{"path": "."})
	if perr != nil {
		t.Fatalf("unexpected: %+v", perr)
	}
	if !a.RespectGitignore {
		t.Error("respect_gitignore should default to true")
	}
}

func TestRun_Exclude(t *testing.T) {
	root := makeTree(t)
	res, perr := Run(&Args{Path: root, Glob: "**/*.go", Type: "file", Exclude: "vendor/**", Limit: 100}, nil)
	if perr != nil {
		t.Fatalf("unexpected error: %+v", perr)
	}
	got := pathsByType(t, res, root, "file")
	for _, p := range got {
		if strings.HasPrefix(p, "vendor/") {
			t.Errorf("exclude pattern leaked: %s", p)
		}
	}
}

func TestRun_LimitTruncatesWithHint(t *testing.T) {
	root := makeTree(t)
	res, perr := Run(&Args{Path: root, Glob: DefaultGlob, Type: "any", Limit: 3}, nil)
	if perr != nil {
		t.Fatalf("unexpected error: %+v", perr)
	}
	if !res.Truncated {
		t.Errorf("expected truncated=true with limit=3")
	}
	if len(res.Records) != 3 {
		t.Errorf("expected 3 records, got %d", len(res.Records))
	}
	if !strings.Contains(res.TruncationHint, "limit of 3") {
		t.Errorf("hint missing limit info: %q", res.TruncationHint)
	}
	// User-set limit is below the hard cap, so the hint should still suggest
	// raising --limit (ASH-12: dead-end suggestion only at the cap).
	if !strings.Contains(res.TruncationHint, "raise --limit") {
		t.Errorf("hint should suggest raising --limit when below cap: %q", res.TruncationHint)
	}
}

// TestRun_TruncationHintAtHardCap covers ASH-12: when the user's --limit is
// at the hard cap (MaxLimit), suggesting "raise --limit" is a dead end.
// The hint should instead point at narrowing.
func TestRun_TruncationHintAtHardCap(t *testing.T) {
	hint := truncationHint(MaxLimit)
	if strings.Contains(hint, "raise --limit") {
		t.Errorf("hint at hard cap must not suggest raising --limit: %q", hint)
	}
	if !strings.Contains(hint, "hard cap") {
		t.Errorf("hint at hard cap should mention the cap: %q", hint)
	}
	if !strings.Contains(hint, "narrow") {
		t.Errorf("hint at hard cap should suggest narrowing: %q", hint)
	}
}

func TestRun_NotFound(t *testing.T) {
	_, perr := Run(&Args{Path: "/no/such/path/here", Glob: "**", Type: "any", Limit: 10}, nil)
	if perr == nil || perr.Code != "not_found" {
		t.Fatalf("expected not_found, got %+v", perr)
	}
}

func TestRun_NotADir(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, perr := Run(&Args{Path: file, Glob: "**", Type: "any", Limit: 10}, nil)
	if perr == nil || perr.Code != "not_dir" {
		t.Fatalf("expected not_dir, got %+v", perr)
	}
}

func TestParseArgs_RejectsBadGlob(t *testing.T) {
	_, perr := ParseArgs(map[string]any{"path": ".", "glob": "[invalid"})
	if perr == nil {
		t.Fatal("expected error for bad glob")
	}
}

func TestParseArgs_LimitClampedToMax(t *testing.T) {
	a, perr := ParseArgs(map[string]any{"path": ".", "limit": MaxLimit + 1000})
	if perr != nil {
		t.Fatalf("unexpected: %+v", perr)
	}
	if a.Limit != MaxLimit {
		t.Errorf("limit=%d not clamped to %d", a.Limit, MaxLimit)
	}
}

func contains(xs []string, target string) bool {
	for _, x := range xs {
		if x == target {
			return true
		}
	}
	return false
}

func equalSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestParseArgs_WireShape verifies that every numeric and bool arg accepts
// string-typed values (the wire shape from CLI parseFlags) and rejects
// garbage. Guards against a future verb skipping argutil and silently
// breaking the string-coercion path.
func TestParseArgs_WireShape(t *testing.T) {
	a, perr := ParseArgs(map[string]any{
		"path":              ".",
		"limit":             "10",
		"max_depth":         "3",
		"include_hidden":    "true",
		"respect_gitignore": "false",
		"with_meta":         "true",
	})
	if perr != nil {
		t.Fatalf("valid string args rejected: %v", perr)
	}
	if a.Limit != 10 {
		t.Errorf("limit: got %d, want 10", a.Limit)
	}
	if a.MaxDepth != 3 {
		t.Errorf("max_depth: got %d, want 3", a.MaxDepth)
	}
	if !a.IncludeHidden {
		t.Error("include_hidden: want true")
	}
	if a.RespectGitignore {
		t.Error("respect_gitignore: want false")
	}
	if !a.WithMeta {
		t.Error("with_meta: want true")
	}

	for _, bad := range []struct{ key, val string }{
		{"limit", "abc"},
		{"max_depth", "abc"},
		{"include_hidden", "maybe"},
		{"respect_gitignore", "maybe"},
		{"with_meta", "maybe"},
	} {
		_, perr := ParseArgs(map[string]any{"path": ".", bad.key: bad.val})
		if perr == nil {
			t.Errorf("expected error for %s=%q", bad.key, bad.val)
		}
	}
}
