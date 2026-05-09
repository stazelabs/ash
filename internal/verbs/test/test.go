// Package test implements the `ash test` verb.
//
// Args:
//
//	packages   string    (optional) - comma-separated package patterns; default "./..."
//	run        string    (optional) - regex passed to go test -run
//	count      int       (optional) - go test -count; default 1 to bypass cache
//	race       bool      (optional) - go test -race; default false
//	short      bool      (optional) - go test -short; default false
//	timeout    string    (optional) - duration (e.g. "60s", "10m"); default "60s". Also passed to
//	                                  go test -timeout (with a 1s grace deducted) so go itself
//	                                  aborts cleanly before the outer ctx fires.
//	verbose    bool      (optional) - render hint: include passing tests per package
//
// Result.OK mirrors `git diff` semantics: Metrics.OK=true means the verb ran;
// Result.OK=false means tests failed. A "tests failed" run is not a verb error.
//
// Multi-stack future: the public Result/Package/Test/Counts types are
// stack-agnostic; all Go-specific code (subprocess invocation, -json parsing,
// build-failure detection) lives in goDriver below. See ASH-40.
package test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/stazelabs/ash/internal/proto"
	"github.com/stazelabs/ash/internal/verbs/argutil"
)

const (
	defaultPackages = "./..."
	defaultCount    = 1
	defaultTimeout  = 60 * time.Second
	maxOutputBytes  = 16 << 20
	timeoutGrace    = 1 * time.Second
)

type Args struct {
	Packages string
	Run      string
	Count    int
	Race     bool
	Short    bool
	Timeout  time.Duration
	Verbose  bool
}

func ParseArgs(in map[string]any) (*Args, *proto.Error) {
	a := &Args{}
	var perr *proto.Error
	if a.Packages, perr = argutil.OptionalNonEmptyString(in, "packages", defaultPackages); perr != nil {
		return nil, perr
	}
	if a.Run, perr = argutil.OptionalString(in, "run", ""); perr != nil {
		return nil, perr
	}
	// count: 0 means "use go's cache", 1+ means "run N times"; we accept
	// any non-negative int. Default 1 (bypass cache) — agents typically
	// want fresh runs after editing code.
	count, perr := argutil.OptionalPosInt(in, "count", defaultCount, 1<<20)
	if perr != nil {
		// Allow 0 explicitly: re-parse with non-negative semantics.
		if v, ok := in["count"]; ok {
			if n, ok := argutil.ToInt(v); ok && n == 0 {
				a.Count = 0
			} else {
				return nil, perr
			}
		} else {
			return nil, perr
		}
	} else {
		a.Count = count
	}
	if a.Race, perr = argutil.OptionalBool(in, "race", false); perr != nil {
		return nil, perr
	}
	if a.Short, perr = argutil.OptionalBool(in, "short", false); perr != nil {
		return nil, perr
	}
	timeoutStr, perr := argutil.OptionalString(in, "timeout", defaultTimeout.String())
	if perr != nil {
		return nil, perr
	}
	d, err := time.ParseDuration(timeoutStr)
	if err != nil {
		return nil, &proto.Error{Code: "args", Msg: "timeout must be a Go duration (e.g. '60s', '10m'): " + err.Error()}
	}
	if d <= 0 {
		return nil, &proto.Error{Code: "args", Msg: "timeout must be positive"}
	}
	a.Timeout = d
	if a.Verbose, perr = argutil.OptionalBool(in, "verbose", false); perr != nil {
		return nil, perr
	}
	return a, nil
}

// Result is the wire envelope. OK is true iff every package passed (or
// was a no_tests). Total aggregates per-test counts across all packages.
// Truncated is set when stdout exceeded maxOutputBytes (rare).
type Result struct {
	OK        bool      `msgpack:"ok"`
	Total     Counts    `msgpack:"total"`
	Packages  []Package `msgpack:"packages,omitempty"`
	Elapsed   float64   `msgpack:"elapsed"`
	Truncated bool      `msgpack:"truncated,omitempty"`
}

