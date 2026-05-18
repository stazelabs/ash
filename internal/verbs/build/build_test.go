package build

import (
	"strings"
	"testing"
	"time"

	"github.com/stazelabs/ash/internal/proto"
)

func TestParseArgs_Defaults(t *testing.T) {
	a, perr := ParseArgs(map[string]any{})
	if perr != nil {
		t.Fatalf("ParseArgs: %v", perr)
	}
	if a.Packages != defaultPackages {
		t.Errorf("Packages = %q, want %q", a.Packages, defaultPackages)
	}
	if a.Timeout != defaultTimeout {
		t.Errorf("Timeout = %v, want %v", a.Timeout, defaultTimeout)
	}
	if a.Race {
		t.Errorf("Race should default to false")
	}
}

func TestParseArgs_AllFields(t *testing.T) {
	a, perr := ParseArgs(map[string]any{
		"packages": "./cmd/ash,./cmd/ashd",
		"tags":     "integration",
		"race":     true,
		"timeout":  "5m",
	})
	if perr != nil {
		t.Fatalf("ParseArgs: %v", perr)
	}
	if a.Packages != "./cmd/ash,./cmd/ashd" {
		t.Errorf("Packages = %q", a.Packages)
	}
	if a.Tags != "integration" {
		t.Errorf("Tags = %q", a.Tags)
	}
	if !a.Race {
		t.Errorf("Race should be true")
	}
	if a.Timeout != 5*time.Minute {
		t.Errorf("Timeout = %v, want 5m", a.Timeout)
	}
}

func TestParseArgs_BadTimeout(t *testing.T) {
	if _, perr := ParseArgs(map[string]any{"timeout": "banana"}); perr == nil {
		t.Error("ParseArgs should reject 'banana' as timeout")
	}
	if _, perr := ParseArgs(map[string]any{"timeout": "0s"}); perr == nil {
		t.Error("ParseArgs should reject zero timeout")
	}
	if _, perr := ParseArgs(map[string]any{"timeout": "-1s"}); perr == nil {
		t.Error("ParseArgs should reject negative timeout")
	}
}

// TestParseStderr_OnePackageOneError covers the headline shape: `# pkg`
// header + one file:line:col error line. The structured BuildError
// must extract the file, line, col, and the trailing message exactly.
func TestParseStderr_OnePackageOneError(t *testing.T) {
	in := "# github.com/stazelabs/ash/internal/verbs/build\n" +
		"./build.go:42:5: undefined: undefinedFoo\n"
	pkgs := parseStderr(in)
	if len(pkgs) != 1 {
		t.Fatalf("got %d pkgs, want 1: %+v", len(pkgs), pkgs)
	}
	p := pkgs[0]
	if p.Path != "github.com/stazelabs/ash/internal/verbs/build" {
		t.Errorf("Path = %q", p.Path)
	}
	if p.Status != "build_failed" {
		t.Errorf("Status = %q, want build_failed", p.Status)
	}
	if len(p.Errors) != 1 {
		t.Fatalf("got %d errors, want 1: %+v", len(p.Errors), p.Errors)
	}
	e := p.Errors[0]
	if e.File != "./build.go" || e.Line != 42 || e.Col != 5 {
		t.Errorf("got %+v, want File=./build.go Line=42 Col=5", e)
	}
	if e.Message != "undefined: undefinedFoo" {
		t.Errorf("Message = %q", e.Message)
	}
}

// TestParseStderr_MultiplePackagesMultipleErrors covers the typical
// real-world case: two packages, three errors, sorted predictably.
func TestParseStderr_MultiplePackagesMultipleErrors(t *testing.T) {
	in := "# pkg/two\n" +
		"./b.go:10:1: syntax error: unexpected }\n" +
		"./b.go:11:2: missing return\n" +
		"# pkg/one\n" +
		"./a.go:1:1: imported and not used: \"fmt\"\n"
	pkgs := parseStderr(in)
	if len(pkgs) != 2 {
		t.Fatalf("got %d pkgs, want 2", len(pkgs))
	}
	// Sort is alphabetical: pkg/one comes before pkg/two.
	if pkgs[0].Path != "pkg/one" || pkgs[1].Path != "pkg/two" {
		t.Errorf("packages not sorted: %q, %q", pkgs[0].Path, pkgs[1].Path)
	}
	if len(pkgs[1].Errors) != 2 {
		t.Fatalf("pkg/two errors = %d, want 2", len(pkgs[1].Errors))
	}
	// Within a package, sorted by (file, line, col).
	if pkgs[1].Errors[0].Line != 10 || pkgs[1].Errors[1].Line != 11 {
		t.Errorf("pkg/two errors not sorted by line: %+v", pkgs[1].Errors)
	}
}

// TestParseStderr_TestPackageHeader covers go test's build-failure
// header form: `# pkg [pkg.test]`. The .test suffix must be stripped
// so the package path stays canonical.
func TestParseStderr_TestPackageHeader(t *testing.T) {
	in := "# github.com/stazelabs/ash/internal/verbs/build [github.com/stazelabs/ash/internal/verbs/build.test]\n" +
		"./build_test.go:5:1: undefined: thing\n"
	pkgs := parseStderr(in)
	if len(pkgs) != 1 {
		t.Fatalf("got %d pkgs, want 1: %+v", len(pkgs), pkgs)
	}
	if pkgs[0].Path != "github.com/stazelabs/ash/internal/verbs/build" {
		t.Errorf("Path = %q (should strip [pkg.test] suffix)", pkgs[0].Path)
	}
}

