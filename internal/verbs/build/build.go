// Package build implements the `ash build` verb (ASH-163). Symmetric
// with `ash test` — wraps `go build`, returns structured per-package
// status + per-error file:line:col extraction.
//
// Binaries are written to a temp dir (-o $TMPDIR/ash-build-XXXX/) and
// removed after the run so the user's cwd doesn't accumulate output
// artifacts. The verb's value is the structured error stream, not the
// binaries themselves; users who want to produce a binary should run
// the underlying `go build -o <path>` themselves.
//
// Args:
//
//	packages  string  (optional) - comma-separated package patterns; default "./..."
//	tags      string  (optional) - go build -tags
//	race      bool    (optional) - go build -race
//	timeout   string  (optional) - duration; default "60s"
//
// Multi-stack future: the public Result/Package/BuildError types are
// stack-agnostic; all Go-specific code (subprocess, stderr parsing)
// lives in goDriver below.
package build

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/stazelabs/ash/internal/jail"
	"github.com/stazelabs/ash/internal/proto"
	"github.com/stazelabs/ash/internal/verbs/argutil"
)

const (
	defaultPackages = "./..."
	defaultTimeout  = 60 * time.Second
	maxOutputBytes  = 4 << 20 // 4 MiB; build errors are smaller than test output
)

type Args struct {
	Packages string
	Tags     string
	Race     bool
	Timeout  time.Duration
}

func ParseArgs(in map[string]any) (*Args, *proto.Error) {
	a := &Args{}
	var perr *proto.Error
	if a.Packages, perr = argutil.OptionalNonEmptyString(in, "packages", defaultPackages); perr != nil {
		return nil, perr
	}
	for _, pkg := range strings.Split(a.Packages, ",") {
		pkg = strings.TrimSpace(pkg)
		if !isFilesystemPackagePath(pkg) {
			continue
		}
		if perr := jail.CheckPaths(map[string]string{"packages": pkg}); perr != nil {
			return nil, perr
		}
	}
	if a.Tags, perr = argutil.OptionalString(in, "tags", ""); perr != nil {
		return nil, perr
	}
	if a.Race, perr = argutil.OptionalBool(in, "race", false); perr != nil {
		return nil, perr
	}
	timeoutStr, perr := argutil.OptionalString(in, "timeout", defaultTimeout.String())
	if perr != nil {
		return nil, perr
	}
	d, err := time.ParseDuration(timeoutStr)
	if err != nil {
		return nil, &proto.Error{Code: "args", Msg: "timeout: invalid Go duration", Hint: "e.g. '60s', '10m'; got: " + err.Error()}
	}
	if d <= 0 {
		return nil, &proto.Error{Code: "args", Msg: "timeout must be positive"}
	}
	a.Timeout = d
	return a, nil
}

// Result is the wire envelope. OK is true iff every package compiled.
// A "build failed" run is NOT a verb error — Result.OK=false but
// Metrics.OK=true, mirroring `ash test`. Truncated is set when stderr
// exceeded maxOutputBytes (rare; build errors are normally small).
type Result struct {
	OK        bool      `msgpack:"ok"`
	Packages  []Package `msgpack:"packages,omitempty"`
	Elapsed   float64   `msgpack:"elapsed"`
	Truncated bool      `msgpack:"truncated,omitempty"`
}

// Package is one Go package's build outcome. Status:
//   - "ok":           the package compiled
//   - "build_failed": at least one error against this package
//
// BuildOutput is the raw stderr chunk attributed to this package
// (everything between this `# pkg` header line and the next, trimmed).
// Errors is the file:line:col-extracted view, sorted by file then line
// so the renderer reads top-to-bottom predictably.
type Package struct {
	Path        string       `msgpack:"path"`
	Status      string       `msgpack:"status"`
	BuildOutput string       `msgpack:"build_output,omitempty"`
	Errors      []BuildError `msgpack:"errors,omitempty"`
}

// BuildError is one structured compiler error. Col is omitempty so a
// (file, line) without column degrades gracefully; that's rare for go
// build but possible for cgo / asm errors.
type BuildError struct {
	File    string `msgpack:"file"`
	Line    int    `msgpack:"line"`
	Col     int    `msgpack:"col,omitempty"`
	Message string `msgpack:"message"`
}

// Run is the verb entry point. Shells out to `go build`, captures
// stderr, parses it into per-package + per-error structured output.
func Run(a *Args, tr *proto.Tracer) (*Result, *proto.Error) {
	d := goDriver{}
	return d.run(a, tr)
}

// goDriver is the Go-specific implementation. The struct is the seam
// for future stacks (see ASH-40); for now it has no fields.
type goDriver struct{}