// Package is one Go package's result. Status:
//   - "pass":         tests ran and all passed (or were skipped)
//   - "fail":         at least one test failed
//   - "skip":         every test in the package was skipped
//   - "build_failed": the package didn't compile; BuildOutput holds the error
//   - "no_tests":     `[no test files]` — counts as pass for OK aggregation
//   - "timeout":      go test wall hit the user's --timeout
type Package struct {
	Path        string  `msgpack:"path"`
	Status      string  `msgpack:"status"`
	Elapsed     float64 `msgpack:"elapsed,omitempty"`
	Counts      Counts  `msgpack:"counts"`
	Tests       []Test  `msgpack:"tests,omitempty"`
	BuildOutput string  `msgpack:"build_output,omitempty"`
}

type Test struct {
	Name    string  `msgpack:"name"`
	Status  string  `msgpack:"status"` // pass | fail | skip
	Elapsed float64 `msgpack:"elapsed,omitempty"`
	Output  string  `msgpack:"output,omitempty"`
	File    string  `msgpack:"file,omitempty"`
	Line    int     `msgpack:"line,omitempty"`
}

type Counts struct {
	Pass int `msgpack:"pass"`
	Fail int `msgpack:"fail"`
	Skip int `msgpack:"skip"`
}

// testEvent matches one line of `go test -json` output. Documented at
// `go doc cmd/test2json`. Time and bench-related fields are ignored.
type testEvent struct {
	Action  string  `json:"Action"`
	Package string  `json:"Package"`
	Test    string  `json:"Test"`
	Output  string  `json:"Output"`
	Elapsed float64 `json:"Elapsed"`
}

// Run is the verb entry point. Shells out to `go test -json`, streams the
// output through a JSON-line scanner, and aggregates events into a Result.
func Run(a *Args, tr *proto.Tracer) (*Result, *proto.Error) {
	d := goDriver{}
	return d.run(a, tr)
}

// goDriver is the Go-specific implementation. The struct is the seam for
// future stacks (see ASH-40); for now it has no fields.
type goDriver struct{}

func (goDriver) run(a *Args, tr *proto.Tracer) (*Result, *proto.Error) {
	ctx, cancel := context.WithTimeout(context.Background(), a.Timeout)
	defer cancel()

	args := []string{"test", "-json"}
	// Pass go a slightly tighter timeout so it self-aborts before our outer
	// ctx fires; that gives us a clean go-side panic message instead of a
	// SIGKILL.
	innerTimeout := a.Timeout - timeoutGrace
	if innerTimeout <= 0 {
		innerTimeout = a.Timeout
	}
	args = append(args, "-timeout", innerTimeout.String())
	if a.Count > 0 {
		args = append(args, fmt.Sprintf("-count=%d", a.Count))
	}
	if a.Race {
		args = append(args, "-race")
	}
	if a.Short {
		args = append(args, "-short")
	}
	if a.Run != "" {
		args = append(args, "-run", a.Run)
	}
	for _, p := range strings.Split(a.Packages, ",") {
		if s := strings.TrimSpace(p); s != "" {
			args = append(args, normalizePackagePattern(s))
		}
	}

	cmd := exec.CommandContext(ctx, "go", args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, &proto.Error{Code: "go_failed", Msg: "stdout pipe: " + err.Error()}
	}
	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	start := time.Now()
	if err := cmd.Start(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return nil, &proto.Error{Code: "go_not_found", Msg: "go binary not on PATH"}
		}
		return nil, &proto.Error{Code: "go_failed", Msg: "start: " + err.Error()}
	}

	events, scanErr, truncated := scanEvents(stdout, maxOutputBytes)
	waitErr := cmd.Wait()
	elapsed := time.Since(start)
	if tr != nil {
		tr.AddIO(elapsed)
	}

	// Outer-context timeout: ctx.Err() == DeadlineExceeded means we killed go.
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		res := aggregate(events, a.Verbose)
		res.Elapsed = elapsed.Seconds()
		res.Truncated = truncated
		// Mark every still-pending package as timeout so the agent sees
		// which package was hung. If the timeout fired between packages,
		// we just return what we have plus the explicit timeout error.
		return res, &proto.Error{Code: "timeout", Msg: fmt.Sprintf("go test exceeded timeout=%s", a.Timeout)}
	}

	if waitErr != nil {
		var exitErr *exec.ExitError
		if !errors.As(waitErr, &exitErr) {
			return nil, &proto.Error{Code: "go_failed", Msg: "wait: " + waitErr.Error()}
		}
		// Non-zero exit is normal for "tests failed" or "build failed";
		// fall through to aggregate. Anything go reports above exit 2 is
		// unusual but we still trust the JSON stream we collected.
	}
	if scanErr != nil && !errors.Is(scanErr, io.EOF) {
		// Can't decode the JSON stream — bad situation but report what
		// we have plus the parse error.
		res := aggregate(events, a.Verbose)
		res.Elapsed = elapsed.Seconds()
		res.Truncated = truncated
		return res, &proto.Error{Code: "parse", Msg: scanErr.Error()}
	}

	if len(events) == 0 {
		// Empty stream: usually "no packages matched" (go test exits 1
		// with stderr like "no Go files in ...") or a startup failure.
		stderr := strings.TrimSpace(stderrBuf.String())
		if stderr != "" {
			return nil, &proto.Error{Code: "no_packages", Msg: stderr}
		}
		return nil, &proto.Error{Code: "no_packages", Msg: "go test produced no events"}
	}

	res := aggregate(events, a.Verbose)
	res.Elapsed = elapsed.Seconds()
	res.Truncated = truncated
	return res, nil
}

