package test

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stazelabs/ash/internal/jail"
	"github.com/stazelabs/ash/internal/proto"
)

func TestParseArgs_Defaults(t *testing.T) {
	a, perr := ParseArgs(map[string]any{})
	if perr != nil {
		t.Fatalf("unexpected: %v", perr)
	}
	if a.Packages != "./..." {
		t.Errorf("packages default: got %q", a.Packages)
	}
	if a.Count != 1 {
		t.Errorf("count default: got %d", a.Count)
	}
	if a.Timeout != 60*time.Second {
		t.Errorf("timeout default: got %s", a.Timeout)
	}
	if a.Race || a.Short || a.Verbose {
		t.Errorf("bool defaults wrong: race=%v short=%v verbose=%v", a.Race, a.Short, a.Verbose)
	}
}

func TestParseArgs_Overrides(t *testing.T) {
	a, perr := ParseArgs(map[string]any{
		"packages": "internal/walker,cmd/ashd",
		"run":      "TestFoo",
		"count":    "3",
		"race":     "true",
		"short":    "true",
		"timeout":  "5m",
		"verbose":  "true",
	})
	if perr != nil {
		t.Fatalf("unexpected: %v", perr)
	}
	if a.Packages != "internal/walker,cmd/ashd" || a.Run != "TestFoo" || a.Count != 3 ||
		!a.Race || !a.Short || !a.Verbose || a.Timeout != 5*time.Minute {
		t.Errorf("overrides not applied: %+v", a)
	}
}

func TestParseArgs_CountZeroAllowed(t *testing.T) {
	// count=0 means "use go's cache" — opt back into caching.
	a, perr := ParseArgs(map[string]any{"count": 0})
	if perr != nil {
		t.Fatalf("count=0 should be allowed: %v", perr)
	}
	if a.Count != 0 {
		t.Errorf("count: got %d", a.Count)
	}
}

func TestParseArgs_BadTimeout(t *testing.T) {
	_, perr := ParseArgs(map[string]any{"timeout": "bogus"})
	if perr == nil || perr.Code != "args" {
		t.Fatalf("expected args error, got %v", perr)
	}
}

func TestParseArgs_NegativeTimeout(t *testing.T) {
	_, perr := ParseArgs(map[string]any{"timeout": "-1s"})
	if perr == nil || perr.Code != "args" {
		t.Fatalf("expected args error, got %v", perr)
	}
}

func TestParseArgs_JailCheck_AbsolutePathDenied(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	jail.SetPolicy(jail.FromConfig(true, root, nil, nil))
	defer jail.SetPolicy(nil)

	_, perr := ParseArgs(map[string]any{"packages": filepath.Join(outside, "foo")})
	if perr == nil || perr.Code != "path_denied" {
		t.Fatalf("expected path_denied, got %v", perr)
	}
}

func TestParseArgs_JailCheck_InsideRootAllowed(t *testing.T) {
	root := t.TempDir()
	jail.SetPolicy(jail.FromConfig(true, root, nil, nil))
	defer jail.SetPolicy(nil)

	// Absolute path inside root — filesystem path that passes jail.
	insidePath := filepath.Join(root, "internal", "foo") + "/..."
	_, perr := ParseArgs(map[string]any{"packages": insidePath})
	if perr != nil {
		t.Fatalf("inside-root path should be allowed: %v", perr)
	}
}

func TestParseArgs_JailCheck_ImportPathAllowed(t *testing.T) {
	root := t.TempDir()
	jail.SetPolicy(jail.FromConfig(true, root, nil, nil))
	defer jail.SetPolicy(nil)

	// Go import path — no filesystem prefix, not checked against jail.
	_, perr := ParseArgs(map[string]any{"packages": "github.com/foo/bar"})
	if perr != nil {
		t.Fatalf("Go import path should not be jail-checked: %v", perr)
	}
}

func TestParseArgs_JailCheck_MixedListDenied(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	jail.SetPolicy(jail.FromConfig(true, root, nil, nil))
	defer jail.SetPolicy(nil)

	// Import path + absolute outside path — the outside path should be denied.
	packages := "github.com/foo/bar," + filepath.Join(outside, "foo")
	_, perr := ParseArgs(map[string]any{"packages": packages})
	if perr == nil || perr.Code != "path_denied" {
		t.Fatalf("expected path_denied for mixed list with outside path, got %v", perr)
	}
}

