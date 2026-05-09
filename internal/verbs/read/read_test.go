package read

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stazelabs/ash/internal/proto"
)

func TestApplyRange_Lines(t *testing.T) {
	body := []byte("alpha\nbeta\ngamma\ndelta\nepsilon\n")
	cases := []struct {
		name           string
		spec           string
		want           string
		wantRangeBack  string
	}{
		{"first line", "1:1", "alpha\n", "1:1"},
		{"middle range", "2:3", "beta\ngamma\n", "2:3"},
		{"last line", "5:5", "epsilon\n", "5:5"},
		{"clamp end", "3:100", "gamma\ndelta\nepsilon\n", "3:100"},
		{"all lines", "1:5", "alpha\nbeta\ngamma\ndelta\nepsilon\n", "1:5"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, gotRange, perr := applyRange(body, c.spec, "lines")
			if perr != nil {
				t.Fatalf("unexpected error: %+v", perr)
			}
			if string(got) != c.want {
				t.Errorf("body mismatch\n got: %q\nwant: %q", got, c.want)
			}
			if gotRange != c.wantRangeBack {
				t.Errorf("range_returned: got %q want %q", gotRange, c.wantRangeBack)
			}
		})
	}
}

func TestApplyRange_LinesNoTrailingNewline(t *testing.T) {
	body := []byte("alpha\nbeta\ngamma")
	got, _, perr := applyRange(body, "3:3", "lines")
	if perr != nil {
		t.Fatalf("unexpected error: %+v", perr)
	}
	if string(got) != "gamma" {
		t.Errorf("last line without newline: got %q want %q", got, "gamma")
	}
}

func TestApplyRange_LinesPastEnd(t *testing.T) {
	body := []byte("alpha\nbeta\ngamma\n")
	_, _, perr := applyRange(body, "10:20", "lines")
	if perr == nil || perr.Code != "range_out_of_bounds" {
		t.Fatalf("expected range_out_of_bounds error, got %+v", perr)
	}
}

func TestApplyRange_Bytes(t *testing.T) {
	body := []byte("0123456789")
	cases := []struct {
		name          string
		spec          string
		want          string
		wantRangeBack string
	}{
		{"middle", "3:7", "23456", "3:7"},
		{"first byte", "1:1", "0", "1:1"},
		{"last byte", "10:10", "9", "10:10"},
		{"clamp end", "5:1000", "456789", "5:10"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, gotRange, perr := applyRange(body, c.spec, "bytes")
			if perr != nil {
				t.Fatalf("unexpected error: %+v", perr)
			}
			if string(got) != c.want {
				t.Errorf("body mismatch\n got: %q\nwant: %q", got, c.want)
			}
			if gotRange != c.wantRangeBack {
				t.Errorf("range_returned: got %q want %q", gotRange, c.wantRangeBack)
			}
		})
	}
}

func TestApplyRange_BytesPastEnd(t *testing.T) {
	body := []byte("0123456789")
	_, _, perr := applyRange(body, "100:200", "bytes")
	if perr == nil || perr.Code != "range_out_of_bounds" {
		t.Fatalf("expected range_out_of_bounds error, got %+v", perr)
	}
}

func TestApplyRange_InvalidArgs(t *testing.T) {
	body := []byte("a\nb\nc\n")
	cases := []struct {
		name string
		spec string
	}{
		{"missing colon", "abc"},
		{"non-int start", "x:5"},
		{"non-int end", "1:x"},
		{"start lt 1", "0:3"},
		{"end lt start", "3:1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, _, perr := applyRange(body, c.spec, "lines"); perr == nil {
				t.Errorf("expected error for spec %q", c.spec)
			}
		})
	}
}

func TestRun_TruncationHint(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.txt")
	body := strings.Repeat("a", 10000)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	res, perr := Run(&Args{Path: path, LimitBytes: 100, RangeKind: "lines"}, nil)
	if perr != nil {
		t.Fatalf("unexpected error: %+v", perr)
	}
	if !res.Truncated {
		t.Fatal("expected truncated=true")
	}
	if len(res.Content) != 100 {
		t.Errorf("content len: got %d want 100", len(res.Content))
	}
	if !strings.Contains(res.TruncationHint, "limit_bytes=100") {
		t.Errorf("hint missing limit_bytes=100: %q", res.TruncationHint)
	}
}

