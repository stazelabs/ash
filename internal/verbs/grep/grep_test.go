package grep

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stazelabs/ash/internal/jail"
	"github.com/stazelabs/ash/internal/proto"
)

// makeTree builds a small fixture for tests. Files contain known patterns
// so assertions can target specific lines/cols.
//
//	root/
//	  a.go         "package main\nfunc Foo() {}\nfunc bar() {}\n"
//	  b.go         "package b\n// FOO marker\nFoo Foo\n"
//	  README.md    "# README\nfoo bar baz\nFOO bar\n"
//	  .gitignore   "vendor/\n*.log\n"
//	  app.log      "ignored Foo line"
//	  src/
//	    main.go    "package main\nfunc Main() {\n  Foo()\n}\n"
//	    util.go    "package util\nfunc helper() string { return \"foo\" }\n"
//	    deep/
//	      x.go     "package deep\n// no match here\n"
//	  vendor/
//	    pkg/
//	      v.go     "package pkg\nFoo in vendor land\n"
//	  .git/
//	    HEAD       "ref: refs/heads/main\n"   (hidden dir, skipped)
//	  big.txt      (16 MiB+1 of 'a' bytes; should be skipped as oversized)
//	  binary.dat   ("hello\x00Foo world")     (binary, should be skipped)
func makeTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"a.go":            "package main\nfunc Foo() {}\nfunc bar() {}\n",
		"b.go":            "package b\n// FOO marker\nFoo Foo\n",
		"README.md":       "# README\nfoo bar baz\nFOO bar\n",
		".gitignore":      "vendor/\n*.log\n",
		"app.log":         "ignored Foo line\n",
		"src/main.go":     "package main\nfunc Main() {\n  Foo()\n}\n",
		"src/util.go":     "package util\nfunc helper() string { return \"foo\" }\n",
		"src/deep/x.go":   "package deep\n// no match here\n",
		"vendor/pkg/v.go": "package pkg\nFoo in vendor land\n",
		".git/HEAD":       "ref: refs/heads/main\n",
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
	// Binary file: NUL byte in the leading probe window.
	if err := os.WriteFile(filepath.Join(root, "binary.dat"), []byte("hello\x00Foo world\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// makeBigFile writes a >16 MiB file at root/path.
func makeBigFile(t *testing.T, root, name string) {
	t.Helper()
	full := filepath.Join(root, name)
	f, err := os.Create(full)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	chunk := bytes.Repeat([]byte("a"), 1<<20) // 1 MiB
	for i := 0; i < 17; i++ {
		if _, err := f.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
}

// matchKey makes match records easy to assert on by reducing them to a
// stable, root-relative form like "src/main.go:3:Foo()" (kind=match) or
// "src/main.go:2-... " (kind=before/after).
func matchKey(t *testing.T, root string, m Match) string {
	t.Helper()
	rel, err := filepath.Rel(root, m.Path)
	if err != nil {
		rel = m.Path
	}
	rel = filepath.ToSlash(rel)
	sep := ":"
	if m.Kind == "before" || m.Kind == "after" {
		sep = "-"
	}
	return fmt.Sprintf("%s%s%d", rel, sep, m.Line)
}

func keysOf(t *testing.T, root string, ms []Match) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, matchKey(t, root, m))
	}
	sort.Strings(out)
	return out
}

func TestRun_BasicLiteralCaseSensitive(t *testing.T) {
	root := makeTree(t)
	res, perr := Run(&Args{
		Pattern:          "Foo",
		Path:             root,
		Glob:             DefaultGlob,
		Case:             "sensitive",
		MaxMatches:       DefaultMaxMatches,
		RespectGitignore: true,
	}, nil)
	if perr != nil {
		t.Fatalf("unexpected error: %+v", perr)
	}
	got := keysOf(t, root, res.Matches)
	// No `foo`/`FOO` matches under sensitive search. vendor/ is gitignored,
	// app.log is gitignored, .git/ is hidden, binary.dat is skipped.
	want := []string{
		"a.go:2",
		"b.go:3",     // "Foo Foo" — only one record per matching line
		"src/main.go:3",
	}
	if !equal(got, want) {
		t.Errorf("matches mismatch\n got: %v\nwant: %v", got, want)
	}
	if res.MatchCount != 3 {
		t.Errorf("MatchCount=%d, want 3", res.MatchCount)
	}
	if res.FileCount != 3 {
		t.Errorf("FileCount=%d, want 3", res.FileCount)
	}
	if res.FilesSkippedBinary != 1 {
		t.Errorf("FilesSkippedBinary=%d, want 1", res.FilesSkippedBinary)
	}
}