func TestExtractFileLine(t *testing.T) {
	cases := []struct {
		in   string
		file string
		line int
	}{
		{"    foo_test.go:42: expected 5 got 3\n", "foo_test.go", 42},
		{"=== RUN   TestX\n    pkg/sub_test.go:7: oops\n", "pkg/sub_test.go", 7},
		{"--- FAIL: TestX (0.00s)\n    deeply/nested/x_test.go:123: msg\n", "deeply/nested/x_test.go", 123},
		{"no test pattern here", "", 0},
		{"foo.go:42: not a _test.go file", "", 0},
	}
	for _, c := range cases {
		f, l := extractFileLine(c.in)
		if f != c.file || l != c.line {
			t.Errorf("extractFileLine(%q) = (%q, %d), want (%q, %d)", c.in, f, l, c.file, c.line)
		}
	}
}

// passEvents is the canonical -json event sequence for one test in pkg
// "foo" that passes.
func passEvents(pkg, name string) []testEvent {
	return []testEvent{
		{Action: "run", Package: pkg, Test: name},
		{Action: "output", Package: pkg, Test: name, Output: "=== RUN   " + name + "\n"},
		{Action: "output", Package: pkg, Test: name, Output: "--- PASS: " + name + " (0.00s)\n"},
		{Action: "pass", Package: pkg, Test: name, Elapsed: 0.001},
	}
}

func failEvents(pkg, name, file string, line int) []testEvent {
	return []testEvent{
		{Action: "run", Package: pkg, Test: name},
		{Action: "output", Package: pkg, Test: name, Output: "=== RUN   " + name + "\n"},
		{Action: "output", Package: pkg, Test: name, Output: "    " + file + ":" + itoa(line) + ": expected non-nil, got nil\n"},
		{Action: "output", Package: pkg, Test: name, Output: "--- FAIL: " + name + " (0.00s)\n"},
		{Action: "fail", Package: pkg, Test: name, Elapsed: 0.002},
	}
}

func itoa(i int) string { return strings.TrimLeft(strings.Repeat(" ", 0)+intToString(i), " ") }

