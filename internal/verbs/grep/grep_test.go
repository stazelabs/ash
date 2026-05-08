package grep

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
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
	if !strings.Contains(res.TruncationHint, "max_matches=3") {
		t.Errorf("hint missing max_matches info: %q", res.TruncationHint)
	}
	// User-set limit below cap; raising should still be suggested.
	if !strings.Contains(res.TruncationHint, "raise --max_matches") {
		t.Errorf("hint should suggest raising --max_matches below cap: %q", res.TruncationHint)
	}
}

// TestRun_TruncationHintAtHardCap covers ASH-12: when --max_matches is at
// MaxMaxMatches, "raise --max_matches" is a dead-end suggestion. Hint should
// pivot to narrowing.
func TestRun_TruncationHintAtHardCap(t *testing.T) {
	t.Run("matches mode", func(t *testing.T) {
		hint := truncationHint(MaxMaxMatches, false)
		if strings.Contains(hint, "raise --max_matches") {
			t.Errorf("hint at cap must not suggest raising: %q", hint)
		}
		if !strings.Contains(hint, "hard cap") || !strings.Contains(hint, "narrow") {
			t.Errorf("hint at cap should mention the cap and suggest narrowing: %q", hint)
		}
	})
	t.Run("files_only mode", func(t *testing.T) {
		hint := truncationHint(MaxMaxMatches, true)
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
		"pattern": "x", "path": ".", "max_matches": MaxMaxMatches + 1000,
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
		"context_before": 999, "context_after": 999,
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