// scanEvents reads JSON-lines from r until EOF or the byte cap. truncated
// is true iff we hit the cap.
func scanEvents(r io.Reader, cap int) ([]testEvent, error, bool) {
	var events []testEvent
	limited := &io.LimitedReader{R: r, N: int64(cap) + 1}
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
	bytesRead := 0
	truncated := false
	for scanner.Scan() {
		line := scanner.Bytes()
		bytesRead += len(line) + 1 // +1 for the newline
		if bytesRead > cap {
			truncated = true
			break
		}
		if len(line) == 0 {
			continue
		}
		var ev testEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			// One bad line shouldn't abort the whole run; surface as
			// parse error after the loop if anything else goes wrong,
			// but for now skip and continue.
			continue
		}
		events = append(events, ev)
	}
	if truncated {
		// Drain the rest so the producer doesn't block on a full pipe.
		_, _ = io.Copy(io.Discard, r)
	}
	if err := scanner.Err(); err != nil {
		return events, err, truncated
	}
	return events, nil, truncated
}

// fileLineRe matches the conventional Go test failure prefix:
//
//	    foo_test.go:42: error message
//
// The path may include a leading directory (subtest output sometimes
// has it). Anchored to start-of-line (multiline) so we pick the first
// real failure location, not later mentions in the message body.
var fileLineRe = regexp.MustCompile(`(?m)^\s*([\w./-]+_test\.go):(\d+):`)

func extractFileLine(output string) (string, int) {
	m := fileLineRe.FindStringSubmatch(output)
	if m == nil {
		return "", 0
	}
	line := 0
	fmt.Sscanf(m[2], "%d", &line)
	return m[1], line
}

