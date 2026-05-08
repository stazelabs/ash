// Command ash is the agent-facing client for the ash daemon. It auto-starts
// ashd if no daemon is reachable on the per-project socket, sends a single
// request, prints a pretty-rendered response, and exits.
package main

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
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
	"github.com/stazelabs/ash/internal/verbs/find"
	"github.com/stazelabs/ash/internal/verbs/read"
)

func main() {
	if len(os.Args) < 2 || os.Args[1] == "-h" || os.Args[1] == "--help" {
		printUsage()
		os.Exit(2)
	}

	verb := os.Args[1]
	args, err := parseFlags(os.Args[2:])
	if err != nil {
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

	out := prettyResponse(verb, req, rsp)
	fmt.Println(out)
	if rsp.Metrics != nil {
		fmt.Fprintf(os.Stderr,
			"\n[ash metrics: bytes_in=%d bytes_out=%d tokens_in=%d tokens_out=%d (%s) latency_us=%d/%d/%d]\n",
			rsp.Metrics.BytesIn, rsp.Metrics.BytesOut,
			rsp.Metrics.TokensIn, rsp.Metrics.TokensOut, rsp.Metrics.TokensMethod,
			rsp.Metrics.LatencyParseUs, rsp.Metrics.LatencyExecUs, rsp.Metrics.LatencySerializeUs,
		)
		if rsp.Metrics.LedgerError != "" {
			fmt.Fprintf(os.Stderr,
				"[ash WARNING: ledger record FAILED: %s -- this call's metrics did not persist]\n",
				rsp.Metrics.LedgerError,
			)
		}
	}
	if !rsp.OK {
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintln(os.Stderr, `usage: ash <verb> [--key value | --key=value]...

verbs (phase 1, work in progress):
  read --path <p> [--range start:end] [--range_kind lines|bytes] [--limit_bytes N]
  find --path <p> [--glob <pattern>] [--type any|file|dir|symlink]
                  [--max_depth N] [--limit N] [--exclude <pattern>]
                  [--include_hidden true|false]

ash auto-starts the daemon (ashd) on first call.`)
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

func dialOrStart(root, sock string) (net.Conn, error) {
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
			return nil, fmt.Errorf("daemon did not come up within 2s: %w", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
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

func prettyResponse(verb string, req *proto.Request, rsp *proto.Response) string {
	switch verb {
	case "read":
		return read.PrettyResponse(req, rsp)
	case "find":
		return find.PrettyResponse(req, rsp)
	default:
		return proto.PrettyResponseHeader(rsp)
	}
}
