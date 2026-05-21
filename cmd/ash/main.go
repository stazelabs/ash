// Command ash is the agent-facing client for the ash daemon. It auto-starts
// ashd if no daemon is reachable on the per-project socket, sends a single
// request, prints a pretty-rendered response, and exits.
package main

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/stazelabs/ash/internal/jail"
	"github.com/stazelabs/ash/internal/proto"
	"github.com/stazelabs/ash/internal/session"
	"github.com/stazelabs/ash/internal/verbs"
	"github.com/stazelabs/ash/internal/verbs/help"
	"github.com/stazelabs/ash/internal/verbs/stop"
)

func main() {
	if len(os.Args) < 2 || os.Args[1] == "-h" || os.Args[1] == "--help" {
		printUsage()
		os.Exit(2)
	}

	verb := os.Args[1]

	// `ash hook` is a client-only fast path for the Claude Code PreToolUse
	// hook. It reads the harness payload from stdin, computes the decision
	// in-process, writes the Claude-format response to stdout, and best-
	// effort fires a normal ash request to the daemon for ledger
	// instrumentation. It never auto-starts the daemon — hook latency is
	// on the agent's critical path.
	if verb == "--version" || verb == "-V" {
		fmt.Println("ash — use 'ash help' for the verb list")
		return
	}

	if verb == "hook" {
		runHook()
		return
	}

	format, _, remaining := extractFormat(os.Args[2:])
	args, err := parseFlags(verb, remaining)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ash:", err)
		os.Exit(2)
	}
	if err := resolveStdin(args); err != nil {
		fmt.Fprintln(os.Stderr, "ash:", err)
		os.Exit(2)
	}
	if err := resolveAtFile(args, "old", "new"); err != nil {
		fmt.Fprintln(os.Stderr, "ash:", err)
		os.Exit(2)
	}
	if err := resolvePatchFile(args); err != nil {

		fmt.Fprintln(os.Stderr, "ash:", err)
		os.Exit(2)
	}

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "ash: getwd:", err)
		os.Exit(1)
	}
	root, err := session.Root(cwd)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ash: project root:", err)
		os.Exit(1)
	}
	// Register a cosmetic policy so client-side pretty renderers can
	// strip the project-root prefix from path-heavy headers (ASH-71).
	// Enforcement happens daemon-side; this is enabled=false purely so
	// jail.PathPrefixes returns the right set on the client process.
	jail.SetPolicy(jail.FromConfig(false, root, nil, nil))
	sockPath := session.SocketPath(root)

	// `ash stop` is a client-side verb: it terminates the daemon, so it must
	// not dial it. Intercept here, after root is resolved but before dialOrStart.
	if verb == "stop" {
		runStop(root, format)
		return
	}

	conn, err := dialOrStart(root, sockPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ash: connect:", err)
		os.Exit(1)
	}
	defer conn.Close()

	req := &proto.Request{
		V:    proto.ProtocolVersion,
		ID:   newID(),
		Verb: verb,
		Args: args,
		Argv: os.Args[1:],
	}
	// ASH-132: forward the calling shell's environment for verbs that
	// shell out to a subprocess. The daemon's env was frozen at startup,
	// so without this UPDATE_GOLDEN / GO* / DEBUG toggles set by the
	// agent's shell never reach `go test`. Limited to `test` today —
	// other verbs don't shell out, and sending env unconditionally would
	// inflate request size and surface client secrets to verbs that
	// shouldn't see them.
	if verb == "test" || verb == "build" {
		req.Env = os.Environ()
	}
	encoded, err := proto.EncodeRequest(req)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ash: encode:", err)
		os.Exit(1)
	}
	if err := proto.WriteFrame(conn, encoded); err != nil {
		fmt.Fprintln(os.Stderr, "ash: write:", err)
		os.Exit(1)
	}

	respBuf, err := proto.ReadFrame(conn)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ash: read:", err)
		os.Exit(1)
	}
	rsp, err := proto.DecodeResponse(respBuf)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ash: decode:", err)
		os.Exit(1)
	}
	// bytes_out in the wire metrics is zero (it cannot be known until after
	// encoding, creating a circular dependency). Compute it from the actual
	// received frame length instead.
	if rsp.Metrics != nil {
		rsp.Metrics.BytesOut = len(respBuf)
	}

	switch format {
	case "json":
		jrsp := jsonResponse{
			V:       rsp.V,
			ID:      rsp.ID,
			OK:      rsp.OK,
			Err:     rsp.Err,
			Metrics: rsp.Metrics,
		}
		if len(rsp.Data) > 0 {
			var decoded any
			if err := proto.UnmarshalData(rsp, &decoded); err == nil {
				jrsp.Data = decoded
			} else {
				jrsp.Data = rsp.Data // fallback: base64-encoded bytes
			}
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(jrsp); err != nil {
			fmt.Fprintln(os.Stderr, "ash: json encode:", err)
			os.Exit(1)
		}

	case "compact":
		jrsp := jsonResponse{
			V:       rsp.V,
			ID:      rsp.ID,
			OK:      rsp.OK,
			Err:     rsp.Err,
			Metrics: rsp.Metrics,
		}
		if len(rsp.Data) > 0 {
			if c, ok := compactHandlers[verb]; ok && rsp.OK {
				if cd, err := c(rsp); err == nil && cd != nil {
					jrsp.Data = cd
				} else {
					// op not row-shaped (e.g. git status): fall back to json
					var decoded any
					if err2 := proto.UnmarshalData(rsp, &decoded); err2 == nil {
						jrsp.Data = decoded
					}
				}
			} else {
				var decoded any
				if err := proto.UnmarshalData(rsp, &decoded); err == nil {
					jrsp.Data = decoded
				} else {
					jrsp.Data = rsp.Data
				}
			}
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(jrsp); err != nil {
			fmt.Fprintln(os.Stderr, "ash: compact encode:", err)
			os.Exit(1)
		}

	case "msgpack":
		if _, err := os.Stdout.Write(respBuf); err != nil {
			fmt.Fprintln(os.Stderr, "ash: write msgpack:", err)
			os.Exit(1)
		}
	default: // "pretty"
		out := prettyResponse(verb, req, rsp)
		fmt.Println(out)
		if rsp.Metrics != nil {
			m := rsp.Metrics
			line := fmt.Sprintf("\n[ash bi=%d bo=%d ti=%d to=%d us=%d/%d",
				m.BytesIn, m.BytesOut, m.TokensIn, m.TokensOut,
				m.LatencyParseUs, m.LatencyExecUs,
			)
			if tok := m.TokensMethod; tok != "" && tok != "real:cl100k_base" {
				line += fmt.Sprintf(" tok=%s", tok)
			}
			if p := m.Phases; p != nil {
				if p.WalkUs > 0 {
					line += fmt.Sprintf(" w=%d", p.WalkUs)
				}
				if p.IOUs > 0 {
					line += fmt.Sprintf(" io=%d", p.IOUs)
				}
				if p.RegexUs > 0 {
					line += fmt.Sprintf(" r=%d", p.RegexUs)
				}
				if p.RegexCompileUs > 0 {
					line += fmt.Sprintf(" rc=%d", p.RegexCompileUs)
				}
			}
			if m.LatencyDispatchUs > 0 {
				line += fmt.Sprintf(" d=%d", m.LatencyDispatchUs)
			}
			fmt.Fprintln(os.Stderr, line+"]")
			if m.LedgerError != "" {
				fmt.Fprintf(os.Stderr,
					"[ash WARNING: ledger record FAILED: %s -- this call's metrics did not persist]\n",
					m.LedgerError,
				)
			}
		}
	}
	if !rsp.OK {
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprint(os.Stderr, help.RenderUsage(0))
}
// extractFormat pulls --format out of argv before verb flag parsing so it
// doesn't get forwarded to the daemon as an unknown arg. specified is true
// when the caller explicitly passed --format; false means it defaulted to
// "pretty" and the caller may promote row-shaped verbs to "compact".
func extractFormat(argv []string) (format string, specified bool, rest []string) {
	format = "pretty"
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		if strings.HasPrefix(a, "--format=") {
			format = a[len("--format="):]
			specified = true
		} else if a == "--format" && i+1 < len(argv) {
			i++
			format = argv[i]
			specified = true
		} else {
			rest = append(rest, a)
		}
	}
	return format, specified, rest
}

// verbPositionals lists, in order, the arg keys that may be supplied as
// positional arguments for each verb. Positional args are parsed
// left-to-right and assigned to slots in this order. The flag form
// (--key value) still works for the same keys; mixing positional and
// flag for the same key is an error to avoid silent overwrite.
var verbPositionals = map[string][]string{
	"read":    {"path"},
	"find":    {"path"},
	"grep":    {"pattern", "path"},
	"stat":    {"paths"},
	"write":   {"path"},
	"edit":    {"path"},
	"diff":    {"path"},
	"init":    {"path"},
	"uninit":  {"path"},
	"git":     {"op"},
	"help":    {"verb"},
	"report":  {"verb"},
	"metrics": {"verb"},
	"bench":   {"verb", "case"},
}

// verbListFlags names per-verb flags that semantically accept a
// comma-separated list. Repeating one of these flags accumulates values
// (joined with commas) rather than erroring; repeating any other flag is
// rejected so we never silently drop the earlier value.
var verbListFlags = map[string]map[string]bool{
	"build": {"packages": true},
	"test":  {"packages": true},
	"stat":  {"paths": true},
}

// verbBoolFlagsCache memoizes per-verb sets of bool-typed flag names,
// derived from help.Registry(). It lets parseFlags answer "is --foo a
// bool flag?" without scanning the registry on every call.
var (
	verbBoolFlagsOnce  sync.Once
	verbBoolFlagsCache map[string]map[string]bool
)

// verbBoolFlags returns the set of bool flag names registered for verb.
// The set keys flag names verbatim (no --no- stripping), so flags whose
// registered name itself begins with "no-" (e.g. --no-text, --no-clobber)
// resolve directly. Returns nil for unknown verbs; callers must treat a
// nil map as "no bool flags," which is the safe default.
func verbBoolFlags(verb string) map[string]bool {
	verbBoolFlagsOnce.Do(func() {
		reg := help.Registry()
		verbBoolFlagsCache = make(map[string]map[string]bool, len(reg))
		for _, vs := range reg {
			m := make(map[string]bool, len(vs.Args))
			for _, a := range vs.Args {
				if a.Type == "bool" {
					m[a.Name] = true
				}
			}
			verbBoolFlagsCache[vs.Verb] = m
		}
	})
	return verbBoolFlagsCache[verb]
}

// looksLikeBool reports whether s is a recognized bool literal — the same
// set argutil.ToBool accepts (case-insensitive). The CLI parser uses this
// to decide whether the token after a bool flag is its value or a free
// positional. ASH-122.
func looksLikeBool(s string) bool {
	switch strings.ToLower(s) {
	case "true", "false", "1", "0", "yes", "no":
		return true
	}
	return false
}

// parseFlags converts agent-friendly long flags and per-verb positional
// arguments into an args map. Both --key value and --key=value are
// accepted. Bare values (no -- prefix) are matched against the verb's
// positional slot list (verbPositionals).
//
// Bool flags (per help.Registry()) accept three forms: --flag (presence-
// only, equivalent to true), --flag=true|false, and --flag true|false (the
// trailing value is consumed only when it parses as a bool literal —
// matching argutil.ToBool — otherwise the token is left for the positional
// pass). This keeps the --key value muscle-memory shape working for flags
// whose registered name itself starts with "no-" (e.g. --no-text true,
// --no-clobber false), where the next-token would otherwise float free
// and collide with a positional slot. ASH-122.
//
// The bare form --no-foo remains shorthand for --foo=false when foo (not
// no-foo) is a registered bool flag — this covers default-true bools like
// --gi, --untracked, --mkdir without requiring the verbose =false suffix.
func parseFlags(verb string, argv []string) (map[string]any, error) {
	out := make(map[string]any)
	positionals := verbPositionals[verb]
	bools := verbBoolFlags(verb)
	posIdx := 0
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		if !strings.HasPrefix(a, "--") {
			if posIdx >= len(positionals) {
				return nil, fmt.Errorf("unexpected positional argument %q (use --key value)", a)
			}
			key := positionals[posIdx]
			posIdx++
			if _, exists := out[key]; exists {
				return nil, fmt.Errorf("--%s set both as a flag and as a positional argument", key)
			}
			out[key] = a
			continue
		}
		key := a[2:]
		var val string
		if eq := strings.IndexByte(key, '='); eq >= 0 {
			val = key[eq+1:]
			key = key[:eq]
		} else if bools[key] {
			if i+1 < len(argv) && looksLikeBool(argv[i+1]) {
				i++
				val = argv[i]
			} else {
				val = "true"
			}
		} else if strings.HasPrefix(key, "no-") && bools[key[3:]] {
			key = key[3:]
			val = "false"
		} else {
			if i+1 >= len(argv) {
				return nil, fmt.Errorf("flag --%s missing value", key)
			}
			i++
			val = argv[i]
		}
		if key == "" {
			return nil, errors.New("empty flag name")
		}
		if existing, dup := out[key]; dup {
			if verbListFlags[verb][key] {
				prev, _ := existing.(string)
				switch {
				case prev == "":
					out[key] = val
				case val == "":
					// keep prev
				default:
					out[key] = prev + "," + val
				}
				continue
			}
			return nil, fmt.Errorf("--%s set more than once (each flag accepts a single value; for list-typed flags use the comma-separated form, e.g. --%s a,b)", key, key)
		}
		out[key] = val
	}
	return out, nil
}