func TestRun_SmartCaseLowercaseGoesInsensitive(t *testing.T) {
	root := makeTree(t)
	res, perr := Run(&Args{
		Pattern:          "foo",
		Path:             root,
		Glob:             DefaultGlob,
		Case:             "smart",
		MaxMatches:       DefaultMaxMatches,
		RespectGitignore: true,
	}, nil)
	if perr != nil {
		t.Fatalf("unexpected error: %+v", perr)
	}
	got := keysOf(t, root, res.Matches)
	// Lowercase pattern under smart-case = insensitive: catches FOO/Foo/foo.
	want := []string{
		"README.md:2", // "foo bar baz"
		"README.md:3", // "FOO bar"
		"a.go:2",
		"b.go:2",      // "// FOO marker"
		"b.go:3",      // "Foo Foo"
		"src/main.go:3",
		"src/util.go:2",
	}
	if !equal(got, want) {
		t.Errorf("smart-case mismatch\n got: %v\nwant: %v", got, want)
	}
}

func TestRun_SmartCaseUppercaseGoesSensitive(t *testing.T) {
	root := makeTree(t)
	res, perr := Run(&Args{
		Pattern:          "Foo",
		Path:             root,
		Glob:             DefaultGlob,
		Case:             "smart",
		MaxMatches:       DefaultMaxMatches,
		RespectGitignore: true,
	}, nil)
	if perr != nil {
		t.Fatalf("unexpected error: %+v", perr)
	}
	got := keysOf(t, root, res.Matches)
	want := []string{
		"a.go:2",
		"b.go:3",
		"src/main.go:3",
	}
	if !equal(got, want) {
		t.Errorf("smart-case sensitive mismatch\n got: %v\nwant: %v", got, want)
	}
}

func TestRun_FixedStringEscapesRegexMeta(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f.txt"),
		[]byte("a.b\nliteral.dot\nx.y\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// As a regex "a.b" matches "a<any>b" -> all three lines. As fixed it should
	// only match "a.b".
	res, perr := Run(&Args{
		Pattern: "a.b", Path: dir, Glob: DefaultGlob, Case: "sensitive",
		MaxMatches: DefaultMaxMatches, FixedString: true,
	}, nil)
	if perr != nil {
		t.Fatalf("unexpected error: %+v", perr)
	}
	if res.MatchCount != 1 || len(res.Matches) != 1 || res.Matches[0].Line != 1 {
		t.Errorf("fixed_string=true should match only line 1, got %+v", res.Matches)
	}
}

func TestRun_WordBoundary(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f.txt"),
		[]byte("Foo\nFooBar\n_Foo_\nbar Foo baz\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, perr := Run(&Args{
		Pattern: "Foo", Path: dir, Glob: DefaultGlob, Case: "sensitive",
		MaxMatches: DefaultMaxMatches, Word: true,
	}, nil)
	if perr != nil {
		t.Fatalf("unexpected error: %+v", perr)
	}
	// Only line 1 ("Foo") and line 4 ("bar Foo baz") satisfy \bFoo\b.
	// "FooBar" fails the trailing \b. "_Foo_" — Go RE2 treats _ as a word
	// character, so \b doesn't fire there either.
	want := []int{1, 4}
	got := []int{}
	for _, m := range res.Matches {
		got = append(got, m.Line)
	}
	if !equalInt(got, want) {
		t.Errorf("word-boundary mismatch: got %v, want %v", got, want)
	}
}

func TestRun_FilesOnly(t *testing.T) {
	root := makeTree(t)
	res, perr := Run(&Args{
		Pattern: "Foo", Path: root, Glob: DefaultGlob, Case: "insensitive",
		MaxMatches: DefaultMaxMatches, RespectGitignore: true, FilesOnly: true,
	}, nil)
	if perr != nil {
		t.Fatalf("unexpected error: %+v", perr)
	}
	if len(res.Matches) != 0 {
		t.Errorf("files_only should not return match records, got %d", len(res.Matches))
	}
	got := relSlice(t, root, res.Files)
	want := []string{
		"README.md", "a.go", "b.go", "src/main.go", "src/util.go",
	}
	if !equal(got, want) {
		t.Errorf("files_only mismatch\n got: %v\nwant: %v", got, want)
	}
	if res.FileCount != len(want) {
		t.Errorf("FileCount=%d, want %d", res.FileCount, len(want))
	}
}

