package find

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stazelabs/ash/internal/jail"
	"github.com/stazelabs/ash/internal/proto"
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
	if res.TruncInfo == nil {
		t.Fatal("TruncInfo should be set when truncated")
	}
	if res.TruncInfo.Limit != 3 {
		t.Errorf("TruncInfo.Limit=%d want 3", res.TruncInfo.Limit)
	}
	// Below hard cap: Limit < Max means raising --limit is still possible.
	if res.TruncInfo.Limit >= res.TruncInfo.Max {
		t.Errorf("below cap: Limit=%d should be < Max=%d", res.TruncInfo.Limit, res.TruncInfo.Max)
	}
}

// TestRun_TruncationHintAtHardCap covers ASH-12: Limit==Max signals the hard
// cap; the reconstructed hint must not suggest raising --limit.
func TestRun_TruncationHintAtHardCap(t *testing.T) {
	ti := &proto.TruncInfo{Trunc: 1, Limit: MaxLimit, Max: MaxLimit}
	hint := findTruncHint(ti)
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
		"depth":         "3",
		"hidden":    "true",
		"gi": "false",
		"meta":         "true",
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
		{"depth", "abc"},
		{"hidden", "maybe"},
		{"gi", "maybe"},
		{"meta", "maybe"},
	} {
		_, perr := ParseArgs(map[string]any{"path": ".", bad.key: bad.val})
		if perr == nil {
			t.Errorf("expected error for %s=%q", bad.key, bad.val)
		}
	}
}

// ASH-71: with a jail policy that names the walk root as the project
// root, absolute input must yield bare repo-relative paths in each
// record (no leading "/", no leading "./").
func TestRun_DefaultStripsRepoRootPrefix(t *testing.T) {
	root := makeTree(t)
	jail.SetPolicy(jail.FromConfig(false, root, nil, nil))
	defer jail.SetPolicy(nil)

	res, perr := Run(&Args{Path: root, Glob: DefaultGlob, Type: "any", Limit: 100}, nil)
	if perr != nil {
		t.Fatalf("unexpected error: %+v", perr)
	}
	if len(res.Records) == 0 {
		t.Fatal("expected records, got none")
	}
	for _, rec := range res.Records {
		if filepath.IsAbs(rec.Path) {
			t.Errorf("record path should be repo-relative, got absolute: %q", rec.Path)
		}
		if strings.HasPrefix(rec.Path, "./") {
			t.Errorf("record path should be bare relative, got leading ./: %q", rec.Path)
		}
	}
	// Spot-check a known entry.
	if !hasRecord(res.Records, "src/main.go") {
		t.Errorf("expected to find src/main.go in records, got: %v", recordPaths(res.Records))
	}
}

// --absolute true must restore absolute paths even when a project root
// is known. This is the explicit opt-out for callers piping output into
// tools that need absolute references.
func TestRun_AbsoluteFlagPreservesAbsolutePaths(t *testing.T) {
	root := makeTree(t)
	jail.SetPolicy(jail.FromConfig(false, root, nil, nil))
	defer jail.SetPolicy(nil)

	res, perr := Run(&Args{Path: root, Glob: DefaultGlob, Type: "any", Limit: 100, Absolute: true}, nil)
	if perr != nil {
		t.Fatalf("unexpected error: %+v", perr)
	}
	if len(res.Records) == 0 {
		t.Fatal("expected records, got none")
	}
	for _, rec := range res.Records {
		if !filepath.IsAbs(rec.Path) {
			t.Errorf("with --absolute true, path should be absolute: %q", rec.Path)
		}
	}
}

// Records that sit outside the project root (e.g., a walk under
// jail.allow_paths) must be left absolute even in default mode —
// relative-with-".." is harder to read and not a token win.
func TestRun_DefaultLeavesOutsideRootAsAbsolute(t *testing.T) {
	root := t.TempDir()  // project root (no files in it)
	other := makeTree(t) // walk target, outside project root
	jail.SetPolicy(jail.FromConfig(false, root, []string{other}, nil))
	defer jail.SetPolicy(nil)

	res, perr := Run(&Args{Path: other, Glob: DefaultGlob, Type: "any", Limit: 100}, nil)
	if perr != nil {
		t.Fatalf("unexpected error: %+v", perr)
	}
	if len(res.Records) == 0 {
		t.Fatal("expected records, got none")
	}
	// Paths under `other` aren't under `root`, so default mode must keep
	// them absolute rather than producing "../<other>/..." gibberish.
	for _, rec := range res.Records {
		if !filepath.IsAbs(rec.Path) {
			t.Errorf("path outside project root should stay absolute, got: %q", rec.Path)
		}
		if strings.HasPrefix(rec.Path, "..") {
			t.Errorf("path should never start with ..: %q", rec.Path)
		}
	}
}

