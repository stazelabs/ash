package edit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stazelabs/ash/internal/proto"
)

// -- ParseArgs -------------------------------------------------------------

func TestParseArgs_RequiresPath(t *testing.T) {
	_, perr := ParseArgs(map[string]any{"old_string": "foo"})
	if perr == nil || perr.Code != "args" {
		t.Fatalf("expected args error for missing path, got %+v", perr)
	}
}

func TestParseArgs_RequiresOldStringOrRange(t *testing.T) {
	_, perr := ParseArgs(map[string]any{"path": "f.go"})
	if perr == nil || perr.Code != "args" {
		t.Fatalf("expected args error for missing old_string/range, got %+v", perr)
	}
}

func TestParseArgs_BothOldStringAndRange(t *testing.T) {
	_, perr := ParseArgs(map[string]any{"path": "f.go", "old_string": "a", "range": "1:2"})
	if perr == nil || perr.Code != "args" {
		t.Fatalf("expected args error for both old_string and range, got %+v", perr)
	}
}

func TestParseArgs_StringModeDefaults(t *testing.T) {
	a, perr := ParseArgs(map[string]any{"path": "f.go", "old_string": "old"})
	if perr != nil {
		t.Fatalf("unexpected: %+v", perr)
	}
	if a.NewString != "" {
		t.Errorf("new_string default=%q want empty", a.NewString)
	}
	if a.ReplaceAll {
		t.Errorf("replace_all default should be false")
	}
}

// TestParseArgs_WireShape verifies that every bool arg accepts string-typed
// values (the wire shape from CLI parseFlags) and rejects garbage.
func TestParseArgs_WireShape(t *testing.T) {
	a, perr := ParseArgs(map[string]any{
		"path":        "f.go",
		"old_string":  "old",
		"replace_all": "true",
		"dry_run":     "true",
	})
	if perr != nil {
		t.Fatalf("valid string args rejected: %v", perr)
	}
	if !a.ReplaceAll {
		t.Error("replace_all: want true")
	}
	if !a.DryRun {
		t.Error("dry_run: want true")
	}

	for _, bad := range []struct{ key, val string }{
		{"replace_all", "maybe"},
		{"dry_run", "maybe"},
	} {
		_, perr := ParseArgs(map[string]any{
			"path":       "f.go",
			"old_string": "old",
			bad.key:      bad.val,
		})
		if perr == nil {
			t.Errorf("expected error for %s=%q", bad.key, bad.val)
		}
	}
}

func TestParseArgs_RangeModeDefaults(t *testing.T) {
	a, perr := ParseArgs(map[string]any{"path": "f.go", "range": "1:3"})
	if perr != nil {
		t.Fatalf("unexpected: %+v", perr)
	}
	if a.NewContent != "" {
		t.Errorf("new_content default=%q want empty", a.NewContent)
	}
}

func TestParseArgs_PatchMode(t *testing.T) {
	a, perr := ParseArgs(map[string]any{"path": "f.go", "patch": "--- f\n+++ f\n@@ -1 +1 @@\n-old\n+new\n"})
	if perr != nil {
		t.Fatalf("unexpected: %+v", perr)
	}
	if a.Patch == "" {
		t.Error("patch should be set")
	}
}

func TestParseArgs_PatchExclusive(t *testing.T) {
	cases := []map[string]any{
		{"path": "f.go", "patch": "x", "old_string": "y"},
		{"path": "f.go", "patch": "x", "range": "1:2"},
		{"path": "f.go", "patch": "x", "old_string": "y", "range": "1:2"},
	}
	for _, in := range cases {
		_, perr := ParseArgs(in)
		if perr == nil || perr.Code != "args" {
			t.Errorf("expected args error for conflicting modes, got %+v (input: %v)", perr, in)
		}
	}
}

// -- applyStringReplace ----------------------------------------------------

func TestStringReplace_Basic(t *testing.T) {
	got, n, perr := applyStringReplace("hello world", "world", "there", false)
	if perr != nil {
		t.Fatal(perr)
	}
	if got != "hello there" {
		t.Errorf("got=%q want %q", got, "hello there")
	}
	if n != 1 {
		t.Errorf("occurrences=%d want 1", n)
	}
}

