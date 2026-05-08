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

func TestParseArgs_RangeModeDefaults(t *testing.T) {
	a, perr := ParseArgs(map[string]any{"path": "f.go", "range": "1:3"})
	if perr != nil {
		t.Fatalf("unexpected: %+v", perr)
	}
	if a.NewContent != "" {
		t.Errorf("new_content default=%q want empty", a.NewContent)
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

// -- helpers ---------------------------------------------------------------

func okResponse(r *Result) *proto.Response {
	return &proto.Response{OK: true, Data: r}
}