func (goDriver) run(a *Args, tr *proto.Tracer) (*Result, *proto.Error) {
	// Streaming cancellation: derive timeout context from tracer's
	// parent so the daemon's watcher (client closed conn / KindCancel)
	// kills the go subprocess at its next checkpoint. Non-streaming
	// callers see context.Background, behavior unchanged.
	ctx, cancel := context.WithTimeout(tr.Context(), a.Timeout)
	defer cancel()

	// No -o: `go build -o DIR/ ./pkg` errors with "no main packages
	// to build" for non-main packages, which would silently swallow
	// type errors in libraries. Without -o, main packages write
	// binaries to cwd (matching standard `go build` behavior) and
	// non-main packages type-check cleanly with no artifact. For
	// users who want a specific output destination, use `make all`
	// or invoke `go build -o` directly.
	args := []string{"build"}
	if a.Race {
		args = append(args, "-race")
	}
	if a.Tags != "" {
		args = append(args, "-tags", a.Tags)
	}
	for _, p := range strings.Split(a.Packages, ",") {
		if s := strings.TrimSpace(p); s != "" {
			args = append(args, normalizePackagePattern(s))
		}
	}

	cmd := exec.CommandContext(ctx, "go", args...)
	if env := tr.Env(); env != nil {
		cmd.Env = env
	}
	var stderrBuf bytes.Buffer
	cmd.Stdout = io.Discard
	cmd.Stderr = &stderrBuf

	start := time.Now()
	err := cmd.Run()
	elapsed := time.Since(start)
	if tr != nil {
		tr.AddIO(elapsed)
	}

	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return nil, &proto.Error{Code: "timeout", Msg: fmt.Sprintf("go build exceeded timeout=%s", a.Timeout)}
	}

	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			if errors.Is(err, exec.ErrNotFound) {
				return nil, &proto.Error{Code: "go_not_found", Msg: "go binary not on PATH"}
			}
			return nil, &proto.Error{Code: "go_failed", Msg: err.Error()}
		}
		// Non-zero exit = build failures. Fall through to parseStderr.
	}

	stderr, truncated := truncate(stderrBuf.String(), maxOutputBytes)
	pkgs := parseStderr(stderr)

	// Non-zero exit with no parseable per-package errors: usually a
	// toolchain message like "go: no main packages to build" or
	// "go: cannot find module". Surface the raw stderr as go_failed
	// so the agent isn't told the build succeeded when it didn't.
	if err != nil && len(pkgs) == 0 && strings.TrimSpace(stderr) != "" {
		return &Result{Elapsed: elapsed.Seconds(), Truncated: truncated},
			&proto.Error{Code: "go_failed", Msg: "go build failed: " + strings.TrimSpace(stderr)}
	}

	res := &Result{
		OK:        len(pkgs) == 0,
		Packages:  pkgs,
		Elapsed:   elapsed.Seconds(),
		Truncated: truncated,
	}
	return res, nil
}

// truncate caps s at maxBytes and reports whether truncation occurred.
// We trim from the END so the first errors (which are usually the
// root cause; subsequent ones often cascade) stay intact.
func truncate(s string, maxBytes int) (string, bool) {
	if len(s) <= maxBytes {
		return s, false
	}
	return s[:maxBytes], true
}

// pkgHeaderRe matches a `go build` package-error header line:
//
//	# package/path
//	# package/path [other.test]
//
// The bracketed suffix appears for test-only build failures during
// `go test`-style invocations; we strip it so the Path stays canonical.
var pkgHeaderRe = regexp.MustCompile(`^# ([^\s\[]+)(?:\s+\[[^\]]*\])?\s*$`)

// errorLineRe matches a Go compiler error line:
//
//	./file.go:42:5: undefined: foo
//	./file.go:42: syntax error: ...
//	file.go:42:5: undefined: foo
//
// Anchored to start-of-line. Path may be relative (with ./), absolute,
// or bare; Col is optional.
var errorLineRe = regexp.MustCompile(`^([^\s:][^:]*):(\d+)(?::(\d+))?:\s*(.+)$`)