func TestStringReplace_Deletion(t *testing.T) {
	got, _, perr := applyStringReplace("foo bar foo", "foo ", "", false)
	if perr != nil {
		t.Fatal(perr)
	}
	if got != "bar foo" {
		t.Errorf("got=%q want %q", got, "bar foo")
	}
}

func TestStringReplace_NotFound(t *testing.T) {
	_, _, perr := applyStringReplace("hello", "xyz", "abc", false)
	if perr == nil || perr.Code != "match_not_found" {
		t.Fatalf("expected match_not_found, got %+v", perr)
	}
}

func TestStringReplace_Ambiguous(t *testing.T) {
	_, _, perr := applyStringReplace("foo foo foo", "foo", "bar", false)
	if perr == nil || perr.Code != "ambiguous" {
		t.Fatalf("expected ambiguous, got %+v", perr)
	}
}

func TestStringReplace_ReplaceAll(t *testing.T) {
	got, n, perr := applyStringReplace("foo foo foo", "foo", "bar", true)
	if perr != nil {
		t.Fatal(perr)
	}
	if got != "bar bar bar" {
		t.Errorf("got=%q want %q", got, "bar bar bar")
	}
	if n != 3 {
		t.Errorf("occurrences=%d want 3", n)
	}
}

func TestStringReplace_MultilineOldString(t *testing.T) {
	content := "func foo() {\n\treturn 1\n}\n"
	old := "return 1\n}"
	new_ := "return 2\n}"
	got, _, perr := applyStringReplace(content, old, new_, false)
	if perr != nil {
		t.Fatal(perr)
	}
	if !strings.Contains(got, "return 2") {
		t.Errorf("replacement not applied: %q", got)
	}
}

// -- applyLineRange --------------------------------------------------------

func TestLineRange_Basic(t *testing.T) {
	content := "line1\nline2\nline3\nline4\n"
	got, perr := applyLineRange(content, "2:3", "newA\nnewB\n")
	if perr != nil {
		t.Fatal(perr)
	}
	want := "line1\nnewA\nnewB\nline4\n"
	if got != want {
		t.Errorf("got=%q want %q", got, want)
	}
}

func TestLineRange_NormalizesMissingTrailingNewline(t *testing.T) {
	content := "line1\nline2\nline3\n"
	got, perr := applyLineRange(content, "2:2", "replaced")
	if perr != nil {
		t.Fatal(perr)
	}
	want := "line1\nreplaced\nline3\n"
	if got != want {
		t.Errorf("got=%q want %q", got, want)
	}
}

func TestLineRange_DeleteLines(t *testing.T) {
	content := "line1\nline2\nline3\n"
	got, perr := applyLineRange(content, "2:2", "")
	if perr != nil {
		t.Fatal(perr)
	}
	want := "line1\nline3\n"
	if got != want {
		t.Errorf("got=%q want %q", got, want)
	}
}

func TestLineRange_ReplaceFirstLine(t *testing.T) {
	content := "line1\nline2\nline3\n"
	got, perr := applyLineRange(content, "1:1", "header\n")
	if perr != nil {
		t.Fatal(perr)
	}
	want := "header\nline2\nline3\n"
	if got != want {
		t.Errorf("got=%q want %q", got, want)
	}
}

func TestLineRange_ReplaceLastLine(t *testing.T) {
	content := "line1\nline2\nline3\n"
	got, perr := applyLineRange(content, "3:3", "footer\n")
	if perr != nil {
		t.Fatal(perr)
	}
	want := "line1\nline2\nfooter\n"
	if got != want {
		t.Errorf("got=%q want %q", got, want)
	}
}

func TestLineRange_ReplaceAll(t *testing.T) {
	content := "line1\nline2\n"
	got, perr := applyLineRange(content, "1:2", "only\n")
	if perr != nil {
		t.Fatal(perr)
	}
	want := "only\n"
	if got != want {
		t.Errorf("got=%q want %q", got, want)
	}
}

func TestLineRange_EndClampedToFileLength(t *testing.T) {
	content := "a\nb\nc\n"
	got, perr := applyLineRange(content, "2:99", "z\n")
	if perr != nil {
		t.Fatal(perr)
	}
	want := "a\nz\n"
	if got != want {
		t.Errorf("got=%q want %q", got, want)
	}
}