func TestRun_GlobFilter(t *testing.T) {
	root := makeTree(t)
	res, perr := Run(&Args{
		Pattern: "Foo", Path: root, Glob: "**/*.go", Case: "insensitive",
		MaxMatches: DefaultMaxMatches, RespectGitignore: true,
	}, nil)
	if perr != nil {
		t.Fatalf("unexpected error: %+v", perr)
	}
	got := keysOf(t, root, res.Matches)
	// README.md filtered out by glob.
	want := []string{
		"a.go:2",
		"b.go:2",
		"b.go:3",
		"src/main.go:3",
		"src/util.go:2",
	}
	if !equal(got, want) {
		t.Errorf("glob mismatch\n got: %v\nwant: %v", got, want)
	}
}

func TestRun_RespectsGitignoreByDefault(t *testing.T) {
	root := makeTree(t)
	// Default-on: vendor/ and *.log are gitignored. Run without explicit override.
	res, perr := Run(&Args{
		Pattern: "Foo", Path: root, Glob: DefaultGlob, Case: "smart",
		MaxMatches: DefaultMaxMatches, RespectGitignore: true,
	}, nil)
	if perr != nil {
		t.Fatalf("unexpected error: %+v", perr)
	}
	for _, m := range res.Matches {
		rel, _ := filepath.Rel(root, m.Path)
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, "vendor/") {
			t.Errorf("vendor/ leaked through default gitignore: %s", rel)
		}
		if strings.HasSuffix(rel, ".log") {
			t.Errorf("*.log leaked through default gitignore: %s", rel)
		}
	}
}

func TestRun_OptOutOfGitignore(t *testing.T) {
	root := makeTree(t)
	res, perr := Run(&Args{
		Pattern: "Foo", Path: root, Glob: DefaultGlob, Case: "smart",
		MaxMatches: DefaultMaxMatches, RespectGitignore: false,
	}, nil)
	if perr != nil {
		t.Fatalf("unexpected error: %+v", perr)
	}
	rels := []string{}
	for _, m := range res.Matches {
		rel, _ := filepath.Rel(root, m.Path)
		rels = append(rels, filepath.ToSlash(rel))
	}
	hasVendor, hasLog := false, false
	for _, r := range rels {
		if strings.HasPrefix(r, "vendor/") {
			hasVendor = true
		}
		if strings.HasSuffix(r, ".log") {
			hasLog = true
		}
	}
	if !hasVendor || !hasLog {
		t.Errorf("opt-out should reach vendor/ and *.log; got %v", rels)
	}
}

func TestRun_HiddenDirSkippedByDefault(t *testing.T) {
	root := makeTree(t)
	res, perr := Run(&Args{
		Pattern: ".", Path: root, Glob: DefaultGlob, Case: "sensitive",
		MaxMatches: DefaultMaxMatches, RespectGitignore: true,
	}, nil)
	if perr != nil {
		t.Fatalf("unexpected error: %+v", perr)
	}
	for _, m := range res.Matches {
		rel, _ := filepath.Rel(root, m.Path)
		if strings.HasPrefix(filepath.ToSlash(rel), ".git/") {
			t.Errorf("hidden dir leaked: %s", rel)
		}
	}
}

func TestRun_BinarySkipped(t *testing.T) {
	root := makeTree(t)
	res, perr := Run(&Args{
		Pattern: "Foo", Path: root, Glob: DefaultGlob, Case: "sensitive",
		MaxMatches: DefaultMaxMatches, RespectGitignore: true,
	}, nil)
	if perr != nil {
		t.Fatalf("unexpected error: %+v", perr)
	}
	if res.FilesSkippedBinary != 1 {
		t.Errorf("expected 1 binary file skipped, got %d", res.FilesSkippedBinary)
	}
	for _, m := range res.Matches {
		if strings.HasSuffix(m.Path, "binary.dat") {
			t.Errorf("binary file leaked into matches: %+v", m)
		}
	}
}

func TestRun_LargeFileSkipped(t *testing.T) {
	root := t.TempDir()
	makeBigFile(t, root, "big.txt")
	if err := os.WriteFile(filepath.Join(root, "small.txt"), []byte("Foo bar\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, perr := Run(&Args{
		Pattern: "Foo", Path: root, Glob: DefaultGlob, Case: "sensitive",
		MaxMatches: DefaultMaxMatches, RespectGitignore: true,
	}, nil)
	if perr != nil {
		t.Fatalf("unexpected error: %+v", perr)
	}
	if res.FilesSkippedLarge != 1 {
		t.Errorf("expected 1 large file skipped, got %d", res.FilesSkippedLarge)
	}
	if res.MatchCount != 1 {
		t.Errorf("expected 1 match (small.txt), got %d", res.MatchCount)
	}
}