// aggregate folds the event stream into a Result. Per Go test docs:
//   - "run" starts a test (or re-enters a paused one)
//   - "output" carries one line of output, attributed to the package and
//     optionally to a test
//   - "pass" / "fail" / "skip" finalize a test or a package (Test=="" => package)
//   - build failures show up as a package "fail" with no preceding test
//     "run" events; the compile error is in the package's accumulated output.
func aggregate(events []testEvent, verbose bool) *Result {
	type testAcc struct {
		test    Test
		outBuf  strings.Builder
		started bool
	}
	type pkgAcc struct {
		path     string
		tests    map[string]*testAcc
		order    []string // test-name order (insertion)
		outBuf   strings.Builder
		hadTests bool
		elapsed  float64
		status   string // set when pkg-level pass/fail/skip arrives
	}
	pkgs := map[string]*pkgAcc{}
	pkgOrder := []string{}
	getPkg := func(name string) *pkgAcc {
		if p, ok := pkgs[name]; ok {
			return p
		}
		p := &pkgAcc{path: name, tests: map[string]*testAcc{}}
		pkgs[name] = p
		pkgOrder = append(pkgOrder, name)
		return p
	}
	getTest := func(p *pkgAcc, name string) *testAcc {
		if t, ok := p.tests[name]; ok {
			return t
		}
		t := &testAcc{test: Test{Name: name}}
		p.tests[name] = t
		p.order = append(p.order, name)
		p.hadTests = true
		return t
	}

	for _, ev := range events {
		if ev.Package == "" {
			continue
		}
		p := getPkg(ev.Package)
		switch ev.Action {
		case "run":
			if ev.Test != "" {
				ta := getTest(p, ev.Test)
				ta.started = true
			}
		case "output":
			if ev.Test != "" {
				ta := getTest(p, ev.Test)
				ta.outBuf.WriteString(ev.Output)
			} else {
				p.outBuf.WriteString(ev.Output)
			}
		case "pass":
			if ev.Test != "" {
				ta := getTest(p, ev.Test)
				ta.test.Status = "pass"
				ta.test.Elapsed = ev.Elapsed
			} else {
				p.status = "pass"
				p.elapsed = ev.Elapsed
			}
		case "fail":
			if ev.Test != "" {
				ta := getTest(p, ev.Test)
				ta.test.Status = "fail"
				ta.test.Elapsed = ev.Elapsed
			} else {
				p.status = "fail"
				p.elapsed = ev.Elapsed
			}
		case "skip":
			if ev.Test != "" {
				ta := getTest(p, ev.Test)
				ta.test.Status = "skip"
				ta.test.Elapsed = ev.Elapsed
			} else {
				if p.status == "" {
					p.status = "skip"
				}
				p.elapsed = ev.Elapsed
			}
		}
	}

	res := &Result{OK: true}
	for _, name := range pkgOrder {
		p := pkgs[name]
		pkg := Package{
			Path:    p.path,
			Status:  p.status,
			Elapsed: p.elapsed,
		}
		if pkg.Status == "" {
			// Subtest events without a finalizing pkg event — treat as fail.
			pkg.Status = "fail"
		}
		// Build-failure detection: package-level fail with no test "run"
		// events. The compile error is in the package's output buffer.
		if pkg.Status == "fail" && !p.hadTests {
			pkg.Status = "build_failed"
			pkg.BuildOutput = strings.TrimSpace(p.outBuf.String())
			res.OK = false
			res.Packages = append(res.Packages, pkg)
			continue
		}
		// "no test files" lands as a pkg "skip" with no tests.
		if pkg.Status == "skip" && !p.hadTests {
			out := p.outBuf.String()
			if strings.Contains(out, "[no test files]") {
				pkg.Status = "no_tests"
			}
		}
		// Roll up per-test results.
		for _, tname := range p.order {
			ta := p.tests[tname]
			ta.test.Output = ta.outBuf.String()
			switch ta.test.Status {
			case "pass":
				pkg.Counts.Pass++
			case "fail":
				pkg.Counts.Fail++
			case "skip":
				pkg.Counts.Skip++
			}
			if ta.test.Status == "fail" {
				ta.test.File, ta.test.Line = extractFileLine(ta.test.Output)
				pkg.Tests = append(pkg.Tests, ta.test)
			} else if verbose {
				// Verbose: include passing/skipping tests but drop
				// captured Output to keep token cost reasonable. Failing
				// tests always carry full output.
				slim := ta.test
				slim.Output = ""
				pkg.Tests = append(pkg.Tests, slim)
			}
		}
		// Pkg-level OK: any fail counts down OK for the whole result.
		if pkg.Counts.Fail > 0 || pkg.Status == "fail" {
			res.OK = false
		}
		res.Total.Pass += pkg.Counts.Pass
		res.Total.Fail += pkg.Counts.Fail
		res.Total.Skip += pkg.Counts.Skip
		res.Packages = append(res.Packages, pkg)
	}

	// Sort: failures first (build_failed > fail > timeout), then alpha.
	sort.SliceStable(res.Packages, func(i, j int) bool {
		ri, rj := statusRank(res.Packages[i].Status), statusRank(res.Packages[j].Status)
		if ri != rj {
			return ri < rj
		}
		return res.Packages[i].Path < res.Packages[j].Path
	})
	return res
}