func TestLineRange_StartBeyondFile(t *testing.T) {
	_, perr := applyLineRange("a\nb\n", "5:5", "x")
	if perr == nil || perr.Code != "range_out_of_bounds" {
		t.Fatalf("expected range_out_of_bounds, got %+v", perr)
	}
}

func TestLineRange_EmptyFile(t *testing.T) {
	_, perr := applyLineRange("", "1:1", "x")
	if perr == nil || perr.Code != "range_out_of_bounds" {
		t.Fatalf("expected range_out_of_bounds for empty file, got %+v", perr)
	}
}

func TestLineRange_InvalidRange(t *testing.T) {
	_, perr := applyLineRange("a\n", "notanumber:2", "x")
	if perr == nil || perr.Code != "args" {
		t.Fatalf("expected args error, got %+v", perr)
	}
}

func TestLineRange_StartGreaterThanEnd(t *testing.T) {
	_, perr := applyLineRange("a\n", "3:1", "x")
	if perr == nil || perr.Code != "args" {
		t.Fatalf("expected args error, got %+v", perr)
	}
}

// -- parseHunkHeader -------------------------------------------------------

func TestParseHunkHeader_FullFormat(t *testing.T) {
	oldStart, oldCount, newStart, newCount, err := parseHunkHeader("@@ -1,4 +1,6 @@")
	if err != nil {
		t.Fatal(err)
	}
	if oldStart != 1 || oldCount != 4 || newStart != 1 || newCount != 6 {
		t.Errorf("got (%d,%d,%d,%d) want (1,4,1,6)", oldStart, oldCount, newStart, newCount)
	}
}

func TestParseHunkHeader_WithContext(t *testing.T) {
	// Some tools append function name after the @@ marker
	oldStart, oldCount, newStart, newCount, err := parseHunkHeader("@@ -10,3 +10,4 @@ func foo() {")
	if err != nil {
		t.Fatal(err)
	}
	if oldStart != 10 || oldCount != 3 || newStart != 10 || newCount != 4 {
		t.Errorf("got (%d,%d,%d,%d) want (10,3,10,4)", oldStart, oldCount, newStart, newCount)
	}
}

func TestParseHunkHeader_ZeroCount(t *testing.T) {
	// @@ -0,0 +1,3 @@ means insertion at start of file
	oldStart, oldCount, newStart, newCount, err := parseHunkHeader("@@ -0,0 +1,3 @@")
	if err != nil {
		t.Fatal(err)
	}
	if oldStart != 0 || oldCount != 0 || newStart != 1 || newCount != 3 {
		t.Errorf("got (%d,%d,%d,%d) want (0,0,1,3)", oldStart, oldCount, newStart, newCount)
	}
}

func TestParseHunkHeader_OmittedCount(t *testing.T) {
	// Standard format allows omitting ",1" when count=1
	oldStart, _, newStart, _, err := parseHunkHeader("@@ -5 +5 @@")
	if err != nil {
		t.Fatal(err)
	}
	if oldStart != 5 || newStart != 5 {
		t.Errorf("got (%d,%d) want (5,5)", oldStart, newStart)
	}
}

func TestParseHunkHeader_Malformed(t *testing.T) {
	cases := []string{
		"not a header",
		"@@ -abc,3 +1,3 @@",
		"@@ +1,3 -1,3 @@",
		"@@",
	}
	for _, c := range cases {
		_, _, _, _, err := parseHunkHeader(c)
		if err == nil {
			t.Errorf("expected error for %q", c)
		}
	}
}

// -- applyPatch ------------------------------------------------------------

func TestApplyPatch_CleanApply(t *testing.T) {
	content := "line1\nline2\nline3\n"
	patch := "--- a\n+++ b\n@@ -1,3 +1,3 @@\n line1\n-line2\n+LINE2\n line3\n"

	got, hunks, perr := applyPatch(content, patch)
	if perr != nil {
		t.Fatalf("applyPatch: %+v", perr)
	}
	want := "line1\nLINE2\nline3\n"
	if got != want {
		t.Errorf("got=%q want=%q", got, want)
	}
	if hunks != 1 {
		t.Errorf("hunksApplied=%d want 1", hunks)
	}
}

