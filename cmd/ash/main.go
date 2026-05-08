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
	"syscall"
	"time"

	"github.com/stazelabs/ash/internal/proto"
	"github.com/stazelabs/ash/internal/session"
	"github.com/stazelabs/ash/internal/verbs"
)

func main() {
	if len(os.Args) < 2 || os.Args[1] == "-h" || os.Args[1] == "--help" {
		printUsage()
		os.Exit(2)
	}

	verb := os.Args[1]
	format, remaining := extractFormat(os.Args[2:])
	args, err := parseFlags(remaining)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ash:", err)
		os.Exit(2)
	}
	if err := resolveStdin(args); err != nil {
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
	sockPath := session.SocketPath(root)

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

	switch format {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rsp); err != nil {
			fmt.Fprintln(os.Stderr, "ash: json encode:", err)
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
			fmt.Fprintf(os.Stderr,
				"\n[ash metrics: bytes_in=%d bytes_out=%d tokens_in=%d tokens_out=%d (%s) latency_us=%d/%d/%d",
				rsp.Metrics.BytesIn, rsp.Metrics.BytesOut,
				rsp.Metrics.TokensIn, rsp.Metrics.TokensOut, rsp.Metrics.TokensMethod,
				rsp.Metrics.LatencyParseUs, rsp.Metrics.LatencyExecUs, rsp.Metrics.LatencySerializeUs,
			)
			if p := rsp.Metrics.Phases; p != nil {
				fmt.Fprintf(os.Stderr, " phases=walk:%d/io:%d/regex:%d", p.WalkUs, p.IOUs, p.RegexUs)
			}
			fmt.Fprintln(os.Stderr, "]")
			if rsp.Metrics.LedgerError != "" {
				fmt.Fprintf(os.Stderr,
					"[ash WARNING: ledger record FAILED: %s -- this call's metrics did not persist]\n",
					rsp.Metrics.LedgerError,
				)
			}
		}
	}
	if !rsp.OK {
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintln(os.Stderr, `usage: ash <verb> [--key value | --key=value]... [--format pretty|json|msgpack]

verbs (phase 2):
  read    --path <p> [--range start:end] [--range_kind lines|bytes] [--limit_bytes N]
  write   --path <p> --content <text|-> [--encoding utf-8|base64]
                     [--mkdir true|false] [--create_only true|false]
  edit    --path <p> --old_string <text> [--new_string <text>]
                     [--replace_all true|false] [--dry_run true|false]
          --path <p> --range start:end [--new_content <text>]
                     [--dry_run true|false]
  diff    --path <p> (--other <p2> | --content <text|->) [--context N]
  find    --path <p> [--glob <pattern>] [--type any|file|dir|symlink]
                     [--max_depth N] [--limit N] [--exclude <pattern>]
                     [--include_hidden true|false] [--with_meta true|false]
                     [--respect_gitignore true|false]   (default true)
  grep    --pattern <p> --path <p> [--glob <pattern>]
                     [--case smart|sensitive|insensitive] (default smart)
                     [--fixed_string true|false] [--word true|false]
                     [--max_matches N] [--max_per_file N]
                     [--context_before N] [--context_after N]
                     [--files_only true|false]
                     [--exclude <pattern>] [--max_depth N]
                     [--include_hidden true|false]
                     [--respect_gitignore true|false]   (default true)
  git     --op <op> [--path <p>] [op-specific flags]
                     ops: status [--untracked true|false] [--ignored true|false]
                          log    [--limit N] [--range <rev>] [--author <s>]
                                 [--since <d>] [--until <d>] [--pathspec <p>]
                          diff   [--staged true|false] [--range <rev>]
                                 [--pathspec <p>] [--stat true|false]
                                 [--context N] [--limit_bytes N]
  metrics [--last N] [--verb <verb>]                   (default last=20)
  report  [--session current|all|<id>] [--since <dur>] (default session=current)
          [--last N] [--verb <verb>]
  stat    --paths <p1>[,<p2>...]                        (lstat; per-entry errors)
  bench   [--verb <verb>] [--case <name>] [--limit N]   (ash vs bash comparison)
  help    [--verb <verb>]                               (omit for all verbs)

note: pass - as a value to read that arg from stdin (e.g. --content -)

global flags:
  --format pretty|json|msgpack   output format (default: pretty)

ash auto-starts the daemon (ashd) on first call.`)
}

// extractFormat pulls --format out of argv before verb flag parsing so it
// doesn't get forwarded to the daemon as an unknown arg.
func extractFormat(argv []string) (format string, rest []string) {
	format = "pretty"
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		if strings.HasPrefix(a, "--format=") {
			format = a[len("--format="):]
		} else if a == "--format" && i+1 < len(argv) {
			i++
			format = argv[i]
		} else {
			rest = append(rest, a)
		}
	}
	return format, rest
}

// parseFlags converts agent-friendly long flags into an args map. Both
// --key value and --key=value are accepted. Boolean shorthand (--flag with no
// value) is rejected for now to keep the on-wire shape unambiguous.
func parseFlags(argv []string) (map[string]any, error) {
	out := make(map[string]any)
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		if !strings.HasPrefix(a, "--") {
			return nil, fmt.Errorf("unexpected positional argument %q (use --key value)", a)
		}
		key := a[2:]
		var val string
		if eq := strings.IndexByte(key, '='); eq >= 0 {
			val = key[eq+1:]
			key = key[:eq]
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
		out[key] = val
	}
	return out, nil
}

// resolveStdin replaces any arg value of exactly "-" with content read from
// stdin. Only one arg may use "-" per invocation; reading stdin twice is an
// error. This enables: echo 'new code' | ash write --path f.go --content -
func resolveStdin(args map[string]any) error {
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
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return fmt.Errorf("reading stdin for --%s: %w", stdinKey, err)
	}
	args[stdinKey] = string(data)
	return nil
}

func dialOrStart(root, sock string) (net.Conn, error) {
	killStaleIfNeeded(root, sock)
	if conn, err := net.DialTimeout("unix", sock, 200*time.Millisecond); err == nil {
		return conn, nil
	} else if !isConnRefused(err) && !isENOENT(err) {
		return nil, err
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
// file. If so, the running daemon is stale — it sends SIGTERM to the PID from
// .ash/ashd.pid and removes the socket so the normal auto-start path picks up
// the fresh binary. Errors are silently ignored: the worst case is we connect
// to the old daemon and get a mismatch, not a crash.
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
	// Binary is newer — read PID file and SIGTERM the old daemon.
	pidData, err := os.ReadFile(session.PIDPath(root))
	if err == nil {
		var pid int
		if _, err := fmt.Sscan(strings.TrimSpace(string(pidData)), &pid); err == nil && pid > 0 {
			if proc, err := os.FindProcess(pid); err == nil {
				_ = proc.Signal(syscall.SIGTERM)
			}
		}
	}
	// Remove the socket so dialOrStart falls through to startDaemon.
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

// prettyHandlers is built once at process start; renderers are pure and
// don't need rebuilding per call.
var prettyHandlers = verbs.PrettyHandlers()

func prettyResponse(verb string, req *proto.Request, rsp *proto.Response) string {
	if p, ok := prettyHandlers[verb]; ok {
		return p(req, rsp)
	}
	return proto.PrettyResponseHeader(rsp)
}