func TestRun_MaxPerFileCaps(t *testing.T) {
	dir := t.TempDir()
	body := "Foo\nFoo\nFoo\nFoo\nFoo\n"
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	res, perr := Run(&Args{
		Pattern: "Foo", Path: dir, Glob: DefaultGlob, Case: "sensitive",
		MaxMatches: DefaultMaxMatches, MaxPerFile: 2,
	}, nil)
	if perr != nil {
		t.Fatalf("unexpected error: %+v", perr)
	}
	if res.MatchCount != 2 {
		t.Errorf("expected MaxPerFile to cap at 2, got %d", res.MatchCount)
	}
	if res.Truncated {
		t.Errorf("MaxPerFile is not a global truncation; Truncated should be false")
	}
}

func TestRun_MaxMatchesGlobalTruncates(t *testing.T) {
	dir := t.TempDir()
	body := "Foo\nFoo\nFoo\nFoo\nFoo\n"
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	res, perr := Run(&Args{
		Pattern: "Foo", Path: dir, Glob: DefaultGlob, Case: "sensitive",
		MaxMatches: 3,
	}, nil)
	if perr != nil {
		t.Fatalf("unexpected error: %+v", perr)
	}
	if !res.Truncated {
		t.Errorf("expected Truncated=true with MaxMatches=3")
	}
	if res.MatchCount != 3 {
		t.Errorf("expected 3 matches, got %d", res.MatchCount)
	}
	if res.TruncInfo == nil {
		t.Fatal("TruncInfo should be set when truncated")
	}
	if res.TruncInfo.Limit != 3 {
		t.Errorf("TruncInfo.Limit=%d want 3", res.TruncInfo.Limit)
	}
	// Below hard cap: Limit < Max means raising --max_matches is still possible.
	if res.TruncInfo.Limit >= res.TruncInfo.Max {
		t.Errorf("below cap: Limit=%d should be < Max=%d", res.TruncInfo.Limit, res.TruncInfo.Max)
	}
}

// TestRun_TruncationHintAtHardCap covers ASH-12: Limit==Max signals the hard
// cap; the reconstructed hint must not suggest raising --max_matches.
func TestRun_TruncationHintAtHardCap(t *testing.T) {
	t.Run("matches mode", func(t *testing.T) {
		ti := &proto.TruncInfo{Trunc: 1, Limit: MaxMaxMatches, Max: MaxMaxMatches}
		hint := grepTruncHint(ti, false)
		if strings.Contains(hint, "raise --max_matches") {
			t.Errorf("hint at cap must not suggest raising: %q", hint)
		}
		if !strings.Contains(hint, "hard cap") || !strings.Contains(hint, "narrow") {
			t.Errorf("hint at cap should mention the cap and suggest narrowing: %q", hint)
		}
	})
	t.Run("files_only mode", func(t *testing.T) {
		ti := &proto.TruncInfo{Trunc: 1, Limit: MaxMaxMatches, Max: MaxMaxMatches}
		hint := grepTruncHint(ti, true)
		if strings.Contains(hint, "raise --max_matches") {
			t.Errorf("files_only hint at cap must not suggest raising: %q", hint)
		}
		if !strings.Contains(hint, "files") {
			t.Errorf("files_only hint should mention files: %q", hint)
		}
	})
}

func TestRun_ContextBeforeAfter(t *testing.T) {
	dir := t.TempDir()
	body := "L1\nL2\nMatch\nL4\nL5\nL6\nMatch\nL8\nL9\n"
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	res, perr := Run(&Args{
		Pattern: "Match", Path: dir, Glob: DefaultGlob, Case: "sensitive",
		MaxMatches: DefaultMaxMatches, ContextBefore: 1, ContextAfter: 1,
	}, nil)
	if perr != nil {
		t.Fatalf("unexpected error: %+v", perr)
	}
	// Expect: L2(before), Match(line 3), L4(after), L6(before), Match(line 7), L8(after).
	if len(res.Matches) != 6 {
		t.Fatalf("expected 6 records, got %d: %+v", len(res.Matches), res.Matches)
	}
	wantLines := []int{2, 3, 4, 6, 7, 8}
	wantKinds := []string{"before", "", "after", "before", "", "after"}
	for i, rec := range res.Matches {
		if rec.Line != wantLines[i] || rec.Kind != wantKinds[i] {
			t.Errorf("record %d: got line=%d kind=%q, want line=%d kind=%q",
				i, rec.Line, rec.Kind, wantLines[i], wantKinds[i])
		}
	}
}