// resolveStdin replaces any arg value of exactly "-" with content read from
// stdin. Only one arg may use "-" per invocation; reading stdin twice is an
// error. This enables: echo 'new code' | ash write --path f.go --content -
//
// All "-" sentinels share one stdin: --old, --new, --content, --patch are
// each pluggable; the verb consuming the resolved string doesn't care which
// flag carried the dash. ASH-113 covered --old explicitly to relieve
// multi-line shell-quoting friction.
func resolveStdin(args map[string]any) error {
	isTTY := false
	if fi, err := os.Stdin.Stat(); err == nil && (fi.Mode()&os.ModeCharDevice) != 0 {
		isTTY = true
	}
	return resolveStdinFromReader(args, os.Stdin, isTTY)
}

// resolveStdinFromReader is the testable core of resolveStdin: it scans args
// for "-" sentinels, errors on duplicates or TTY-piped input, and otherwise
// reads the full reader into the sentinel arg. Kept separate from
// resolveStdin so tests can inject a controlled reader.
func resolveStdinFromReader(args map[string]any, r io.Reader, isTTY bool) error {
	var stdinKey string
	for k, v := range args {
		if s, ok := v.(string); ok && s == "-" {
			if stdinKey != "" {
				return fmt.Errorf("only one arg can read from stdin (-); got both --%s and --%s", stdinKey, k)
			}
			stdinKey = k
		}
	}
	if stdinKey == "" {
		return nil
	}
	if isTTY {
		verbName := ""
		if len(os.Args) > 1 {
			verbName = os.Args[1]
		}
		return fmt.Errorf("stdin_not_piped: --%s uses \"-\" to read from stdin, but stdin is a terminal\n  pipe content in: echo '...' | ash %s\n  or pass the value directly: --%s '...'", stdinKey, verbName, stdinKey)
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return fmt.Errorf("reading stdin for --%s: %w", stdinKey, err)
	}
	args[stdinKey] = string(data)
	return nil
}

