package stat

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stazelabs/ash/internal/proto"
)

func makeFixture(t *testing.T) (filePath, dirPath, linkPath string) {
	t.Helper()
	root := t.TempDir()
	filePath = filepath.Join(root, "hello.go")
	if err := os.WriteFile(filePath, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dirPath = filepath.Join(root, "subdir")
	if err := os.Mkdir(dirPath, 0o755); err != nil {
		t.Fatal(err)
	}
	linkPath = filepath.Join(root, "link")
	if err := os.Symlink(filePath, linkPath); err != nil {
		t.Fatal(err)
	}
	return filePath, dirPath, linkPath
}

func TestParseArgs_missing(t *testing.T) {
	_, err := ParseArgs(map[string]any{})
	if err == nil || err.Code != "args" {
		t.Fatalf("want args error, got %v", err)
	}
}

func TestParseArgs_emptyAfterSplit(t *testing.T) {
	_, err := ParseArgs(map[string]any{"paths": "  ,  , "})
	if err == nil || err.Code != "args" {
		t.Fatalf("want args error for all-blank paths, got %v", err)
	}
}

func TestParseArgs_single(t *testing.T) {
	a, err := ParseArgs(map[string]any{"paths": "some/file.go"})
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Paths) != 1 || a.Paths[0] != "some/file.go" {
		t.Fatalf("unexpected paths: %v", a.Paths)
	}
}

func TestParseArgs_pathAlias(t *testing.T) {
	a, err := ParseArgs(map[string]any{"path": "some/file.go"})
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Paths) != 1 || a.Paths[0] != "some/file.go" {
		t.Fatalf("unexpected paths from --path alias: %v", a.Paths)
	}
}

func TestParseArgs_multi(t *testing.T) {
	a, err := ParseArgs(map[string]any{"paths": "a.go , b.go,c.go"})
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Paths) != 3 {
		t.Fatalf("want 3 paths, got %v", a.Paths)
	}
	if a.Paths[1] != "b.go" {
		t.Fatalf("unexpected middle path: %q", a.Paths[1])
	}
}

func TestRun_file(t *testing.T) {
	filePath, _, _ := makeFixture(t)
	res, perr := Run(&Args{Paths: []string{filePath}}, nil)
	if perr != nil {
		t.Fatal(perr)
	}
	if res.Count != 1 || res.Errors != 0 {
		t.Fatalf("count=%d errors=%d", res.Count, res.Errors)
	}
	e := res.Entries[0]
	if e.Type != "file" {
		t.Errorf("want type=file, got %q", e.Type)
	}
	if e.Size != int64(len("package main\n")) {
		t.Errorf("want size=%d, got %d", len("package main\n"), e.Size)
	}
	if e.Mtime == 0 {
		t.Error("mtime should be non-zero")
	}
	if e.Mode != "0644" {
		t.Errorf("want mode=0644, got %q", e.Mode)
	}
	if e.Error != "" {
		t.Errorf("want no error, got %q", e.Error)
	}
}

func TestRun_dir(t *testing.T) {
	_, dirPath, _ := makeFixture(t)
	res, perr := Run(&Args{Paths: []string{dirPath}}, nil)
	if perr != nil {
		t.Fatal(perr)
	}
	e := res.Entries[0]
	if e.Type != "dir" {
		t.Errorf("want type=dir, got %q", e.Type)
	}
	if e.Size != 0 {
		t.Errorf("want size=0 for dir, got %d", e.Size)
	}
	if e.Mode == "" {
		t.Error("mode should be set for dir")
	}
}

func TestRun_symlink(t *testing.T) {
	filePath, _, linkPath := makeFixture(t)
	res, perr := Run(&Args{Paths: []string{linkPath}}, nil)
	if perr != nil {
		t.Fatal(perr)
	}
	e := res.Entries[0]
	if e.Type != "symlink" {
		t.Errorf("want type=symlink, got %q", e.Type)
	}
	if e.LinkTarget != filePath {
		t.Errorf("want link_target=%q, got %q", filePath, e.LinkTarget)
	}
}