// normalizePackagePattern auto-prefixes bare directory patterns with "./".
// Without this, `go test internal/walker` fails with "package internal/walker
// is not in std" because go interprets unprefixed paths as stdlib imports.
// We leave already-prefixed paths and full import paths (containing ".") alone.
func normalizePackagePattern(p string) string {
	if p == "" {
		return p
	}
	if strings.HasPrefix(p, "./") || strings.HasPrefix(p, "../") || strings.HasPrefix(p, "/") {
		return p
	}
	// Full import paths (e.g. github.com/foo/bar) — the first path
	// segment contains a "." (domain). Distinguish from "internal/walker/..."
	// where the dots live inside the trailing "..." pattern, not the first
	// segment.
	first := p
	if i := strings.IndexByte(p, '/'); i >= 0 {
		first = p[:i]
	}
	if strings.Contains(first, ".") {
		return p
	}
	return "./" + p
}

func statusRank(s string) int {
	switch s {
	case "build_failed":
		return 0
	case "timeout":
		return 1
	case "fail":
		return 2
	case "skip":
		return 3
	case "no_tests":
		return 4
	case "pass":
		return 5
	}
	return 9
}

// PrettyResponse renders the canonical token-cheap form. Verbose mode
// adds per-test names + elapsed for non-failing packages.
func PrettyResponse(_ *proto.Request, rsp *proto.Response) string {
	if !rsp.OK {
		return proto.PrettyResponseHeader(rsp)
	}
	r, ok := decodeResult(rsp.Data)
	if !ok {
		return "ok\n<unrecognized test result>"
	}
	return prettyResult(r)
}

func prettyResult(r *Result) string {
	var b strings.Builder
	overall := "pass"
	if !r.OK {
		overall = "fail"
	}
	failingPkgs := 0
	for _, p := range r.Packages {
		if p.Status == "fail" || p.Status == "build_failed" || p.Status == "timeout" {
			failingPkgs++
		}
	}
	fmt.Fprintf(&b, "=== ash test: %d pkgs (%d pass, %d fail) — %.2fs [%s] ===\n",
		len(r.Packages), len(r.Packages)-failingPkgs, failingPkgs, r.Elapsed, overall)

	verbose := false
	for _, p := range r.Packages {
		// Verbose mode is a render hint we do not carry on the wire — but
		// aggregate() included passing tests when verbose=true, and when
		// it did, p.Tests has more than just failures.
		if (p.Status == "pass" || p.Status == "skip") && len(p.Tests) > 0 {
			verbose = true
			break
		}
	}

	var passing []string
	for _, p := range r.Packages {
		switch p.Status {
		case "build_failed":
			fmt.Fprintf(&b, "BUILD  %s\n", p.Path)
			if p.BuildOutput != "" {
				for _, line := range strings.Split(strings.TrimRight(p.BuildOutput, "\n"), "\n") {
					b.WriteString("  ")
					b.WriteString(line)
					b.WriteByte('\n')
				}
			}
		case "fail":
			fmt.Fprintf(&b, "FAIL  %s  (%d fail / %d pass / %d skip, %.2fs)\n",
				p.Path, p.Counts.Fail, p.Counts.Pass, p.Counts.Skip, p.Elapsed)
			for _, t := range p.Tests {
				if t.Status != "fail" {
					if verbose {
						fmt.Fprintf(&b, "  %s %s  %.2fs\n", strings.ToUpper(t.Status), t.Name, t.Elapsed)
					}
					continue
				}
				if t.File != "" {
					fmt.Fprintf(&b, "  %s  %s:%d\n", t.Name, t.File, t.Line)
				} else {
					fmt.Fprintf(&b, "  %s\n", t.Name)
				}
				out := strings.TrimSpace(t.Output)
				if out != "" {
					for _, line := range strings.Split(out, "\n") {
						b.WriteString("    ")
						b.WriteString(line)
						b.WriteByte('\n')
					}
				}
			}
		case "timeout":
			fmt.Fprintf(&b, "TIMEOUT  %s\n", p.Path)
		default:
			if verbose {
				fmt.Fprintf(&b, "%s  %s  (%d / %d / %d, %.2fs)\n",
					strings.ToUpper(p.Status), p.Path, p.Counts.Pass, p.Counts.Fail, p.Counts.Skip, p.Elapsed)
				for _, t := range p.Tests {
					fmt.Fprintf(&b, "  %s %s  %.2fs\n", strings.ToUpper(t.Status), t.Name, t.Elapsed)
				}
			} else {
				passing = append(passing, p.Path)
			}
		}
	}
	if !verbose && len(passing) > 0 {
		fmt.Fprintf(&b, "PASS (%d): %s\n", len(passing), strings.Join(passing, ", "))
	}
	return strings.TrimRight(b.String(), "\n")
}