func TestApplyPatch_MultiHunk(t *testing.T) {
	content := "a\nb\nc\nd\ne\nf\n"
	// Change line 2 and line 5
	patch := "--- a\n+++ b\n@@ -1,3 +1,3 @@\n a\n-b\n+B\n c\n@@ -4,3 +4,3 @@\n d\n-e\n+E\n f\n"

	got, hunks, perr := applyPatch(content, patch)
	if perr != nil {
		t.Fatalf("applyPatch: %+v", perr)
	}
	want := "a\nB\nc\nd\nE\nf\n"
	if got != want {
		t.Errorf("got=%q want=%q", got, want)
	}
	if hunks != 2 {
		t.Errorf("hunksApplied=%d want 2", hunks)
	}
}

func TestApplyPatch_AddOnly(t *testing.T) {
	content := "a\nb\n"
	// Insert a line after line 1
	patch := "--- a\n+++ b\n@@ -1,2 +1,3 @@\n a\n+inserted\n b\n"

	got, _, perr := applyPatch(content, patch)
	if perr != nil {
		t.Fatalf("applyPatch: %+v", perr)
	}
	want := "a\ninserted\nb\n"
	if got != want {
		t.Errorf("got=%q want=%q", got, want)
	}
}

func TestApplyPatch_DeleteOnly(t *testing.T) {
	content := "a\nb\nc\n"
	// Delete line 2
	patch := "--- a\n+++ b\n@@ -1,3 +1,2 @@\n a\n-b\n c\n"

	got, _, perr := applyPatch(content, patch)
	if perr != nil {
		t.Fatalf("applyPatch: %+v", perr)
	}
	want := "a\nc\n"
	if got != want {
		t.Errorf("got=%q want=%q", got, want)
	}
}

func TestApplyPatch_HunkMismatch_ContextLine(t *testing.T) {
	content := "a\nb\nc\n"
	// Patch expects "x" as context but file has "a"
	patch := "--- a\n+++ b\n@@ -1,3 +1,3 @@\n x\n-b\n+B\n c\n"

	_, _, perr := applyPatch(content, patch)
	if perr == nil || perr.Code != "patch_failed" {
		t.Fatalf("expected patch_failed, got %+v", perr)
	}
}

func TestApplyPatch_HunkMismatch_DeleteLine(t *testing.T) {
	content := "a\nb\nc\n"
	// Patch expects to delete "x" but file has "b"
	patch := "--- a\n+++ b\n@@ -1,3 +1,2 @@\n a\n-x\n c\n"

	_, _, perr := applyPatch(content, patch)
	if perr == nil || perr.Code != "patch_failed" {
		t.Fatalf("expected patch_failed, got %+v", perr)
	}
}

func TestApplyPatch_MalformedInput_Empty(t *testing.T) {
	_, _, perr := applyPatch("content\n", "")
	if perr == nil || perr.Code != "patch_parse_error" {
		t.Fatalf("expected patch_parse_error for empty patch, got %+v", perr)
	}
}

func TestApplyPatch_MalformedInput_NoHunks(t *testing.T) {
	_, _, perr := applyPatch("content\n", "--- a\n+++ b\n")
	if perr == nil || perr.Code != "patch_parse_error" {
		t.Fatalf("expected patch_parse_error for patch without hunks, got %+v", perr)
	}
}

func TestApplyPatch_MalformedInput_BadHeader(t *testing.T) {
	_, _, perr := applyPatch("content\n", "@@ not a valid header @@\n")
	if perr == nil || perr.Code != "patch_parse_error" {
		t.Fatalf("expected patch_parse_error for bad hunk header, got %+v", perr)
	}
}

func TestApplyPatch_UnknownBodyPrefix(t *testing.T) {
	patch := "--- a\n+++ b\n@@ -1,1 +1,1 @@\n?line\n"
	_, _, perr := applyPatch("line\n", patch)
	if perr == nil || perr.Code != "patch_parse_error" {
		t.Fatalf("expected patch_parse_error for unknown prefix, got %+v", perr)
	}
}

func TestApplyPatch_AppendToEndOfFile(t *testing.T) {
	content := "a\nb\n"
	// Add a line at the end using @@ -2,1 +2,2 @@
	patch := "--- a\n+++ b\n@@ -2,1 +2,2 @@\n b\n+c\n"

	got, _, perr := applyPatch(content, patch)
	if perr != nil {
		t.Fatalf("applyPatch: %+v", perr)
	}
	want := "a\nb\nc\n"
	if got != want {
		t.Errorf("got=%q want=%q", got, want)
	}
}

