package main

import (
	"context"
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

	"github.com/stazelabs/ash/internal/session"
	"github.com/stazelabs/ash/internal/verbs/stop"
)

// dialOrStart mirrors the same-named helper in cmd/ash: if the daemon
// is running on `sock`, return a connection; otherwise auto-start ashd
// (sibling lookup, $ASH_DAEMON, $PATH) and dial once it's up. Stale-daemon
// detection (binary newer than the socket) signals the old daemon and
// removes the socket so the next dial picks up the fresh binary. The
// context deadline bounds the spin-wait so a stuck ashd can't hang the
// MCP server thread.
//
// Deliberate duplication of the cmd/ash logic — keeping the two clients
// independent avoids one tightly-coupled "client helpers" package the
// next harness adapter would also have to consume. See CLAUDE.md
// "Don't add features... beyond what the task requires" — extract when
// the third caller appears.
func dialOrStart(ctx context.Context, root, sock string) (net.Conn, error) {
	killStaleIfNeeded(root, sock)
	if conn, err := net.DialTimeout("unix", sock, 200*time.Millisecond); err == nil {
		return conn, nil
	} else if !isConnRefused(err) && !isENOENT(err) {
		return nil, err
	}
	// Mirror cmd/ash: refuse to spawn a second daemon when an orphan ashd
	// is still alive for this socket. The MCP server has no way to surface
	// a clean error to the harness mid-stream, so producing this error
	// here is strictly better than racing the orphan. ASH-151.
	if pids := stop.FindAshdPIDs(sock); len(pids) > 0 {
		return nil, fmt.Errorf("multiple_daemons: %d ashd process(es) still alive for socket %s (pid=%v) but the socket is unreachable; run ash stop to clean up",
			len(pids), sock, pids)
	}
	if err := startDaemon(root, sock); err != nil {
		return nil, fmt.Errorf("start daemon: %w", err)
	}
	for {
		conn, err := net.DialTimeout("unix", sock, 200*time.Millisecond)
		if err == nil {
			return conn, nil
		}
		select {
		case <-ctx.Done():
			logPath := session.LogPath(root)
			msg := fmt.Sprintf("daemon did not come up at socket %s (root %s): %v", sock, root, err)
			if tail := tailLog(logPath, 20); tail != "" {
				msg += "\n\nashd log (last lines from " + logPath + "):\n" + tail
			}
			return nil, errors.New(msg)
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func startDaemon(root, sock string) error {
	bin, err := findAshd()
	if err != nil {
		return err // already names  / sibling / PATH in its message
	}
	if err := session.EnsureRuntimeDirs(root); err != nil {
		return fmt.Errorf("create runtime dirs under %s: %w", root, err)
	}
	logPath := session.LogPath(root)
	logF, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open daemon log %s: %w", logPath, err)
	}
	defer logF.Close()
	cmd := exec.Command(bin, "--root", root, "--socket", sock)
	cmd.Stdout = logF
	cmd.Stderr = logF
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("spawn %s --root %s --socket %s: %w", bin, root, sock, err)
	}
	return nil
}

// findAshd resolves the daemon binary path. ashmcp ships as a sibling
// of ashd in bin/, so the sibling-of-executable lookup hits first. The
// $ASH_DAEMON override and $PATH fallback match cmd/ash's behavior so
// the two clients agree on which daemon they're talking to.
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
	return "", errors.New("ashd binary not found (tried $ASH_DAEMON, sibling of ashmcp, and PATH)")
}

// killStaleIfNeeded mirrors the cmd/ash helper of the same name: when
// the daemon binary is newer than the socket, sweep every ashd bound to
// the socket and unlink it so dialOrStart picks up the fresh binary.
// ASH-154 — the pre-sweep version only signalled the pidfile PID and
// left orphans behind when the old daemon was mid-request.
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
		return
	}
	if !binStat.ModTime().After(sockStat.ModTime()) {
		return
	}
	_ = stop.SweepAshdOnSocket(sock)
	_ = os.Remove(sock)
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
