package diff

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stazelabs/ash/internal/jail"
	"github.com/stazelabs/ash/internal/proto"
)

func TestParseArgs_RequiresPath(t *testing.T) {
	_, perr := ParseArgs(map[string]any{"other": "b.go"})
	if perr == nil || perr.Code != "args" {
		t.Fatalf("expected args error, got %+v", perr)
	}
}

func TestParseArgs_RequiresOtherOrContent(t *testing.T) {
	_, perr := ParseArgs(map[string]any{"path": "a.go"})
	if perr == nil || perr.Code != "args" {
		t.Fatalf("expected args error, got %+v", perr)
	}
}

func TestParseArgs_BothOtherAndContent(t *testing.T) {
	_, perr := ParseArgs(map[string]any{"path": "a.go", "other": "b.go", "content": "x"})
	if perr == nil || perr.Code != "args" {
		t.Fatalf("expected args error, got %+v", perr)
	}
}

func TestParseArgs_OtherMode(t *testing.T) {
	a, perr := ParseArgs(map[string]any{"path": "a.go", "other": "b.go"})
	if perr != nil {
		t.Fatal(perr)
	}
	if a.Other != "b.go" || a.Context != 3 {
		t.Errorf("unexpected: %+v", a)
	}
}

func TestParseArgs_ContentMode(t *testing.T) {
	a, perr := ParseArgs(map[string]any{"path": "a.go", "content": "hello"})
	if perr != nil {
		t.Fatal(perr)
	}
	if a.Content != "hello" {
		t.Errorf("content=%q", a.Content)
	}
}

// -- Run ------------------------------------------------------------------

func TestRun_Identical(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.txt")
	_ = os.WriteFile(p, []byte("hello\n"), 0o644)

	a := &Args{Path: p, Content: "hello\n", Context: 3}
	r, perr := Run(a, nil)
	if perr != nil {
		t.Fatal(perr)
	}
	if !r.Unchanged {
		t.Errorf("expected unchanged=true for identical content")
	}
	if r.Additions != 0 || r.Deletions != 0 {
		t.Errorf("add=%d del=%d want 0,0", r.Additions, r.Deletions)
	}
}

func TestRun_FileVsContent(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.txt")
	_ = os.WriteFile(p, []byte("line1\nline2\nline3\n"), 0o644)

	a := &Args{Path: p, Content: "line1\nLINE2\nline3\n", Context: 1}
	r, perr := Run(a, nil)
	if perr != nil {
		t.Fatal(perr)
	}
	if r.Unchanged {
		t.Error("expected changed")
	}
	if r.Additions != 1 || r.Deletions != 1 {
		t.Errorf("add=%d del=%d want 1,1", r.Additions, r.Deletions)
	}
	if !strings.Contains(r.Patch, "-line2") {
		t.Errorf("expected -line2 in patch: %s", r.Patch)
	}
	if !strings.Contains(r.Patch, "+LINE2") {
		t.Errorf("expected +LINE2 in patch: %s", r.Patch)
	}
}

func TestRun_TwoFiles(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.txt")
	b := filepath.Join(dir, "b.txt")
	_ = os.WriteFile(a, []byte("old\n"), 0o644)
	_ = os.WriteFile(b, []byte("new\n"), 0o644)

	args := &Args{Path: a, Other: b, Context: 3}
	r, perr := Run(args, nil)
	if perr != nil {
		t.Fatal(perr)
	}
	if r.Additions != 1 || r.Deletions != 1 {
		t.Errorf("add=%d del=%d want 1,1", r.Additions, r.Deletions)
	}
}

func TestRun_FileNotFound(t *testing.T) {
	a := &Args{Path: "/no/such/file", Content: "x"}
	_, perr := Run(a, nil)
	if perr == nil || perr.Code != "not_found" {
		t.Fatalf("expected not_found, got %+v", perr)
	}
}

// -- stat mode ------------------------------------------------------------------

func TestParseArgs_StatFlag(t *testing.T) {
	a, perr := ParseArgs(map[string]any{"path": "a.go", "other": "b.go", "stat": "true"})
	if perr != nil {
		t.Fatal(perr)
	}
	if !a.Stat {
		t.Error("want Stat=true")
	}
}