func TestApplyPatch_SkipsNoNewlineMarker(t *testing.T) {
	content := "a\nb\n"
	// Patch with "\ No newline at end of file" marker (from external tools)
	patch := "--- a\n+++ b\n@@ -1,2 +1,2 @@\n a\n-b\n+B\n\\ No newline at end of file\n"

	got, _, perr := applyPatch(content, patch)
	if perr != nil {
		t.Fatalf("applyPatch: %+v", perr)
	}
	want := "a\nB\n"
	if got != want {
		t.Errorf("got=%q want=%q", got, want)
	}
}

// -- Run (integration) -----------------------------------------------------

func TestRun_StringReplace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.go")
	_ = os.WriteFile(path, []byte("package main\n\nfunc old() {}\n"), 0o644)

	a := &Args{Path: path, OldString: "func old() {}", NewString: "func new() {}"}
	res, perr := Run(a, nil)
	if perr != nil {
		t.Fatalf("Run: %+v", perr)
	}
	if res.Occurrences != 1 {
		t.Errorf("occurrences=%d want 1", res.Occurrences)
	}
	got, _ := os.ReadFile(path)
	if !strings.Contains(string(got), "func new()") {
		t.Errorf("replacement not written: %q", got)
	}
}

func TestRun_LineRange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	_ = os.WriteFile(path, []byte("a\nb\nc\n"), 0o644)

	a := &Args{Path: path, Range: "2:2", NewContent: "B"}
	res, perr := Run(a, nil)
	if perr != nil {
		t.Fatalf("Run: %+v", perr)
	}
	if res.Occurrences != 1 {
		t.Errorf("occurrences=%d want 1", res.Occurrences)
	}
	got, _ := os.ReadFile(path)
	want := "a\nB\nc\n"
	if string(got) != want {
		t.Errorf("file=%q want %q", got, want)
	}
}

func TestRun_PatchMode_Write(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	_ = os.WriteFile(path, []byte("line1\nline2\nline3\n"), 0o644)

	patch := "--- a\n+++ b\n@@ -1,3 +1,3 @@\n line1\n-line2\n+LINE2\n line3\n"
	a := &Args{Path: path, Patch: patch}
	res, perr := Run(a, nil)
	if perr != nil {
		t.Fatalf("Run patch mode: %+v", perr)
	}
	if res.Occurrences != 1 {
		t.Errorf("occurrences (hunk count)=%d want 1", res.Occurrences)
	}
	got, _ := os.ReadFile(path)
	want := "line1\nLINE2\nline3\n"
	if string(got) != want {
		t.Errorf("file=%q want=%q", got, want)
	}
}

func TestRun_PatchMode_DryRun(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	original := "line1\nline2\nline3\n"
	_ = os.WriteFile(path, []byte(original), 0o644)

	patch := "--- a\n+++ b\n@@ -1,3 +1,3 @@\n line1\n-line2\n+LINE2\n line3\n"
	a := &Args{Path: path, Patch: patch, DryRun: true}
	res, perr := Run(a, nil)
	if perr != nil {
		t.Fatalf("Run patch dry_run: %+v", perr)
	}
	if !res.DryRun {
		t.Error("DryRun should be true")
	}
	if res.Patch == "" {
		t.Error("Patch field should contain unified diff")
	}
	// File should be unchanged
	got, _ := os.ReadFile(path)
	if string(got) != original {
		t.Errorf("file was modified in dry_run: %q", got)
	}
}

func TestRun_PatchMode_HunkMismatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	_ = os.WriteFile(path, []byte("a\nb\nc\n"), 0o644)

	// Patch expects "x" as first context line but file has "a"
	patch := "--- a\n+++ b\n@@ -1,3 +1,3 @@\n x\n-b\n+B\n c\n"
	a := &Args{Path: path, Patch: patch}
	_, perr := Run(a, nil)
	if perr == nil || perr.Code != "patch_failed" {
		t.Fatalf("expected patch_failed, got %+v", perr)
	}
}