// No jail policy registered → no transformation. This is the test-side
// default and also matches the daemon behavior before SetPolicy runs.
func TestRun_NoPolicyLeavesPathsAlone(t *testing.T) {
	root := makeTree(t)
	jail.SetPolicy(nil)

	res, perr := Run(&Args{Path: root, Glob: DefaultGlob, Type: "any", Limit: 100}, nil)
	if perr != nil {
		t.Fatalf("unexpected error: %+v", perr)
	}
	for _, rec := range res.Records {
		if !filepath.IsAbs(rec.Path) {
			t.Errorf("without jail policy, absolute input should yield absolute paths: %q", rec.Path)
		}
	}
}

// --absolute parses through ParseArgs.
func TestParseArgs_AbsoluteFlag(t *testing.T) {
	a, perr := ParseArgs(map[string]any{"path": ".", "absolute": true})
	if perr != nil {
		t.Fatalf("unexpected error: %v", perr)
	}
	if !a.Absolute {
		t.Error("Absolute: want true")
	}
	a2, perr := ParseArgs(map[string]any{"path": "."})
	if perr != nil {
		t.Fatalf("unexpected error: %v", perr)
	}
	if a2.Absolute {
		t.Error("Absolute default: want false")
	}
}

// Fix 3: scope echo is no longer emitted in the pretty header. The agent
// already has its own request args; only Count + TRUNCATED are novel info.
func TestPrettyResponse_HeaderHasNoScope(t *testing.T) {
	rsp := &proto.Response{
		OK: true,
		Data: proto.MustData(&Result{
			Count: 3,
			Records: []Record{
				{Path: "docs/a.md", Type: "file"},
				{Path: "docs/b.md", Type: "file"},
				{Path: "docs/c.md", Type: "file"},
			},
		}),
	}
	req := &proto.Request{
		Verb: "find",
		Args: map[string]any{"path": "docs", "glob": "**/*.md", "type": "file"},
	}
	got := PrettyResponse(req, rsp)
	if !strings.HasPrefix(got, "=== ash find: 3 results ===\n") {
		t.Errorf("expected header without scope echo, got %q", got)
	}
	if strings.Contains(got, "[path=") || strings.Contains(got, "[glob=") || strings.Contains(got, "[type=") {
		t.Errorf("scope must be absent from header: %q", got)
	}
}

func TestPrettyResponse_HeaderShowsTruncated(t *testing.T) {
	rsp := &proto.Response{
		OK: true,
		Data: proto.MustData(&Result{
			Count:     2,
			Truncated: true,
			Records: []Record{
				{Path: "a.go", Type: "file"},
				{Path: "b.go", Type: "file"},
			},
		}),
	}
	req := &proto.Request{Verb: "find", Args: map[string]any{}}
	got := PrettyResponse(req, rsp)
	if !strings.Contains(got, "TRUNCATED") {
		t.Errorf("TRUNCATED marker must remain in header: %q", got)
	}
}

func hasRecord(records []Record, path string) bool {
	for _, r := range records {
		if r.Path == path {
			return true
		}
	}
	return false
}

func recordPaths(records []Record) []string {
	out := make([]string, len(records))
	for i, r := range records {
		out[i] = r.Path
	}
	return out
}

// TestPrettyResponse_AliasTableSingleAllowPath verifies that PrettyResponse
// prepends a @0 = <prefix> alias table when allow_paths is configured and
// emits paths under that root as @0/<tail>.
func TestPrettyResponse_AliasTableSingleAllowPath(t *testing.T) {
	root := t.TempDir()    // project root
	scratch := t.TempDir() // allow_paths entry outside project root
	// AllowedRoots stores the EvalSymlinks-resolved canonical form; resolve so
	// the header-string comparison is correct on macOS (/var -> /private/var).
	canonScratch := scratch
	if resolved, err := filepath.EvalSymlinks(scratch); err == nil {
		canonScratch = resolved
	}
	jail.SetPolicy(jail.FromConfig(false, root, []string{scratch}, nil))
	defer jail.SetPolicy(nil)

	rsp := &proto.Response{
		OK: true,
		Data: proto.MustData(&Result{
			Count: 2,
			Records: []Record{
				{Path: "internal/foo.go", Type: "file"}, // inside project root (bare)
				{Path: scratch + "/notes.md", Type: "file"}, // outside project root (absolute)
			},
		}),
	}
	req := &proto.Request{Verb: "find", Args: map[string]any{"path": scratch}}
	got := PrettyResponse(req, rsp)

	if !strings.Contains(got, "@0 = "+canonScratch) {
		t.Errorf("alias header missing: want @0 = %s in %q", canonScratch, got)
	}
	if !strings.Contains(got, "@0/notes.md") {
		t.Errorf("aliased path missing: want @0/notes.md in %q", got)
	}
	if !strings.Contains(got, "internal/foo.go") {
		t.Errorf("project-root path should be unchanged: want internal/foo.go in %q", got)
	}
}