func TestRun_ContextOverlapDeduplicates(t *testing.T) {
	dir := t.TempDir()
	// Two matches one line apart; before-context for the second should not
	// re-emit a line we already emitted as after-context for the first.
	body := "L1\nMatch\nMatch\nL4\n"
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	res, perr := Run(&Args{
		Pattern: "Match", Path: dir, Glob: DefaultGlob, Case: "sensitive",
		MaxMatches: DefaultMaxMatches, ContextBefore: 2, ContextAfter: 2,
	}, nil)
	if perr != nil {
		t.Fatalf("unexpected error: %+v", perr)
	}
	// Records in order: L1(before of m1), Match(2), Match(3), L4(after).
	wantLines := []int{1, 2, 3, 4}
	if len(res.Matches) != len(wantLines) {
		t.Fatalf("record count mismatch: %+v", res.Matches)
	}
	for i, rec := range res.Matches {
		if rec.Line != wantLines[i] {
			t.Errorf("record %d: line=%d, want %d", i, rec.Line, wantLines[i])
		}
	}
}

func TestRun_SingleFilePathSearchesJustThat(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f.go"),
		[]byte("Foo\nbar\nFoo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "g.go"),
		[]byte("Foo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, perr := Run(&Args{
		Pattern: "Foo", Path: filepath.Join(dir, "f.go"),
		Glob: DefaultGlob, Case: "sensitive", MaxMatches: DefaultMaxMatches,
	}, nil)
	if perr != nil {
		t.Fatalf("unexpected error: %+v", perr)
	}
	if res.FileCount != 1 || res.MatchCount != 2 {
		t.Errorf("single-file search expected 2 matches in 1 file, got file_count=%d match_count=%d",
			res.FileCount, res.MatchCount)
	}
}

func TestRun_NotFound(t *testing.T) {
	_, perr := Run(&Args{
		Pattern: "x", Path: "/no/such/path/here",
		Glob: DefaultGlob, Case: "smart", MaxMatches: 10,
	}, nil)
	if perr == nil || perr.Code != "not_found" {
		t.Fatalf("expected not_found, got %+v", perr)
	}
}

func TestRun_ColIs1IndexedByteColumn(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f.txt"),
		[]byte("xxxFoo bar\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, perr := Run(&Args{
		Pattern: "Foo", Path: dir, Glob: DefaultGlob, Case: "sensitive",
		MaxMatches: DefaultMaxMatches,
	}, nil)
	if perr != nil {
		t.Fatalf("unexpected error: %+v", perr)
	}
	if len(res.Matches) != 1 {
		t.Fatalf("expected 1 match, got %d", len(res.Matches))
	}
	if res.Matches[0].Col != 4 {
		t.Errorf("col=%d, want 4 (1-indexed)", res.Matches[0].Col)
	}
}

func TestParseArgs_DefaultsAndValidation(t *testing.T) {
	a, perr := ParseArgs(map[string]any{"pattern": "x", "path": "."})
	if perr != nil {
		t.Fatalf("unexpected: %+v", perr)
	}
	if a.Glob != DefaultGlob {
		t.Errorf("glob default: %q, want %q", a.Glob, DefaultGlob)
	}
	if a.Case != "smart" {
		t.Errorf("case default: %q, want smart", a.Case)
	}
	if !a.RespectGitignore {
		t.Errorf("respect_gitignore default should be true")
	}
	if a.MaxMatches != DefaultMaxMatches {
		t.Errorf("max_matches default: %d, want %d", a.MaxMatches, DefaultMaxMatches)
	}
}

func TestParseArgs_RejectsMissingPattern(t *testing.T) {
	_, perr := ParseArgs(map[string]any{"path": "."})
	if perr == nil || perr.Code != "args" {
		t.Fatalf("expected args error for missing pattern, got %+v", perr)
	}
}

func TestParseArgs_RejectsMissingPath(t *testing.T) {
	_, perr := ParseArgs(map[string]any{"pattern": "x"})
	if perr == nil || perr.Code != "args" {
		t.Fatalf("expected args error for missing path, got %+v", perr)
	}
}

func TestParseArgs_RejectsBadCase(t *testing.T) {
	_, perr := ParseArgs(map[string]any{"pattern": "x", "path": ".", "case": "weird"})
	if perr == nil {
		t.Fatal("expected error for invalid case")
	}
}

func TestParseArgs_RejectsBadRegex(t *testing.T) {
	a, _ := ParseArgs(map[string]any{"pattern": "[invalid", "path": "."})
	// ParseArgs doesn't compile; Run does. So Run should error.
	_, perr := Run(a, nil)
	if perr == nil || perr.Code != "args" {
		t.Fatalf("expected args error from invalid regex, got %+v", perr)
	}
}

