package write

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"github.com/stazelabs/ash/internal/proto"
)

func TestParseArgs_RequiresPath(t *testing.T) {
	_, perr := ParseArgs(map[string]any{"content": "hello"})
	if perr == nil || perr.Code != "args" {
		t.Fatalf("expected args error for missing path, got %+v", perr)
	}
}

func TestParseArgs_Defaults(t *testing.T) {
	a, perr := ParseArgs(map[string]any{"path": "foo.go", "content": "x"})
	if perr != nil {
		t.Fatalf("unexpected: %+v", perr)
	}
	if a.Encoding != "utf-8" {
		t.Errorf("encoding default=%q want utf-8", a.Encoding)
	}
	if !a.Mkdir {
		t.Errorf("mkdir default should be true")
	}
	if a.CreateOnly {
		t.Errorf("create_only default should be false")
	}
}

func TestParseArgs_InvalidEncoding(t *testing.T) {
	_, perr := ParseArgs(map[string]any{"path": "f", "encoding": "latin1"})
	if perr == nil || perr.Code != "args" {
		t.Fatalf("expected args error for invalid encoding, got %+v", perr)
	}
}

// TestParseArgs_WireShape verifies that every bool arg accepts string-typed
// values (the wire shape from CLI parseFlags) and rejects garbage.
func TestParseArgs_WireShape(t *testing.T) {
	a, perr := ParseArgs(map[string]any{
		"path":        "f.go",
		"mkdir":       "false",
		"create_only": "true",
	})
	if perr != nil {
		t.Fatalf("valid string args rejected: %v", perr)
	}
	if a.Mkdir {
		t.Error("mkdir: want false")
	}
	if !a.CreateOnly {
		t.Error("create_only: want true")
	}

	for _, bad := range []struct{ key, val string }{
		{"mkdir", "maybe"},
		{"create_only", "maybe"},
	} {
		_, perr := ParseArgs(map[string]any{"path": "f.go", bad.key: bad.val})
		if perr == nil {
			t.Errorf("expected error for %s=%q", bad.key, bad.val)
		}
	}
}

// -- Run unit tests -------------------------------------------------------

func TestRun_CreateNewFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hello.txt")
	a := &Args{Path: path, Content: "hello world\n", Encoding: "utf-8", Mkdir: true}
	res, perr := Run(a, nil)
	if perr != nil {
		t.Fatalf("Run: %+v", perr)
	}
	if !res.Created {
		t.Errorf("expected created=true for new file")
	}
	if res.BytesWritten != len("hello world\n") {
		t.Errorf("bytes_written=%d want %d", res.BytesWritten, len("hello world\n"))
	}
	got, _ := os.ReadFile(path)
	if string(got) != "hello world\n" {
		t.Errorf("file content=%q want %q", got, "hello world\n")
	}
}

func TestRun_OverwriteExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	_ = os.WriteFile(path, []byte("old content"), 0o644)

	a := &Args{Path: path, Content: "new content", Encoding: "utf-8", Mkdir: true}
	res, perr := Run(a, nil)
	if perr != nil {
		t.Fatalf("Run: %+v", perr)
	}
	if res.Created {
		t.Errorf("expected created=false for overwrite")
	}
	got, _ := os.ReadFile(path)
	if string(got) != "new content" {
		t.Errorf("file content=%q want %q", got, "new content")
	}
}

func TestRun_CreateOnlyRejectsExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "exists.txt")
	_ = os.WriteFile(path, []byte("existing"), 0o644)

	a := &Args{Path: path, Content: "new", Encoding: "utf-8", Mkdir: true, CreateOnly: true}
	_, perr := Run(a, nil)
	if perr == nil || perr.Code != "exists" {
		t.Fatalf("expected exists error, got %+v", perr)
	}
}

func TestRun_CreateOnlySucceedsForNewFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "brand_new.txt")
	a := &Args{Path: path, Content: "fresh", Encoding: "utf-8", Mkdir: true, CreateOnly: true}
	res, perr := Run(a, nil)
	if perr != nil {
		t.Fatalf("Run: %+v", perr)
	}
	if !res.Created {
		t.Errorf("expected created=true")
	}
}

func TestRun_MkdirCreatesParents(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a", "b", "c", "file.go")
	a := &Args{Path: path, Content: "package c\n", Encoding: "utf-8", Mkdir: true}
	_, perr := Run(a, nil)
	if perr != nil {
		t.Fatalf("Run with mkdir: %+v", perr)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("file not created: %v", err)
	}
}

func TestRun_NoMkdirFailsWithMissingParent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "missing_parent", "file.go")
	a := &Args{Path: path, Content: "x", Encoding: "utf-8", Mkdir: false}
	_, perr := Run(a, nil)
	if perr == nil || perr.Code != "no_parent" {
		t.Fatalf("expected no_parent error, got %+v", perr)
	}
}

func TestRun_IsDirectoryError(t *testing.T) {
	dir := t.TempDir()
	a := &Args{Path: dir, Content: "x", Encoding: "utf-8", Mkdir: true}
	_, perr := Run(a, nil)
	if perr == nil || perr.Code != "is_dir" {
		t.Fatalf("expected is_dir error, got %+v", perr)
	}
}

func TestRun_Base64Encoding(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "binary.bin")
	rawBytes := []byte{0x00, 0x01, 0x02, 0xFF, 0xFE}
	encoded := base64.StdEncoding.EncodeToString(rawBytes)

	a := &Args{Path: path, Content: encoded, Encoding: "base64", Mkdir: true}
	res, perr := Run(a, nil)
	if perr != nil {
		t.Fatalf("Run base64: %+v", perr)
	}
	if res.BytesWritten != len(rawBytes) {
		t.Errorf("bytes_written=%d want %d", res.BytesWritten, len(rawBytes))
	}
	got, _ := os.ReadFile(path)
	if string(got) != string(rawBytes) {
		t.Errorf("binary content mismatch")
	}
}

func TestRun_InvalidBase64(t *testing.T) {
	dir := t.TempDir()
	a := &Args{Path: filepath.Join(dir, "f"), Content: "not!!base64@@", Encoding: "base64", Mkdir: true}
	_, perr := Run(a, nil)
	if perr == nil || perr.Code != "args" {
		t.Fatalf("expected args error for bad base64, got %+v", perr)
	}
}

func TestRun_EmptyContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.txt")
	a := &Args{Path: path, Content: "", Encoding: "utf-8", Mkdir: true}
	res, perr := Run(a, nil)
	if perr != nil {
		t.Fatalf("Run empty: %+v", perr)
	}
	if res.BytesWritten != 0 {
		t.Errorf("bytes_written=%d want 0", res.BytesWritten)
	}
	info, _ := os.Stat(path)
	if info.Size() != 0 {
		t.Errorf("file size=%d want 0", info.Size())
	}
}

// -- PrettyResponse tests -------------------------------------------------

func TestPrettyResponse_Created(t *testing.T) {
	r := &Result{Path: "foo/bar.go", BytesWritten: 42, Created: true}
	got := PrettyResponse(nil, okResponse(r))
	want := "=== ash write: foo/bar.go [42B, created] ==="
	if got != want {
		t.Errorf("pretty=%q want %q", got, want)
	}
}

func TestPrettyResponse_Overwritten(t *testing.T) {
	r := &Result{Path: "foo/bar.go", BytesWritten: 100, Created: false}
	got := PrettyResponse(nil, okResponse(r))
	want := "=== ash write: foo/bar.go [100B, overwritten] ==="
	if got != want {
		t.Errorf("pretty=%q want %q", got, want)
	}
}

// -- helpers --------------------------------------------------------------

func okResponse(r *Result) *proto.Response {
	return &proto.Response{OK: true, Data: proto.MustData(r)}
}