func intToString(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

func TestAggregate_PassAndFail(t *testing.T) {
	var events []testEvent
	events = append(events, passEvents("foo", "TestA")...)
	events = append(events, failEvents("foo", "TestB", "foo_test.go", 42)...)
	events = append(events,
		testEvent{Action: "output", Package: "foo", Output: "FAIL\n"},
		testEvent{Action: "fail", Package: "foo", Elapsed: 0.05},
	)
	events = append(events, passEvents("bar", "TestC")...)
	events = append(events,
		testEvent{Action: "output", Package: "bar", Output: "ok  \tbar\t0.01s\n"},
		testEvent{Action: "pass", Package: "bar", Elapsed: 0.01},
	)

	r := aggregate(events, false)
	if r.OK {
		t.Error("OK should be false when any test fails")
	}
	if r.Total.Pass != 2 || r.Total.Fail != 1 {
		t.Errorf("totals wrong: %+v", r.Total)
	}
	// foo (fail) should sort before bar (pass)
	if len(r.Packages) != 2 {
		t.Fatalf("expected 2 packages, got %d", len(r.Packages))
	}
	if r.Packages[0].Path != "foo" || r.Packages[0].Status != "fail" {
		t.Errorf("first pkg: got %+v", r.Packages[0])
	}
	if r.Packages[1].Path != "bar" || r.Packages[1].Status != "pass" {
		t.Errorf("second pkg: got %+v", r.Packages[1])
	}
	// Default (non-verbose): only failing tests included
	if len(r.Packages[0].Tests) != 1 || r.Packages[0].Tests[0].Name != "TestB" {
		t.Errorf("expected only TestB in foo: %+v", r.Packages[0].Tests)
	}
	if r.Packages[0].Tests[0].File != "foo_test.go" || r.Packages[0].Tests[0].Line != 42 {
		t.Errorf("file:line not extracted: %+v", r.Packages[0].Tests[0])
	}
	if len(r.Packages[1].Tests) != 0 {
		t.Errorf("passing pkg should have no tests in non-verbose: %+v", r.Packages[1].Tests)
	}
}

func TestAggregate_Verbose(t *testing.T) {
	events := append([]testEvent{}, passEvents("foo", "TestA")...)
	events = append(events,
		testEvent{Action: "output", Package: "foo", Output: "ok foo\n"},
		testEvent{Action: "pass", Package: "foo", Elapsed: 0.01},
	)
	r := aggregate(events, true)
	if len(r.Packages) != 1 || len(r.Packages[0].Tests) != 1 {
		t.Fatalf("verbose: expected 1 test included, got %+v", r.Packages)
	}
	tn := r.Packages[0].Tests[0]
	if tn.Name != "TestA" || tn.Status != "pass" {
		t.Errorf("verbose pass test: %+v", tn)
	}
	// Verbose passes drop captured Output to keep tokens reasonable.
	if tn.Output != "" {
		t.Errorf("verbose passes should drop Output, got %q", tn.Output)
	}
}

func TestAggregate_BuildFailed(t *testing.T) {
	events := []testEvent{
		{Action: "output", Package: "foo", Output: "# foo\n"},
		{Action: "output", Package: "foo", Output: "foo.go:42:9: undefined: bar\n"},
		{Action: "output", Package: "foo", Output: "FAIL\tfoo [build failed]\n"},
		{Action: "fail", Package: "foo", Elapsed: 0},
	}
	r := aggregate(events, false)
	if r.OK {
		t.Error("OK should be false on build failure")
	}
	if len(r.Packages) != 1 {
		t.Fatalf("expected 1 pkg, got %d", len(r.Packages))
	}
	p := r.Packages[0]
	if p.Status != "build_failed" {
		t.Errorf("status: %q", p.Status)
	}
	if !strings.Contains(p.BuildOutput, "undefined: bar") {
		t.Errorf("build output missing compile error: %q", p.BuildOutput)
	}
}

func TestAggregate_NoTests(t *testing.T) {
	events := []testEvent{
		{Action: "output", Package: "foo", Output: "?   \tfoo\t[no test files]\n"},
		{Action: "skip", Package: "foo", Elapsed: 0},
	}
	r := aggregate(events, false)
	if !r.OK {
		t.Error("OK should remain true for no_tests packages")
	}
	if len(r.Packages) != 1 || r.Packages[0].Status != "no_tests" {
		t.Errorf("expected no_tests status, got %+v", r.Packages)
	}
}

func TestAggregate_Skip(t *testing.T) {
	events := []testEvent{
		{Action: "run", Package: "foo", Test: "TestX"},
		{Action: "output", Package: "foo", Test: "TestX", Output: "--- SKIP: TestX (0.00s)\n"},
		{Action: "skip", Package: "foo", Test: "TestX", Elapsed: 0},
		{Action: "output", Package: "foo", Output: "ok foo\n"},
		{Action: "pass", Package: "foo", Elapsed: 0.01},
	}
	r := aggregate(events, false)
	if !r.OK {
		t.Error("all-skip pkg should keep OK true")
	}
	if r.Total.Skip != 1 {
		t.Errorf("expected 1 skip in totals, got %+v", r.Total)
	}
}

func TestPretty_Default(t *testing.T) {
	r := &Result{
		OK:      false,
		Total:   Counts{Pass: 5, Fail: 1, Skip: 0},
		Elapsed: 0.123,
		Packages: []Package{
			{
				Path:    "internal/walker",
				Status:  "fail",
				Elapsed: 0.07,
				Counts:  Counts{Pass: 5, Fail: 1},
				Tests: []Test{
					{Name: "TestCacheHit", Status: "fail", File: "walker_test.go", Line: 42, Output: "    walker_test.go:42: expected non-nil, got nil"},
				},
			},
			{Path: "cmd/ashd", Status: "pass", Counts: Counts{Pass: 4}},
			{Path: "internal/diff", Status: "pass", Counts: Counts{Pass: 8}},
		},
	}
	out := prettyResult(r)
	for _, want := range []string{
		"3 pkgs (2 pass, 1 fail)",
		"FAIL  internal/walker",
		"TestCacheHit  walker_test.go:42",
		"expected non-nil, got nil",
		"PASS (2): cmd/ashd, internal/diff",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("pretty output missing %q\nactual:\n%s", want, out)
		}
	}
}

func TestPretty_BuildFailed(t *testing.T) {
	r := &Result{
		OK:      false,
		Elapsed: 0.3,
		Packages: []Package{
			{Path: "internal/foo", Status: "build_failed", BuildOutput: "foo.go:42:9: undefined: bar"},
		},
	}
	out := prettyResult(r)
	for _, want := range []string{
		"BUILD  internal/foo",
		"foo.go:42:9: undefined: bar",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("pretty output missing %q\nactual:\n%s", want, out)
		}
	}
}