func TestParseArgs_LimitClampedToMax(t *testing.T) {
	a, perr := ParseArgs(map[string]any{
		"pattern": "x", "path": ".", "max": MaxMaxMatches + 1000,
	})
	if perr != nil {
		t.Fatalf("unexpected: %+v", perr)
	}
	if a.MaxMatches != MaxMaxMatches {
		t.Errorf("max_matches not clamped: %d", a.MaxMatches)
	}
}

func TestParseArgs_ContextClampedToMax(t *testing.T) {
	a, perr := ParseArgs(map[string]any{
		"pattern": "x", "path": ".",
		"cb": 999, "ca": 999,
	})
	if perr != nil {
		t.Fatalf("unexpected: %+v", perr)
	}
	if a.ContextBefore != MaxContextLines || a.ContextAfter != MaxContextLines {
		t.Errorf("context not clamped: before=%d after=%d", a.ContextBefore, a.ContextAfter)
	}
}

// -- helpers ---------------------------------------------------------------

func relSlice(t *testing.T, root string, ps []string) []string {
	t.Helper()
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		rel, err := filepath.Rel(root, p)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, filepath.ToSlash(rel))
	}
	sort.Strings(out)
	return out
}

func TestNoText(t *testing.T) {
	root := makeTree(t)
	a := &Args{
		Pattern: "Foo", Path: root, Glob: "**/*.go",
		Case: "sensitive", MaxMatches: DefaultMaxMatches,
		RespectGitignore: true, NoText: true,
	}
	res, perr := Run(a, nil)
	if perr != nil {
		t.Fatalf("Run: %+v", perr)
	}
	if res.MatchCount == 0 {
		t.Fatal("expected matches")
	}
	for _, m := range res.Matches {
		if m.Kind == "" && m.Text != "" {
			t.Errorf("match record has text=%q with no_text=true", m.Text)
		}
		if m.Kind != "" && m.Text != "" {
			t.Errorf("context record has text=%q with no_text=true", m.Text)
		}
		if m.Kind == "" && m.Col == 0 {
			t.Errorf("match record missing col with no_text=true (line %d)", m.Line)
		}
	}
}