func TestRun_followSymlinks(t *testing.T) {
	filePath, _, linkPath := makeFixture(t)
	res, perr := Run(&Args{Paths: []string{linkPath}, FollowSymlinks: true}, nil)
	if perr != nil {
		t.Fatal(perr)
	}
	e := res.Entries[0]
	if e.Type != "file" {
		t.Errorf("want type=file after follow, got %q", e.Type)
	}
	if e.LinkTarget != filePath {
		t.Errorf("want link_target preserved, got %q", e.LinkTarget)
	}
	if e.Size != int64(len("package main\n")) {
		t.Errorf("want size=%d, got %d", len("package main\n"), e.Size)
	}
	if e.Error != "" {
		t.Errorf("want no error, got %q", e.Error)
	}
}

func TestRun_followSymlinks_broken(t *testing.T) {
	root := t.TempDir()
	linkPath := filepath.Join(root, "broken")
	if err := os.Symlink(filepath.Join(root, "nonexistent"), linkPath); err != nil {
		t.Fatal(err)
	}
	res, perr := Run(&Args{Paths: []string{linkPath}, FollowSymlinks: true}, nil)
	if perr != nil {
		t.Fatal(perr)
	}
	if res.Errors != 1 {
		t.Fatalf("want 1 error, got %d", res.Errors)
	}
	e := res.Entries[0]
	if e.Type != "symlink" {
		t.Errorf("want type=symlink for broken link, got %q", e.Type)
	}
	if e.Error != "broken_symlink" {
		t.Errorf("want error=broken_symlink, got %q", e.Error)
	}
	if e.LinkTarget == "" {
		t.Error("want link_target preserved for broken symlink")
	}
}

func TestParseArgs_followSymlinks(t *testing.T) {
	a, err := ParseArgs(map[string]any{"paths": "some/file.go", "follow_symlinks": "true"})
	if err != nil {
		t.Fatal(err)
	}
	if !a.FollowSymlinks {
		t.Error("want FollowSymlinks=true")
	}
}

func TestParseArgs_followSymlinks_default(t *testing.T) {
	a, err := ParseArgs(map[string]any{"paths": "some/file.go"})
	if err != nil {
		t.Fatal(err)
	}
	if a.FollowSymlinks {
		t.Error("want FollowSymlinks=false by default")
	}
}

func TestParseArgs_followSymlinks_badString(t *testing.T) {
	_, perr := ParseArgs(map[string]any{"paths": "f.go", "follow_symlinks": "maybe"})
	if perr == nil {
		t.Error("expected error for follow_symlinks=maybe")
	}
}

func TestRun_notFound(t *testing.T) {
	root := t.TempDir()
	res, perr := Run(&Args{Paths: []string{filepath.Join(root, "no_such_file")}}, nil)
	if perr != nil {
		t.Fatal(perr)
	}
	if res.Errors != 1 {
		t.Fatalf("want 1 error, got %d", res.Errors)
	}
	e := res.Entries[0]
	if e.Error != "not_found" {
		t.Errorf("want error=not_found, got %q", e.Error)
	}
	if e.Type != "" || e.Mtime != 0 {
		t.Error("type/mtime should be zero for missing path")
	}
}

func TestRun_bulk_mixed(t *testing.T) {
	filePath, dirPath, _ := makeFixture(t)
	missing := filepath.Join(t.TempDir(), "ghost")
	res, perr := Run(&Args{Paths: []string{filePath, dirPath, missing}}, nil)
	if perr != nil {
		t.Fatal(perr)
	}
	if res.Count != 3 {
		t.Fatalf("want count=3, got %d", res.Count)
	}
	if res.Errors != 1 {
		t.Fatalf("want 1 error, got %d", res.Errors)
	}
	if res.Entries[0].Type != "file" {
		t.Errorf("first entry should be file")
	}
	if res.Entries[1].Type != "dir" {
		t.Errorf("second entry should be dir")
	}
	if res.Entries[2].Error != "not_found" {
		t.Errorf("third entry should be not_found")
	}
}