func TestParseArgs_StatDefault(t *testing.T) {
	a, perr := ParseArgs(map[string]any{"path": "a.go", "content": "x"})
	if perr != nil {
		t.Fatal(perr)
	}
	if a.Stat {
		t.Error("want Stat=false by default")
	}
}

// TestParseArgs_WireShape verifies that context (int) and stat (bool) accept
// string-typed values (the wire shape from CLI parseFlags) and that invalid
// strings are rejected. stat=true is already covered by TestParseArgs_StatFlag;
// this test adds context and the invalid-string path.
func TestParseArgs_WireShape(t *testing.T) {
	a, perr := ParseArgs(map[string]any{
		"path":    "a.go",
		"other":   "b.go",
		"context": "5",
		"stat":    "false",
	})
	if perr != nil {
		t.Fatalf("valid string args rejected: %v", perr)
	}
	if a.Context != 5 {
		t.Errorf("context: got %d, want 5", a.Context)
	}
	if a.Stat {
		t.Error("stat: want false")
	}

	_, perr = ParseArgs(map[string]any{"path": "a.go", "other": "b.go", "context": "abc"})
	if perr == nil {
		t.Error("expected error for context=abc")
	}
	_, perr = ParseArgs(map[string]any{"path": "a.go", "other": "b.go", "stat": "maybe"})
	if perr == nil {
		t.Error("expected error for stat=maybe")
	}
}

func TestRun_StatMode_Changed(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.txt")
	_ = os.WriteFile(p, []byte("line1\nline2\nline3\n"), 0o644)

	a := &Args{Path: p, Content: "line1\nLINE2\nline3\n", Context: 3, Stat: true}
	r, perr := Run(a, nil)
	if perr != nil {
		t.Fatal(perr)
	}
	if r.Patch != "" {
		t.Errorf("want empty patch in stat mode, got %q", r.Patch)
	}
	if r.Additions != 1 || r.Deletions != 1 {
		t.Errorf("add=%d del=%d want 1,1", r.Additions, r.Deletions)
	}
	if r.Unchanged {
		t.Error("want unchanged=false for changed content")
	}
}

func TestRun_StatMode_Identical(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.txt")
	_ = os.WriteFile(p, []byte("hello\n"), 0o644)

	a := &Args{Path: p, Content: "hello\n", Context: 3, Stat: true}
	r, perr := Run(a, nil)
	if perr != nil {
		t.Fatal(perr)
	}
	if r.Patch != "" {
		t.Errorf("want empty patch in stat mode, got %q", r.Patch)
	}
	if !r.Unchanged {
		t.Error("want unchanged=true for identical content")
	}
}

func TestPrettyResponse_StatMode(t *testing.T) {
	r := &Result{PathA: "a.go", PathB: "b.go", Additions: 2, Deletions: 1}
	got := PrettyResponse(nil, okResponse(r))
	if strings.Contains(got, "\n") {
		t.Errorf("stat-mode pretty should be single line, got: %q", got)
	}
	if !strings.Contains(got, "+2") || !strings.Contains(got, "-1") {
		t.Errorf("expected counts in stat-mode output: %q", got)
	}
}

// -- PrettyResponse -------------------------------------------------------

func TestPrettyResponse_Unchanged(t *testing.T) {
	r := &Result{PathA: "a.go", PathB: "b.go", Unchanged: true}
	got := PrettyResponse(nil, okResponse(r))
	if !strings.Contains(got, "identical") {
		t.Errorf("expected 'identical' in: %s", got)
	}
}

func TestPrettyResponse_WithPatch(t *testing.T) {
	r := &Result{
		PathA:     "a.go",
		PathB:     "b.go",
		Additions: 1,
		Deletions: 1,
		Patch:     "--- a.go\n+++ b.go\n@@ -1,1 +1,1 @@\n-old\n+new\n",
	}
	got := PrettyResponse(nil, okResponse(r))
	if !strings.Contains(got, "+1") {
		t.Errorf("expected addition count in: %s", got)
	}
	if !strings.Contains(got, "-old") {
		t.Errorf("expected patch in output: %s", got)
	}
}

// -- path-relativization tests (ASH-86) ------------------------------------