func equal(a, b []string) bool {
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

func equalInt(a, b []int) bool {
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
	root := makeTree(t)
	a, perr := ParseArgs(map[string]any{
		"pattern":           "foo",
		"path":              root,
		"max":       "100",
		"mpf":      "5",
		"cb":    "2",
		"ca":     "3",
		"depth":         "4",
		"lit":      "true",
		"word":              "false",
		"fo":        "true",
		"no-text":           "false",
		"hidden":    "true",
		"gi": "false",
	})
	if perr != nil {
		t.Fatalf("valid string args rejected: %v", perr)
	}
	if a.MaxMatches != 100 {
		t.Errorf("max_matches: got %d, want 100", a.MaxMatches)
	}
	if a.MaxPerFile != 5 {
		t.Errorf("max_per_file: got %d, want 5", a.MaxPerFile)
	}
	if a.ContextBefore != 2 {
		t.Errorf("context_before: got %d, want 2", a.ContextBefore)
	}
	if a.ContextAfter != 3 {
		t.Errorf("context_after: got %d, want 3", a.ContextAfter)
	}
	if a.MaxDepth != 4 {
		t.Errorf("max_depth: got %d, want 4", a.MaxDepth)
	}
	if !a.FixedString {
		t.Error("fixed_string: want true")
	}
	if a.Word {
		t.Error("word: want false")
	}
	if !a.FilesOnly {
		t.Error("files_only: want true")
	}
	if a.NoText {
		t.Error("no_text: want false")
	}
	if !a.IncludeHidden {
		t.Error("include_hidden: want true")
	}
	if a.RespectGitignore {
		t.Error("respect_gitignore: want false")
	}

	for _, bad := range []struct{ key, val string }{
		{"max", "abc"},
		{"mpf", "abc"},
		{"cb", "abc"},
		{"ca", "abc"},
		{"depth", "abc"},
		{"lit", "maybe"},
		{"word", "maybe"},
		{"fo", "maybe"},
		{"no-text", "maybe"},
		{"hidden", "maybe"},
		{"gi", "maybe"},
	} {
		_, perr := ParseArgs(map[string]any{
			"pattern": "foo",
			"path":    root,
			bad.key:   bad.val,
		})
		if perr == nil {
			t.Errorf("expected error for %s=%q", bad.key, bad.val)
		}
	}
}

// ASH-71: matches default to repo-root-relative paths once a jail policy
// names the walk root. Without the policy, behavior is unchanged.
func TestRun_DefaultStripsRepoRootPrefixInMatches(t *testing.T) {
	root := makeTree(t)
	jail.SetPolicy(jail.FromConfig(false, root, nil, nil))
	defer jail.SetPolicy(nil)

	res, perr := Run(&Args{
		Pattern:          "Foo",
		Path:             root,
		Glob:             DefaultGlob,
		Case:             "sensitive",
		MaxMatches:       DefaultMaxMatches,
		RespectGitignore: true,
	}, nil)
	if perr != nil {
		t.Fatalf("unexpected error: %+v", perr)
	}
	if len(res.Matches) == 0 {
		t.Fatal("expected matches, got none")
	}
	for _, m := range res.Matches {
		if filepath.IsAbs(m.Path) {
			t.Errorf("match path should be repo-relative, got absolute: %q", m.Path)
		}
		if strings.HasPrefix(m.Path, "./") {
			t.Errorf("match path should be bare relative, got leading ./: %q", m.Path)
		}
	}
	// Sanity: known matching files appear with bare paths.
	wantPaths := map[string]bool{"a.go": false, "b.go": false, "src/main.go": false}
	for _, m := range res.Matches {
		if _, ok := wantPaths[m.Path]; ok {
			wantPaths[m.Path] = true
		}
	}
	for p, seen := range wantPaths {
		if !seen {
			t.Errorf("expected match in %s; got matches: %v", p, res.Matches)
		}
	}
}

// --absolute true preserves the legacy input-mirroring form. files_only
// is also covered so the Files slice gets the same treatment.
func TestRun_AbsoluteFlagPreservesAbsolutePaths(t *testing.T) {
	root := makeTree(t)
	jail.SetPolicy(jail.FromConfig(false, root, nil, nil))
	defer jail.SetPolicy(nil)

	res, perr := Run(&Args{
		Pattern:          "Foo",
		Path:             root,
		Glob:             DefaultGlob,
		Case:             "sensitive",
		MaxMatches:       DefaultMaxMatches,
		RespectGitignore: true,
		FilesOnly:        true,
		Absolute:         true,
	}, nil)
	if perr != nil {
		t.Fatalf("unexpected error: %+v", perr)
	}
	if len(res.Files) == 0 {
		t.Fatal("expected files, got none")
	}
	for _, f := range res.Files {
		if !filepath.IsAbs(f) {
			t.Errorf("with --absolute true, file path should be absolute: %q", f)
		}
	}
}

// files_only output gets the same default repo-relative treatment as the
// match records. This is the test for ASH-71's wider-coverage claim.
func TestRun_DefaultStripsRepoRootPrefixInFilesOnly(t *testing.T) {
	root := makeTree(t)
	jail.SetPolicy(jail.FromConfig(false, root, nil, nil))
	defer jail.SetPolicy(nil)

	res, perr := Run(&Args{
		Pattern:          "Foo",
		Path:             root,
		Glob:             DefaultGlob,
		Case:             "sensitive",
		MaxMatches:       DefaultMaxMatches,
		RespectGitignore: true,
		FilesOnly:        true,
	}, nil)
	if perr != nil {
		t.Fatalf("unexpected error: %+v", perr)
	}
	if len(res.Files) == 0 {
		t.Fatal("expected files, got none")
	}
	for _, f := range res.Files {
		if filepath.IsAbs(f) {
			t.Errorf("files_only path should be repo-relative, got absolute: %q", f)
		}
	}
}

// No jail policy → paths stay in their input-mirroring form (current
// daemon behavior before SetPolicy runs).
func TestRun_NoPolicyLeavesMatchesAlone(t *testing.T) {
	root := makeTree(t)
	jail.SetPolicy(nil)

	res, perr := Run(&Args{
		Pattern:          "Foo",
		Path:             root,
		Glob:             DefaultGlob,
		Case:             "sensitive",
		MaxMatches:       DefaultMaxMatches,
		RespectGitignore: true,
	}, nil)
	if perr != nil {
		t.Fatalf("unexpected error: %+v", perr)
	}
	for _, m := range res.Matches {
		if !filepath.IsAbs(m.Path) {
			t.Errorf("without jail policy, absolute input should yield absolute paths: %q", m.Path)
		}
	}
}

// --absolute parses through ParseArgs.
func TestParseArgs_AbsoluteFlag(t *testing.T) {
	a, perr := ParseArgs(map[string]any{"pattern": "x", "path": ".", "absolute": true})
	if perr != nil {
		t.Fatalf("unexpected error: %v", perr)
	}
	if !a.Absolute {
		t.Error("Absolute: want true")
	}
	a2, perr := ParseArgs(map[string]any{"pattern": "x", "path": "."})
	if perr != nil {
		t.Fatalf("unexpected error: %v", perr)
	}
	if a2.Absolute {
		t.Error("Absolute default: want false")
	}
}

// TestPrettyResponse_AliasTableSingleAllowPath verifies that PrettyResponse
// prepends a @0 alias table when allow_paths is configured and rewrites paths
// under that root as @0/<tail>.
func TestPrettyResponse_AliasTableSingleAllowPath(t *testing.T) {
	root    := t.TempDir()
	scratch := t.TempDir()
	canon := func(p string) string {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return r
		}
		return p
	}
	jail.SetPolicy(jail.FromConfig(false, root, []string{scratch}, nil))
	defer jail.SetPolicy(nil)

	rsp := &proto.Response{
		OK: true,
		Data: proto.MustData(&Result{
			Matches: []Match{
				{Path: scratch + "/notes.md", Line: 1, Text: "hello", Col: 1},
			},
			Count:      1,
			MatchCount: 1,
			FileCount:  1,
		}),
	}
	req := &proto.Request{Verb: "grep", Args: map[string]any{"pattern": "hello", "path": scratch}}
	got := PrettyResponse(req, rsp)

	if !strings.Contains(got, "@0 = "+canon(scratch)) {
		t.Errorf("alias header missing: want @0 = %s in %q", canon(scratch), got)
	}
	if !strings.Contains(got, "@0/notes.md") {
		t.Errorf("aliased path missing: want @0/notes.md in %q", got)
	}
}