func TestPrettyResponse_ok(t *testing.T) {
	filePath, _, _ := makeFixture(t)
	res, _ := Run(&Args{Paths: []string{filePath}}, nil)
	rsp := &proto.Response{OK: true, Data: res}
	out := PrettyResponse(nil, rsp)
	if !strings.Contains(out, "ash stat: 1 path(s)") {
		t.Errorf("unexpected header: %q", out)
	}
	if !strings.Contains(out, "F ") {
		t.Errorf("expected file type marker: %q", out)
	}
}

func TestPrettyResponse_withErrors(t *testing.T) {
	root := t.TempDir()
	res, _ := Run(&Args{Paths: []string{filepath.Join(root, "ghost")}}, nil)
	rsp := &proto.Response{OK: true, Data: res}
	out := PrettyResponse(nil, rsp)
	if !strings.Contains(out, "1 error(s)") {
		t.Errorf("expected error count in header: %q", out)
	}
	if !strings.Contains(out, "[not_found]") {
		t.Errorf("expected [not_found] marker: %q", out)
	}
}

func TestPrettyResponse_LeanDefaultDropsMtimeAndMode(t *testing.T) {
	filePath, _, _ := makeFixture(t)
	res, _ := Run(&Args{Paths: []string{filePath}}, nil)
	rsp := &proto.Response{OK: true, Data: res}
	out := PrettyResponse(&proto.Request{Verb: "stat", Args: map[string]any{}}, rsp)
	// Mode "0644" must not appear in lean rows (it would in full).
	if strings.Contains(out, "0644") {
		t.Errorf("lean rows must omit mode 0644:\n%s", out)
	}
	// Mtime renders as RFC3339Z; the trailing T<digits>:<digits>:<digits>Z
	// substring is enough to catch it.
	if strings.Contains(out, "Z\n") || strings.Contains(out, "Z ") {
		t.Errorf("lean rows must omit mtime:\n%s", out)
	}
	// Type marker still present.
	if !strings.Contains(out, "F ") {
		t.Errorf("expected file type marker:\n%s", out)
	}
}

func TestPrettyResponse_WithMetaIncludesMtimeAndMode(t *testing.T) {
	filePath, _, _ := makeFixture(t)
	res, _ := Run(&Args{Paths: []string{filePath}}, nil)
	rsp := &proto.Response{OK: true, Data: res}
	req := &proto.Request{Verb: "stat", Args: map[string]any{"with_meta": "true"}}
	out := PrettyResponse(req, rsp)
	if !strings.Contains(out, "0644") {
		t.Errorf("with_meta must include mode 0644:\n%s", out)
	}
}

func TestDecodeResult_roundtrip(t *testing.T) {
	filePath, dirPath, _ := makeFixture(t)
	typed, _ := Run(&Args{Paths: []string{filePath, dirPath}}, nil)

	// Simulate the msgpack loose-decode path (map[string]any).
	asMap := map[string]any{
		"count":  typed.Count,
		"errors": typed.Errors,
		"entries": []any{
			map[string]any{
				"path":  typed.Entries[0].Path,
				"type":  typed.Entries[0].Type,
				"size":  typed.Entries[0].Size,
				"mtime": typed.Entries[0].Mtime,
				"mode":  typed.Entries[0].Mode,
			},
			map[string]any{
				"path":  typed.Entries[1].Path,
				"type":  typed.Entries[1].Type,
				"mtime": typed.Entries[1].Mtime,
				"mode":  typed.Entries[1].Mode,
			},
		},
	}
	decoded, ok := decodeResult(asMap)
	if !ok {
		t.Fatal("decodeResult returned !ok")
	}
	if decoded.Count != 2 {
		t.Errorf("want count=2, got %d", decoded.Count)
	}
	if decoded.Entries[0].Type != "file" {
		t.Errorf("first entry type: want file, got %q", decoded.Entries[0].Type)
	}
	if decoded.Entries[1].Type != "dir" {
		t.Errorf("second entry type: want dir, got %q", decoded.Entries[1].Type)
	}
}