func TestRun_PatchMode_MalformedPatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	_ = os.WriteFile(path, []byte("a\n"), 0o644)

	a := &Args{Path: path, Patch: "not a valid patch"}
	_, perr := Run(a, nil)
	if perr == nil || perr.Code != "patch_parse_error" {
		t.Fatalf("expected patch_parse_error, got %+v", perr)
	}
}

func TestRun_FileNotFound(t *testing.T) {
	a := &Args{Path: "/does/not/exist.go", OldString: "x"}
	_, perr := Run(a, nil)
	if perr == nil || perr.Code != "not_found" {
		t.Fatalf("expected not_found, got %+v", perr)
	}
}

func TestRun_IsDirectory(t *testing.T) {
	dir := t.TempDir()
	a := &Args{Path: dir, OldString: "x"}
	_, perr := Run(a, nil)
	if perr == nil || perr.Code != "is_dir" {
		t.Fatalf("expected is_dir, got %+v", perr)
	}
}

// -- PrettyResponse --------------------------------------------------------

func TestPrettyResponse_StringReplace(t *testing.T) {
	r := &Result{Path: "cmd/main.go", BytesWritten: 100, LinesTotal: 5, Occurrences: 1}
	got := PrettyResponse(nil, okResponse(r))
	want := "=== ash edit: cmd/main.go [100B, 1 replacement] ==="
	if got != want {
		t.Errorf("pretty=%q want %q", got, want)
	}
}

func TestPrettyResponse_ReplaceAll(t *testing.T) {
	r := &Result{Path: "a.go", BytesWritten: 50, LinesTotal: 3, Occurrences: 4}
	got := PrettyResponse(nil, okResponse(r))
	want := "=== ash edit: a.go [50B, 4 replacements] ==="
	if got != want {
		t.Errorf("pretty=%q want %q", got, want)
	}
}

func TestPrettyResponse_RangeMode(t *testing.T) {
	r := &Result{Path: "a.go", BytesWritten: 60, LinesTotal: 4, Occurrences: 1}
	req := &proto.Request{Args: map[string]any{"range": "3:5"}}
	got := PrettyResponse(req, okResponse(r))
	want := "=== ash edit: a.go [60B, lines 3:5 replaced] ==="
	if got != want {
		t.Errorf("pretty=%q want %q", got, want)
	}
}

func TestPrettyResponse_PatchMode_SingleHunk(t *testing.T) {
	r := &Result{Path: "a.go", BytesWritten: 80, LinesTotal: 5, Occurrences: 1}
	req := &proto.Request{Args: map[string]any{"patch": "--- a\n+++ b\n@@ -1 +1 @@\n-x\n+y\n"}}
	got := PrettyResponse(req, okResponse(r))
	want := "=== ash edit: a.go [80B, 1 hunk applied] ==="
	if got != want {
		t.Errorf("pretty=%q want %q", got, want)
	}
}

func TestPrettyResponse_PatchMode_MultiHunk(t *testing.T) {
	r := &Result{Path: "a.go", BytesWritten: 80, LinesTotal: 5, Occurrences: 3}
	req := &proto.Request{Args: map[string]any{"patch": "--- a\n+++ b\n@@ -1 +1 @@\n-x\n+y\n"}}
	got := PrettyResponse(req, okResponse(r))
	want := "=== ash edit: a.go [80B, 3 hunks applied] ==="
	if got != want {
		t.Errorf("pretty=%q want %q", got, want)
	}
}

func TestPrettyResponse_PatchMode_DryRun(t *testing.T) {
	r := &Result{Path: "a.go", LinesTotal: 5, Occurrences: 1, DryRun: true, Patch: "--- a\n+++ b\n"}
	req := &proto.Request{Args: map[string]any{"patch": "--- a\n+++ b\n@@ -1 +1 @@\n-x\n+y\n"}}
	got := PrettyResponse(req, okResponse(r))
	if !strings.Contains(got, "not written") {
		t.Errorf("dry_run output should mention 'not written': %q", got)
	}
	if !strings.Contains(got, "1 hunk") {
		t.Errorf("dry_run output should mention hunk count: %q", got)
	}
}

// -- helpers ---------------------------------------------------------------

func okResponse(r *Result) *proto.Response {
	return &proto.Response{OK: true, Data: proto.MustData(r)}
}