// TestPrettyResponse_AliasTableTwoAllowPaths verifies the multi-prefix case:
// two allow_paths entries produce @0 and @1 aliases in the same response.
func TestPrettyResponse_AliasTableTwoAllowPaths(t *testing.T) {
	root    := t.TempDir()
	scratch := t.TempDir()
	vendor  := t.TempDir()
	canon := func(p string) string {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return r
		}
		return p
	}
	jail.SetPolicy(jail.FromConfig(false, root, []string{scratch, vendor}, nil))
	defer jail.SetPolicy(nil)

	rsp := &proto.Response{
		OK: true,
		Data: proto.MustData(&Result{
			Count: 2,
			Records: []Record{
				{Path: scratch + "/a.md", Type: "file"},
				{Path: vendor + "/pkg/b.go", Type: "file"},
			},
		}),
	}
	req := &proto.Request{Verb: "find", Args: map[string]any{"path": scratch}}
	got := PrettyResponse(req, rsp)

	if !strings.Contains(got, "@0 = "+canon(scratch)) {
		t.Errorf("@0 alias header missing in %q", got)
	}
	if !strings.Contains(got, "@1 = "+canon(vendor)) {
		t.Errorf("@1 alias header missing in %q", got)
	}
	if !strings.Contains(got, "@0/a.md") {
		t.Errorf("@0 path missing in %q", got)
	}
	if !strings.Contains(got, "@1/pkg/b.go") {
		t.Errorf("@1 path missing in %q", got)
	}
}

// TestPrettyResponse_NoAliasTableWithoutAllowPaths confirms no alias table is
// emitted when allow_paths is empty (the common host-repo case).
func TestPrettyResponse_NoAliasTableWithoutAllowPaths(t *testing.T) {
	root := t.TempDir()
	jail.SetPolicy(jail.FromConfig(false, root, nil, nil))
	defer jail.SetPolicy(nil)

	rsp := &proto.Response{
		OK: true,
		Data: proto.MustData(&Result{
			Count: 1,
			Records: []Record{{Path: "internal/foo.go", Type: "file"}},
		}),
	}
	req := &proto.Request{Verb: "find", Args: map[string]any{"path": "."}}
	got := PrettyResponse(req, rsp)

	if strings.Contains(got, "@0") {
		t.Errorf("no alias table expected when allow_paths is empty, got %q", got)
	}
}

// TestPrettyResponse_AbsoluteFlagSkipsAliasTable confirms --absolute suppresses
// the alias table even when allow_paths entries exist.
func TestPrettyResponse_AbsoluteFlagSkipsAliasTable(t *testing.T) {
	root    := t.TempDir()
	scratch := t.TempDir()
	jail.SetPolicy(jail.FromConfig(false, root, []string{scratch}, nil))
	defer jail.SetPolicy(nil)

	rsp := &proto.Response{
		OK: true,
		Data: proto.MustData(&Result{
			Count: 1,
			Records: []Record{{Path: scratch + "/notes.md", Type: "file"}},
		}),
	}
	req := &proto.Request{Verb: "find", Args: map[string]any{"path": scratch, "absolute": true}}
	got := PrettyResponse(req, rsp)

	if strings.Contains(got, "@0") {
		t.Errorf("no alias table expected with --absolute, got %q", got)
	}
}

// -- ASH-88: error messages strip project-root prefix ---------------------

func TestRun_NotFoundErrorStripsPrefix(t *testing.T) {
	root := t.TempDir()
	jail.SetPolicy(jail.FromConfig(false, root, nil, nil))
	defer jail.SetPolicy(nil)

	missing := filepath.Join(root, "no-such-dir")
	_, perr := Run(&Args{Path: missing, Glob: DefaultGlob, Type: "any", Limit: 100}, nil)
	if perr == nil || perr.Code != "not_found" {
		t.Fatalf("expected not_found, got %+v", perr)
	}
	if strings.Contains(perr.Msg, root) {
		t.Errorf("error Msg should not contain project root, got %q", perr.Msg)
	}
}

func TestRun_NotDirErrorStripsPrefix(t *testing.T) {
	root := t.TempDir()
	jail.SetPolicy(jail.FromConfig(false, root, nil, nil))
	defer jail.SetPolicy(nil)

	file := filepath.Join(root, "file.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, perr := Run(&Args{Path: file, Glob: DefaultGlob, Type: "any", Limit: 100}, nil)
	if perr == nil || perr.Code != "not_dir" {
		t.Fatalf("expected not_dir, got %+v", perr)
	}
	if strings.Contains(perr.Msg, root) {
		t.Errorf("error Msg should not contain project root, got %q", perr.Msg)
	}
}