// TestPrettyResponse_AliasTableTwoAllowPaths verifies the multi-root case:
// two allow_paths entries produce @0 and @1 aliases in a single response.
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
			Matches: []Match{
				{Path: scratch + "/a.md", Line: 1, Text: "x", Col: 1},
				{Path: vendor + "/pkg/b.go", Line: 3, Text: "x", Col: 2},
			},
			Count:      2,
			MatchCount: 2,
			FileCount:  2,
		}),
	}
	req := &proto.Request{Verb: "grep", Args: map[string]any{"pattern": "x", "path": scratch}}
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

// TestPrettyResponse_FilesOnlyAliasTable verifies alias table works in files_only mode.
func TestPrettyResponse_FilesOnlyAliasTable(t *testing.T) {
	root    := t.TempDir()
	scratch := t.TempDir()
	canon := func(p string) string {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return r
		}
		return p
	}
	jail.SetPolicy(jail.FromConfig(false, root, []string{scratch}, nil))
	defer jail.SetPolicy(nil)

	rsp := &proto.Response{
		OK: true,
		Data: proto.MustData(&Result{
			Files:      []string{scratch + "/notes.md"},
			Count:      1,
			MatchCount: 1,
			FileCount:  1,
		}),
	}
	req := &proto.Request{Verb: "grep", Args: map[string]any{"pattern": "x", "path": scratch, "fo": true}}
	got := PrettyResponse(req, rsp)

	if !strings.Contains(got, "@0 = "+canon(scratch)) {
		t.Errorf("alias header missing in files_only output: %q", got)
	}
	if !strings.Contains(got, "@0/notes.md") {
		t.Errorf("aliased file path missing in files_only output: %q", got)
	}
}

// TestPrettyResponse_NoAliasWithoutAllowPaths confirms no alias table is emitted
// when allow_paths is empty.
func TestPrettyResponse_NoAliasWithoutAllowPaths(t *testing.T) {
	root := t.TempDir()
	jail.SetPolicy(jail.FromConfig(false, root, nil, nil))
	defer jail.SetPolicy(nil)

	rsp := &proto.Response{
		OK: true,
		Data: proto.MustData(&Result{
			Matches:    []Match{{Path: "internal/foo.go", Line: 1, Text: "x", Col: 1}},
			Count:      1,
			MatchCount: 1,
			FileCount:  1,
		}),
	}
	req := &proto.Request{Verb: "grep", Args: map[string]any{"pattern": "x", "path": "."}}
	got := PrettyResponse(req, rsp)

	if strings.Contains(got, "@0") {
		t.Errorf("no alias expected when allow_paths empty, got %q", got)
	}
}

// -- ASH-88: error messages strip project-root prefix ---------------------

func TestRun_NotFoundErrorStripsPrefix_Grep(t *testing.T) {
	root := t.TempDir()
	jail.SetPolicy(jail.FromConfig(false, root, nil, nil))
	defer jail.SetPolicy(nil)

	missing := filepath.Join(root, "no-such-path")
	_, perr := Run(&Args{Path: missing, Pattern: "foo"}, nil)
	if perr == nil || perr.Code != "not_found" {
		t.Fatalf("expected not_found, got %+v", perr)
	}
	if strings.Contains(perr.Msg, root) {
		t.Errorf("error Msg should not contain project root, got %q", perr.Msg)
	}
}