// resolveAtFile expands string args of the form "@PATH" by replacing them
// with the contents of PATH on disk. This lets a single `ash edit` call
// supply both --old and --new from external storage in one shot (ASH-119),
// since the stdin sentinel "-" can only be used by one arg per invocation.
//
// The leading "@" is hard: if it is present and the file is missing or
// unreadable, the call errors loudly rather than falling back to a literal
// value. --old / --new strings are arbitrary user content (code that may
// itself look like a path), so silent file-vs-literal heuristics would be
// a footgun. To pass a literal value that genuinely starts with "@", pipe
// it through stdin with "-" instead.
func resolveAtFile(args map[string]any, keys ...string) error {
	for _, k := range keys {
		v, ok := args[k]
		if !ok {
			continue
		}
		s, ok := v.(string)
		if !ok || !strings.HasPrefix(s, "@") {
			continue
		}
		path := s[1:]
		if path == "" {
			return fmt.Errorf("--%s: empty path after @", k)
		}
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("reading file for --%s: %w", k, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("--%s @%s is not a regular file", k, path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("reading file for --%s: %w", k, err)
		}
		args[k] = string(data)
	}
	return nil
}

// resolvePatchFile replaces --patch <file> with the file's contents when the

// value is a path to an existing regular file (and not the stdin sentinel "-").
// This enables: ash edit --path f.go --patch my.diff
func resolvePatchFile(args map[string]any) error {
	v, ok := args["patch"]
	if !ok {
		return nil
	}
	s, ok := v.(string)
	if !ok || s == "" || s == "-" {
		return nil
	}
	info, err := os.Stat(s)
	if err != nil {
		return nil // not a file path; pass through as literal patch text
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("--patch %q is not a regular file", s)
	}
	data, err := os.ReadFile(s)
	if err != nil {
		return fmt.Errorf("reading patch file %q: %w", s, err)
	}
	args["patch"] = string(data)
	return nil
}


func dialOrStart(root, sock string) (net.Conn, error) {
	killStaleIfNeeded(root, sock)
	if conn, err := net.DialTimeout("unix", sock, 200*time.Millisecond); err == nil {
		return conn, nil
	} else if !isConnRefused(err) && !isENOENT(err) {
		return nil, err
	}
	// Before auto-starting, refuse if an ashd process is already alive
	// for this socket — the socket is gone/unreachable but the process
	// survived, which is exactly the orphan state ASH-151 cleans up.
	// Spawning a second daemon would race the survivor and let stale
	// behavior leak into the session.
	if pids := stop.FindAshdPIDs(sock); len(pids) > 0 {
		return nil, fmt.Errorf("multiple_daemons: %d ashd process(es) still alive for this socket (pid=%v) but the socket is unreachable; run ash stop to clean up",
			len(pids), pids)
	}
	if err := startDaemon(root, sock); err != nil {
		return nil, fmt.Errorf("start daemon: %w", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		conn, err := net.DialTimeout("unix", sock, 200*time.Millisecond)
		if err == nil {
			return conn, nil
		}
		if time.Now().After(deadline) {
			msg := fmt.Sprintf("daemon did not come up within 2s: %v", err)
			if tail := tailLog(session.LogPath(root), 20); tail != "" {
				msg += "\n\nashd log (last lines):\n" + tail
			}
			return nil, errors.New(msg)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func tailLog(path string, n int) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil || len(data) == 0 {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func startDaemon(root, sock string) error {
	bin, err := findAshd()
	if err != nil {
		return err
	}
	if err := session.EnsureRuntimeDirs(root); err != nil {
		return err
	}
	logF, err := os.OpenFile(session.LogPath(root), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer logF.Close()
	cmd := exec.Command(bin, "--root", root, "--socket", sock)
	cmd.Stdout = logF
	cmd.Stderr = logF
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return cmd.Start()
}

func findAshd() (string, error) {
	if env := os.Getenv("ASH_DAEMON"); env != "" {
		if _, err := os.Stat(env); err == nil {
			return env, nil
		}
	}
	if exe, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(exe), "ashd")
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	if p, err := exec.LookPath("ashd"); err == nil {
		return p, nil
	}
	return "", errors.New("ashd binary not found (tried $ASH_DAEMON, sibling of ash, and PATH)")
}

// killStaleIfNeeded checks whether the ashd binary is newer than the socket
// file. If so, the running daemon is stale — it sweeps every ashd bound to
// this socket (SIGTERM, then SIGKILL after a bounded grace), then removes
// the socket so the normal auto-start path picks up the fresh binary.
//
// ASH-154: the pre-sweep version only signalled the pidfile PID and
// trusted SIGTERM-then-socket-unlink to do the rest, which left orphans
// alive whenever the old daemon was mid-request or had a hung handler.
// stop.SweepAshdOnSocket reuses the same per-PID grace+escalate logic
// that `ash stop` uses for its post-hoc orphan cleanup, so the
// preventative and curative paths agree on what "gone" means.
func killStaleIfNeeded(root, sock string) {
	ashdBin, err := findAshd()
	if err != nil {
		return
	}
	binStat, err := os.Stat(ashdBin)
	if err != nil {
		return
	}
	sockStat, err := os.Stat(sock)
	if err != nil {
		return // socket doesn't exist; daemon not running
	}
	if !binStat.ModTime().After(sockStat.ModTime()) {
		return // binary is not newer than socket; daemon is current
	}
	_ = stop.SweepAshdOnSocket(sock)
	// Remove the socket so dialOrStart falls through to startDaemon. The
	// sweep may have already done this when the daemons unlinked on exit,
	// but in the SIGKILL escalation path nobody cleaned it up.
	_ = os.Remove(sock)
}

func isConnRefused(err error) bool {
	var oe *net.OpError
	if errors.As(err, &oe) {
		return oe.Err != nil && strings.Contains(oe.Err.Error(), "connection refused")
	}
	return strings.Contains(err.Error(), "connection refused")
}

func isENOENT(err error) bool {
	if errors.Is(err, fs.ErrNotExist) {
		return true
	}
	return strings.Contains(err.Error(), "no such file")
}

func newID() uint64 {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return uint64(time.Now().UnixNano())
	}
	return binary.BigEndian.Uint64(b[:])
}

// jsonResponse mirrors proto.Response but uses `any` for Data so the
// msgpack payload is emitted as decoded JSON rather than base64 bytes.
//
// Field order mirrors proto.Response (ASH-108): the cache-stable prefix
// (V, OK, Data, Err) precedes the volatile suffix (ID, Metrics) so
// `--format json` output of two identical calls shares a long prefix
// suitable for Anthropic prompt caching. See docs/cache-shape.md.
type jsonResponse struct {
	V       int            `json:"v"`
	OK      bool           `json:"ok"`
	Data    any            `json:"data,omitempty"`
	Err     *proto.Error   `json:"err,omitempty"`
	ID      uint64         `json:"id"`
	Metrics *proto.Metrics `json:"metrics,omitempty"`
}

// prettyHandlers is built once at process start; renderers are pure and
// don't need rebuilding per call.
var prettyHandlers = verbs.PrettyHandlers()

// compactHandlers is built once at process start; maps verb → compact renderer.
var compactHandlers = verbs.CompactHandlers()

func prettyResponse(verb string, req *proto.Request, rsp *proto.Response) string {
	if p, ok := prettyHandlers[verb]; ok {
		return p(req, rsp)
	}
	return proto.PrettyResponseHeader(rsp)
}