func TestRun_DefaultStripsRepoRootPrefix_Diff(t *testing.T) {
	dir := t.TempDir()
	fileA := filepath.Join(dir, "a.go")
	fileB := filepath.Join(dir, "b.go")
	if err := os.WriteFile(fileA, []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fileB, []byte("package b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	jail.SetPolicy(jail.FromConfig(false, dir, nil, nil))
	defer jail.SetPolicy(nil)

	res, perr := Run(&Args{Path: fileA, Other: fileB, Context: 3}, nil)
	if perr != nil {
		t.Fatalf("unexpected error: %+v", perr)
	}
	if filepath.IsAbs(res.PathA) {
		t.Errorf("PathA should be repo-relative, got absolute: %q", res.PathA)
	}
	if filepath.IsAbs(res.PathB) {
		t.Errorf("PathB should be repo-relative, got absolute: %q", res.PathB)
	}
	if res.PathA != "a.go" {
		t.Errorf("PathA: got %q want %q", res.PathA, "a.go")
	}
	if res.PathB != "b.go" {
		t.Errorf("PathB: got %q want %q", res.PathB, "b.go")
	}
}

func TestRun_AbsoluteFlagPreservesAbsolutePaths_Diff(t *testing.T) {
	dir := t.TempDir()
	fileA := filepath.Join(dir, "a.go")
	fileB := filepath.Join(dir, "b.go")
	if err := os.WriteFile(fileA, []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fileB, []byte("package b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	jail.SetPolicy(jail.FromConfig(false, dir, nil, nil))
	defer jail.SetPolicy(nil)

	res, perr := Run(&Args{Path: fileA, Other: fileB, Context: 3, Absolute: true}, nil)
	if perr != nil {
		t.Fatalf("unexpected error: %+v", perr)
	}
	if !filepath.IsAbs(res.PathA) {
		t.Errorf("with Absolute=true, PathA should be absolute: %q", res.PathA)
	}
	if !filepath.IsAbs(res.PathB) {
		t.Errorf("with Absolute=true, PathB should be absolute: %q", res.PathB)
	}
}

func TestRun_NoPolicyLeavesPathsAlone_Diff(t *testing.T) {
	dir := t.TempDir()
	fileA := filepath.Join(dir, "a.go")
	fileB := filepath.Join(dir, "b.go")
	if err := os.WriteFile(fileA, []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fileB, []byte("package b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	jail.SetPolicy(nil)

	res, perr := Run(&Args{Path: fileA, Other: fileB, Context: 3}, nil)
	if perr != nil {
		t.Fatalf("unexpected error: %+v", perr)
	}
	if !filepath.IsAbs(res.PathA) {
		t.Errorf("without jail policy, PathA should stay absolute: %q", res.PathA)
	}
	if !filepath.IsAbs(res.PathB) {
		t.Errorf("without jail policy, PathB should stay absolute: %q", res.PathB)
	}
}

func TestParseArgs_AbsoluteFlag_Diff(t *testing.T) {
	a, perr := ParseArgs(map[string]any{"path": "a.go", "content": "x", "absolute": true})
	if perr != nil {
		t.Fatalf("unexpected error: %v", perr)
	}
	if !a.Absolute {
		t.Error("Absolute: want true")
	}
	a2, perr := ParseArgs(map[string]any{"path": "a.go", "content": "x"})
	if perr != nil {
		t.Fatalf("unexpected error: %v", perr)
	}
	if a2.Absolute {
		t.Error("Absolute default: want false")
	}
}

// -- helpers --------------------------------------------------------------

func okResponse(r *Result) *proto.Response {
	return &proto.Response{OK: true, Data: proto.MustData(r)}
}

// -- ASH-88: error messages strip project-root prefix ---------------------

func TestRun_NotFoundErrorStripsPrefix_Diff(t *testing.T) {
	root := t.TempDir()
	jail.SetPolicy(jail.FromConfig(false, root, nil, nil))
	defer jail.SetPolicy(nil)

	missing := filepath.Join(root, "no-such-file.go")
	_, perr := Run(&Args{Path: missing, Content: "x"}, nil)
	if perr == nil || perr.Code != "not_found" {
		t.Fatalf("expected not_found, got %+v", perr)
	}
	if strings.Contains(perr.Msg, root) {
		t.Errorf("error Msg should not contain project root, got %q", perr.Msg)
	}
}