func TestRun_NotFound(t *testing.T) {
	_, perr := Run(&Args{Path: "/no/such/path/here", LimitBytes: 1024, RangeKind: "lines"}, nil)
	if perr == nil || perr.Code != "not_found" {
		t.Fatalf("expected not_found error, got %+v", perr)
	}
}

func TestRun_DirIsError(t *testing.T) {
	dir := t.TempDir()
	_, perr := Run(&Args{Path: dir, LimitBytes: 1024, RangeKind: "lines"}, nil)
	if perr == nil || perr.Code != "is_dir" {
		t.Fatalf("expected is_dir error, got %+v", perr)
	}
}

// TestParseArgs_WireShape verifies that the limit_bytes int arg accepts a
// string-typed value (the wire shape from the CLI's parseFlags) and rejects
// garbage. Guards against a future implementation skipping argutil and
// silently breaking the string→int coercion path.
func TestParseArgs_WireShape(t *testing.T) {
	a, perr := ParseArgs(map[string]any{"path": "f.go", "limit_bytes": "2048"})
	if perr != nil {
		t.Fatalf("string limit_bytes rejected: %v", perr)
	}
	if a.LimitBytes != 2048 {
		t.Errorf("limit_bytes: got %d, want 2048", a.LimitBytes)
	}
	_, perr = ParseArgs(map[string]any{"path": "f.go", "limit_bytes": "abc"})
	if perr == nil {
		t.Error("expected error for limit_bytes=abc")
	}
}

func TestPrettyResponse_LeanDefault(t *testing.T) {
	rsp := &proto.Response{
		OK: true,
		Data: &Result{
			Path:     "foo.go",
			Size:     1024,
			Lines:    42,
			Encoding: "utf-8",
			Mtime:    1714946747000000000,
			Content:  "package foo\n",
		},
	}
	got := PrettyResponse(&proto.Request{Verb: "read", Args: map[string]any{}}, rsp)
	if !strings.HasPrefix(got, "=== foo.go [1024B, 42L] ===\n") {
		t.Errorf("lean default header mismatch:\ngot %q", got)
	}
	if strings.Contains(got, "mtime=") {
		t.Error("lean default should not include mtime")
	}
	if strings.Contains(got, "utf-8") {
		t.Error("lean default should not include utf-8 encoding")
	}
}

func TestPrettyResponse_LeanSurfacesBase64(t *testing.T) {
	rsp := &proto.Response{
		OK: true,
		Data: &Result{
			Path:     "img.png",
			Size:     5000,
			Encoding: "base64",
			Mtime:    1714946747000000000,
			Content:  "iVBORw0KGgo=",
		},
	}
	got := PrettyResponse(&proto.Request{Verb: "read", Args: map[string]any{}}, rsp)
	if !strings.Contains(got, "encoding=base64") {
		t.Errorf("expected encoding=base64 in lean output, got %q", got)
	}
	if strings.Contains(got, "mtime=") {
		t.Error("lean should still drop mtime")
	}
}

func TestPrettyResponse_WithMetaFull(t *testing.T) {
	rsp := &proto.Response{
		OK: true,
		Data: &Result{
			Path:     "foo.go",
			Size:     1024,
			Lines:    42,
			Encoding: "utf-8",
			Mtime:    1714946747000000000,
			Content:  "package foo\n",
		},
	}
	req := &proto.Request{Verb: "read", Args: map[string]any{"with_meta": "true"}}
	got := PrettyResponse(req, rsp)
	if !strings.Contains(got, "utf-8") {
		t.Errorf("with_meta should include encoding, got %q", got)
	}
	if !strings.Contains(got, "mtime=") {
		t.Errorf("with_meta should include mtime, got %q", got)
	}
}