// TestParseStderr_NoFileLineErrors covers lines that don't match the
// file:line:col pattern (cgo, linker, tool errors). They should still
// land in BuildOutput so the agent isn't blind.
func TestParseStderr_NoFileLineErrors(t *testing.T) {
	in := "# pkg/cgo\n" +
		"# runtime/cgo\n" +
		"clang: error: linker command failed with exit code 1\n"
	pkgs := parseStderr(in)
	// Two `#` headers create two packages even though the second was
	// emitted by cgo itself. Both should land cleanly.
	if len(pkgs) < 1 {
		t.Fatalf("got %d pkgs, want >= 1", len(pkgs))
	}
	// At least one package should carry the clang error in BuildOutput
	// (no structured errors parsed).
	var sawClang bool
	for _, p := range pkgs {
		if strings.Contains(p.BuildOutput, "clang: error") {
			sawClang = true
		}
		if len(p.Errors) != 0 {
			t.Errorf("pkg %q: unexpected structured errors from clang output: %+v", p.Path, p.Errors)
		}
	}
	if !sawClang {
		t.Errorf("expected clang error in some package's BuildOutput, got %+v", pkgs)
	}
}

// TestParseStderr_Empty: no output → no packages → caller treats as OK.
func TestParseStderr_Empty(t *testing.T) {
	if pkgs := parseStderr(""); pkgs != nil {
		t.Errorf("empty stderr should yield nil packages, got %+v", pkgs)
	}
	if pkgs := parseStderr("\n\n"); pkgs != nil {
		t.Errorf("whitespace-only stderr should yield nil packages, got %+v", pkgs)
	}
}

// TestParseStderr_LineWithoutCol covers the rare "file:line: message"
// form (no column) — must still produce a structured BuildError with
// Col=0 omitempty.
func TestParseStderr_LineWithoutCol(t *testing.T) {
	in := "# pkg/x\n./a.go:7: syntax error: ...\n"
	pkgs := parseStderr(in)
	if len(pkgs) != 1 || len(pkgs[0].Errors) != 1 {
		t.Fatalf("expected 1 pkg, 1 error: %+v", pkgs)
	}
	e := pkgs[0].Errors[0]
	if e.Line != 7 || e.Col != 0 {
		t.Errorf("got Line=%d Col=%d, want Line=7 Col=0", e.Line, e.Col)
	}
}

func TestTruncate(t *testing.T) {
	if s, tr := truncate("short", 100); s != "short" || tr {
		t.Errorf("truncate short: got %q,%v want unchanged,false", s, tr)
	}
	if s, tr := truncate("ten chars!", 5); s != "ten c" || !tr {
		t.Errorf("truncate over cap: got %q,%v want \"ten c\",true", s, tr)
	}
}

func TestNormalizePackagePattern(t *testing.T) {
	cases := map[string]string{
		"":                   "",
		"./...":              "./...",
		"./cmd/ash":          "./cmd/ash",
		"../foo":             "../foo",
		"/abs/path":          "/abs/path",
		"internal/walker":    "./internal/walker", // auto-prefix
		"cmd/ashd/...":       "./cmd/ashd/...",
		"github.com/foo/bar": "github.com/foo/bar", // dot in first segment → import path
	}
	for in, want := range cases {
		if got := normalizePackagePattern(in); got != want {
			t.Errorf("normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestPrettyResponse_Success: zero packages, OK=true → one-line header
// in "ok" mode.
func TestPrettyResponse_Success(t *testing.T) {
	r := &Result{OK: true, Elapsed: 1.23}
	rsp := &proto.Response{OK: true, Data: proto.MustData(r)}
	out := PrettyResponse(nil, rsp)
	if !strings.Contains(out, "§build") {
		t.Errorf("missing §build header: %q", out)
	}
	if !strings.Contains(out, "[ok]") {
		t.Errorf("expected [ok] marker: %q", out)
	}
	if !strings.Contains(out, "0 pkg(s) failed, 0 error(s)") {
		t.Errorf("expected zero-failure counts: %q", out)
	}
}

// TestPrettyResponse_BuildFailed renders both the per-package header
// and each BuildError on its own indented file:line:col line.
func TestPrettyResponse_BuildFailed(t *testing.T) {
	r := &Result{
		OK:      false,
		Elapsed: 0.42,
		Packages: []Package{{
			Path:   "pkg/x",
			Status: "build_failed",
			Errors: []BuildError{
				{File: "./a.go", Line: 10, Col: 5, Message: "undefined: foo"},
				{File: "./b.go", Line: 1, Message: "syntax error"},
			},
		}},
	}
	rsp := &proto.Response{OK: true, Data: proto.MustData(r)}
	out := PrettyResponse(nil, rsp)

	for _, want := range []string{
		"[fail]",
		"BUILD  pkg/x",
		"./a.go:10:5  undefined: foo",
		"./b.go:1  syntax error",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}