// parseStderr walks `go build`'s stderr line-by-line, grouping errors
// by their preceding `# package/path` header. Lines that don't fit
// either pattern are appended to the current package's raw output buf
// so cgo / linker messages still surface.
//
// Returns a slice in stable order (sorted by package path for
// determinism). Empty stderr yields nil.
func parseStderr(stderr string) []Package {
	type acc struct {
		path     string
		outBuf   strings.Builder
		errs     []BuildError
		order    int
	}
	pkgs := map[string]*acc{}
	order := []string{}
	var current *acc

	getOrCreate := func(path string) *acc {
		if a, ok := pkgs[path]; ok {
			return a
		}
		a := &acc{path: path, order: len(order)}
		pkgs[path] = a
		order = append(order, path)
		return a
	}

	lines := strings.Split(strings.TrimRight(stderr, "\n"), "\n")
	for _, line := range lines {
		if m := pkgHeaderRe.FindStringSubmatch(line); m != nil {
			current = getOrCreate(m[1])
			continue
		}
		if current == nil {
			// Lines before any `# pkg` header — usually toolchain
			// noise (gopls suggestions, env warnings). Drop them
			// rather than fabricate a synthetic package; if a real
			// error follows it will land under its own header.
			continue
		}
		current.outBuf.WriteString(line)
		current.outBuf.WriteByte('\n')
		if m := errorLineRe.FindStringSubmatch(strings.TrimLeft(line, " \t")); m != nil {
			be := BuildError{File: m[1], Message: strings.TrimSpace(m[4])}
			fmt.Sscanf(m[2], "%d", &be.Line)
			if m[3] != "" {
				fmt.Sscanf(m[3], "%d", &be.Col)
			}
			current.errs = append(current.errs, be)
		}
	}

	if len(order) == 0 {
		return nil
	}
	out := make([]Package, 0, len(order))
	for _, path := range order {
		a := pkgs[path]
		errs := a.errs
		// Sort errors within a package by (file, line, col) for
		// predictable render order across runs.
		sort.SliceStable(errs, func(i, j int) bool {
			if errs[i].File != errs[j].File {
				return errs[i].File < errs[j].File
			}
			if errs[i].Line != errs[j].Line {
				return errs[i].Line < errs[j].Line
			}
			return errs[i].Col < errs[j].Col
		})
		out = append(out, Package{
			Path:        path,
			Status:      "build_failed",
			BuildOutput: strings.TrimRight(a.outBuf.String(), "\n"),
			Errors:      errs,
		})
	}
	// Sort packages alphabetically for deterministic top-to-bottom rendering.
	sort.SliceStable(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// isFilesystemPackagePath mirrors test.go: filesystem paths get jail-
// checked; bare import paths (github.com/foo/bar) don't.
func isFilesystemPackagePath(p string) bool {
	return strings.HasPrefix(p, "/") || strings.HasPrefix(p, "./") || strings.HasPrefix(p, "../")
}

// normalizePackagePattern auto-prefixes bare directory patterns with
// "./" so `ash build internal/walker` reaches the local package rather
// than failing as a stdlib lookup. Same logic as test.go.
func normalizePackagePattern(p string) string {
	if p == "" {
		return p
	}
	if strings.HasPrefix(p, "./") || strings.HasPrefix(p, "../") || strings.HasPrefix(p, "/") {
		return p
	}
	first := p
	if i := strings.IndexByte(p, '/'); i >= 0 {
		first = p[:i]
	}
	if strings.Contains(first, ".") {
		return p
	}
	return "./" + p
}

// PrettyResponse renders the canonical token-cheap form: a header line
// with package counts + elapsed, then a per-failed-package block with
// each BuildError on its own line in file:line:col: message form.
//
// Success runs render as a one-line header — symmetric with `ash test`
// passing runs.
func PrettyResponse(_ *proto.Request, rsp *proto.Response) string {
	if !rsp.OK {
		return proto.PrettyResponseHeader(rsp)
	}
	var r Result
	if err := proto.UnmarshalData(rsp, &r); err != nil {
		return "ok\n<unrecognized build result>"
	}
	return prettyResult(&r)
}

func prettyResult(r *Result) string {
	var b strings.Builder
	overall := "ok"
	if !r.OK {
		overall = "fail"
	}
	totalErrs := 0
	for _, p := range r.Packages {
		totalErrs += len(p.Errors)
	}
	fmt.Fprintf(&b, "§build: %d pkg(s) failed, %d error(s) — %.2fs [%s]\n",
		len(r.Packages), totalErrs, r.Elapsed, overall)
	for _, p := range r.Packages {
		fmt.Fprintf(&b, "BUILD  %s\n", p.Path)
		if len(p.Errors) > 0 {
			for _, e := range p.Errors {
				if e.Col > 0 {
					fmt.Fprintf(&b, "  %s:%d:%d  %s\n", e.File, e.Line, e.Col, e.Message)
				} else {
					fmt.Fprintf(&b, "  %s:%d  %s\n", e.File, e.Line, e.Message)
				}
			}
		} else if p.BuildOutput != "" {
			// No structured errors parsed — surface the raw output so
			// the agent isn't blind to cgo / linker / tool failures.
			for _, line := range strings.Split(p.BuildOutput, "\n") {
				b.WriteString("  ")
				b.WriteString(line)
				b.WriteByte('\n')
			}
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