func decodeResult(data any) (*Result, bool) {
	if r, ok := data.(*Result); ok {
		return r, true
	}
	m, ok := data.(map[string]any)
	if !ok {
		return nil, false
	}
	r := &Result{}
	if v, ok := m["ok"].(bool); ok {
		r.OK = v
	}
	if v, ok := m["truncated"].(bool); ok {
		r.Truncated = v
	}
	if v, ok := m["elapsed"].(float64); ok {
		r.Elapsed = v
	}
	if tm, ok := m["total"].(map[string]any); ok {
		r.Total = decodeCounts(tm)
	}
	if raw, ok := m["packages"].([]any); ok {
		for _, x := range raw {
			pm, ok := x.(map[string]any)
			if !ok {
				continue
			}
			r.Packages = append(r.Packages, decodePackage(pm))
		}
	}
	return r, true
}

func decodePackage(m map[string]any) Package {
	p := Package{}
	if v, ok := m["path"].(string); ok {
		p.Path = v
	}
	if v, ok := m["status"].(string); ok {
		p.Status = v
	}
	if v, ok := m["elapsed"].(float64); ok {
		p.Elapsed = v
	}
	if v, ok := m["build_output"].(string); ok {
		p.BuildOutput = v
	}
	if cm, ok := m["counts"].(map[string]any); ok {
		p.Counts = decodeCounts(cm)
	}
	if raw, ok := m["tests"].([]any); ok {
		for _, x := range raw {
			tm, ok := x.(map[string]any)
			if !ok {
				continue
			}
			p.Tests = append(p.Tests, decodeTest(tm))
		}
	}
	return p
}

func decodeTest(m map[string]any) Test {
	t := Test{}
	if v, ok := m["name"].(string); ok {
		t.Name = v
	}
	if v, ok := m["status"].(string); ok {
		t.Status = v
	}
	if v, ok := m["elapsed"].(float64); ok {
		t.Elapsed = v
	}
	if v, ok := m["output"].(string); ok {
		t.Output = v
	}
	if v, ok := m["file"].(string); ok {
		t.File = v
	}
	if v, ok := argutil.ToInt(m["line"]); ok {
		t.Line = v
	}
	return t
}

func decodeCounts(m map[string]any) Counts {
	c := Counts{}
	if v, ok := argutil.ToInt(m["pass"]); ok {
		c.Pass = v
	}
	if v, ok := argutil.ToInt(m["fail"]); ok {
		c.Fail = v
	}
	if v, ok := argutil.ToInt(m["skip"]); ok {
		c.Skip = v
	}
	return c
}

// Truncated is the wire-side flag for `verbs.Runner.Truncated`.
func Truncated(d any) bool {
	if r, ok := d.(*Result); ok {
		return r.Truncated
	}
	return false
}