// Sanity: the verb hooks into proto.Tracer correctly when passed nil.
func TestNormalizePackagePattern(t *testing.T) {
	cases := []struct{ in, want string }{
		{"./...", "./..."},
		{"internal/walker", "./internal/walker"},
		{"internal/walker/...", "./internal/walker/..."},
		{"./internal/walker", "./internal/walker"},
		{"../foo", "../foo"},
		{"/abs/path", "/abs/path"},
		{"github.com/foo/bar", "github.com/foo/bar"},
		{"", ""},
	}
	for _, c := range cases {
		if got := normalizePackagePattern(c.in); got != c.want {
			t.Errorf("normalizePackagePattern(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestRun_NilTracerSafe(t *testing.T) {
	// Don't actually shell out — just confirm ParseArgs + nil tracer
	// don't blow up trivially. Real e2e runs in cmd/ash smoke tests.
	a, perr := ParseArgs(map[string]any{"timeout": "1ms"})
	if perr != nil {
		t.Fatal(perr)
	}
	if a.Timeout != time.Millisecond {
		t.Errorf("timeout: got %s", a.Timeout)
	}
	_ = (*proto.Tracer)(nil)
}

func TestParseArgs_BenchFlags(t *testing.T) {
	a, perr := ParseArgs(map[string]any{
		"bench":     "BenchmarkFoo",
		"benchmem":  "true",
		"benchtime": "100x",
	})
	if perr != nil {
		t.Fatalf("unexpected: %v", perr)
	}
	if a.Bench != "BenchmarkFoo" {
		t.Errorf("bench: got %q", a.Bench)
	}
	if !a.BenchMem {
		t.Errorf("benchmem: expected true")
	}
	if a.BenchTime != "100x" {
		t.Errorf("benchtime: got %q", a.BenchTime)
	}
}

func TestParseArgs_BenchDefaults(t *testing.T) {
	a, perr := ParseArgs(map[string]any{})
	if perr != nil {
		t.Fatalf("unexpected: %v", perr)
	}
	if a.Bench != "" || a.BenchMem || a.BenchTime != "" {
		t.Errorf("bench defaults wrong: bench=%q benchmem=%v benchtime=%q", a.Bench, a.BenchMem, a.BenchTime)
	}
}

func TestRun_BenchIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test — shells out to go test -bench")
	}
	a, perr := ParseArgs(map[string]any{
		"packages":  "github.com/stazelabs/ash/cmd/ash",
		"bench":     "BenchmarkHookDecide",
		"benchtime": "1x",
	})
	if perr != nil {
		t.Fatalf("ParseArgs: %v", perr)
	}
	res, werr := Run(a, nil, "")
	if werr != nil {
		t.Fatalf("Run: %v", werr)
	}
	var found bool
	for _, pkg := range res.Packages {
		for _, line := range pkg.BenchOutput {
			if strings.Contains(line, "BenchmarkHookDecide") {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("BenchmarkHookDecide line not found in bench output; packages: %+v", res.Packages)
	}
}

func TestParseArgs_Cover(t *testing.T) {
	a, perr := ParseArgs(map[string]any{"cover": "true"})
	if perr != nil {
		t.Fatalf("unexpected: %v", perr)
	}
	if !a.Cover {
		t.Errorf("cover: expected true")
	}
}

func TestParseArgs_CoverDefault(t *testing.T) {
	a, perr := ParseArgs(map[string]any{})
	if perr != nil {
		t.Fatalf("unexpected: %v", perr)
	}
	if a.Cover {
		t.Errorf("cover default: expected false")
	}
}

func TestExtractCoverage(t *testing.T) {
	cases := []struct {
		in   string
		want float64
		ok   bool
	}{
		{"ok  \tfoo\t0.05s\tcoverage: 87.3% of statements\n", 87.3, true},
		{"coverage: 100.0% of statements", 100.0, true},
		{"coverage: 0.0% of statements", 0.0, true},
		{"coverage: 65% of statements", 65.0, true}, // integer form just in case
		{"coverage: [no statements]\n", 0, false},
		{"ok  \tfoo\t0.05s\n", 0, false},
		{"", 0, false},
	}
	for _, c := range cases {
		got, ok := extractCoverage(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("extractCoverage(%q) = (%v, %v), want (%v, %v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestAggregate_Coverage(t *testing.T) {
	// Pass + cover line on package output → Coverage populated.
	events := append([]testEvent{}, passEvents("foo", "TestA")...)
	events = append(events,
		testEvent{Action: "output", Package: "foo", Output: "PASS\n"},
		testEvent{Action: "output", Package: "foo", Output: "coverage: 87.3% of statements\n"},
		testEvent{Action: "output", Package: "foo", Output: "ok  \tfoo\t0.01s\tcoverage: 87.3% of statements\n"},
		testEvent{Action: "pass", Package: "foo", Elapsed: 0.01},
	)
	// "[no statements]" package — Coverage stays nil.
	events = append(events,
		testEvent{Action: "output", Package: "bar", Output: "ok  \tbar\t0.00s\t[no statements]\n"},
		testEvent{Action: "pass", Package: "bar", Elapsed: 0.0},
	)
	r := aggregate(events, false)
	if !r.OK {
		t.Fatalf("OK should be true: %+v", r)
	}
	if len(r.Packages) != 2 {
		t.Fatalf("expected 2 packages, got %d", len(r.Packages))
	}
	// Pkgs are sorted pass-first then alpha; both pass here.
	var foo, bar *Package
	for i := range r.Packages {
		switch r.Packages[i].Path {
		case "foo":
			foo = &r.Packages[i]
		case "bar":
			bar = &r.Packages[i]
		}
	}
	if foo == nil || bar == nil {
		t.Fatalf("missing pkgs: foo=%v bar=%v", foo, bar)
	}
	if foo.Coverage == nil || *foo.Coverage != 87.3 {
		t.Errorf("foo coverage: got %v, want 87.3", foo.Coverage)
	}
	if bar.Coverage != nil {
		t.Errorf("bar (no statements) coverage: got %v, want nil", bar.Coverage)
	}
}

func TestAggregate_CoverageNotAttachedOnFail(t *testing.T) {
	// Failing pkg with a coverage line should not get Coverage attached —
	// coverage of a failed run isn't meaningful to surface.
	events := append([]testEvent{}, failEvents("foo", "TestB", "foo_test.go", 1)...)
	events = append(events,
		testEvent{Action: "output", Package: "foo", Output: "FAIL\n"},
		testEvent{Action: "output", Package: "foo", Output: "coverage: 40.0% of statements\n"},
		testEvent{Action: "fail", Package: "foo", Elapsed: 0.01},
	)
	r := aggregate(events, false)
	if r.Packages[0].Coverage != nil {
		t.Errorf("failing pkg should not carry Coverage, got %v", r.Packages[0].Coverage)
	}
}

func TestPretty_Coverage(t *testing.T) {
	cov1 := 87.3
	cov2 := 100.0
	r := &Result{
		OK:      true,
		Total:   Counts{Pass: 9, Fail: 0, Skip: 0},
		Elapsed: 0.1,
		Packages: []Package{
			{Path: "internal/foo", Status: "pass", Counts: Counts{Pass: 5}, Coverage: &cov1},
			{Path: "internal/bar", Status: "pass", Counts: Counts{Pass: 4}, Coverage: &cov2},
			{Path: "internal/baz", Status: "no_tests"}, // no coverage, listed by path
		},
	}
	out := prettyResult(r)
	for _, want := range []string{
		"PASS  internal/foo  87.3%",
		"PASS  internal/bar  100.0%",
		"NO_TESTS  internal/baz",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("pretty output missing %q\nactual:\n%s", want, out)
		}
	}
	// The terse "PASS (N): pkg, pkg" tail must NOT fire when coverage mode
	// is on — otherwise the per-pkg numbers get duplicated by the summary.
	if strings.Contains(out, "PASS (") {
		t.Errorf("terse PASS (N): tail should be suppressed in coverage mode, got:\n%s", out)
	}
}

func TestExtractBenchLines(t *testing.T) {
	// Result rows have tab-separated metrics; preamble rows do not.
	input := "goos: darwin\ngoarch: arm64\nBenchmarkFoo\nBenchmarkFoo-8\t1000000\t1234 ns/op\nBenchmarkBar/sub\nBenchmarkBar/sub-8\t500000\t2000 ns/op\t0 B/op\t0 allocs/op\nok  \tfoo\t1.234s\n"
	lines := extractBenchLines(input)
	if len(lines) != 2 {
		t.Fatalf("expected 2 bench lines (not preambles), got %d: %v", len(lines), lines)
	}
	if !strings.Contains(lines[0], "BenchmarkFoo-8") {
		t.Errorf("lines[0]: %q", lines[0])
	}
	if !strings.Contains(lines[1], "BenchmarkBar/sub-8") {
		t.Errorf("lines[1]: %q", lines[1])
	}
}
