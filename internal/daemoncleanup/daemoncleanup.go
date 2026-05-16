// Package daemoncleanup discovers ashd processes across the host and
// classifies each as alive, zombie, or unknown so an out-of-band tool can
// reap the abandoned ones. Per-project ash is correctness-safe — every
// project gets its own UDS, ledger, and pidfile — but resource-leaky on a
// laptop that opens dozens of projects: an `ashd` whose project directory
// was deleted still holds ~10–20 MB RSS, a tokenizer, and a deleted-but-
// open SQLite file. `ash stop` only knows about the current project's
// socket; this package powers `bin/ashd-clean` (ASH-155), the broader
// sweep that walks the live process table.
//
// Mechanism mirrors internal/verbs/stop/orphans.go: shell out to `ps`,
// match argv[0] to ashd, extract --root and --socket. The split — own
// process lister vs. reusing stop's — is deliberate. The classifier
// here needs `--root` (not just `--socket`), and the test seam pattern
// works best when each package owns its own lister. Killing reuses
// stop.CleanupOrphans so the curative and preventative paths share the
// same per-PID grace + SIGKILL escalation policy.
package daemoncleanup

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/stazelabs/ash/internal/verbs/stop"
)

// Status describes a daemon's health from the classifier's perspective.
// Only zombies are signalled by Cleanup; alive and unknown are reported
// for visibility but never touched.
type Status string

const (
	// StatusAlive — argv parses, --root is an existing directory, and
	// --socket dials successfully. Definitely a real working daemon for
	// a real project; never reap.
	StatusAlive Status = "alive"
	// StatusZombie — argv parses but the daemon's project root is gone
	// or its socket no longer accepts connections. Resource-leaky and
	// safe to SIGTERM.
	StatusZombie Status = "zombie"
	// StatusUnknown — we found an ashd process but its argv didn't
	// contain --root or --socket, so we can't reason about its project.
	// Reported as a row but never signalled — the user investigates.
	StatusUnknown Status = "unknown"
)

// Daemon is one classified ashd process found in the host process table.
type Daemon struct {
	PID    int    `json:"pid" msgpack:"pid"`
	Root   string `json:"root,omitempty" msgpack:"root,omitempty"`
	Socket string `json:"socket,omitempty" msgpack:"socket,omitempty"`
	Status Status `json:"status" msgpack:"status"`
	Reason string `json:"reason,omitempty" msgpack:"reason,omitempty"`
	Cmd    string `json:"cmd" msgpack:"cmd"`
}

// processInfo is one row of the live process scan: PID and full command
// line. Kept narrow so the test seam stays small.
type processInfo struct {
	PID     int
	Cmdline string
}

// dialTimeout bounds how long we wait per-socket during classification.
// Short enough to keep `ashd-clean` snappy on a host with many projects;
// long enough to survive a momentarily-busy daemon.
const dialTimeout = 250 * time.Millisecond

// processLister returns the current process table. Overridable for tests.
// Default shells out to `ps -A -ww` — same primitive as
// internal/verbs/stop/orphans.go for the same reasons (no /proc on
// macOS, no lsof dependency).
var processLister = listProcessesPS

// socketDialer reports whether a unix-domain socket accepts a connection.
// Overridable for tests so classification doesn't actually create files.
var socketDialer = dialUnixOnce

// rootExists reports whether the given path is an existing directory.
// Overridable for tests.
var rootExists = func(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func listProcessesPS() ([]processInfo, error) {
	out, err := exec.Command("ps", "-A", "-ww", "-o", "pid=,command=").Output()
	if err != nil {
		return nil, fmt.Errorf("ps: %w", err)
	}
	var procs []processInfo
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimLeft(line, " \t")
		if line == "" {
			continue
		}
		idx := strings.IndexAny(line, " \t")
		if idx < 0 {
			continue
		}
		pid, err := strconv.Atoi(line[:idx])
		if err != nil {
			continue
		}
		procs = append(procs, processInfo{PID: pid, Cmdline: strings.TrimSpace(line[idx:])})
	}
	return procs, nil
}

func dialUnixOnce(sock string) error {
	conn, err := net.DialTimeout("unix", sock, dialTimeout)
	if err != nil {
		return err
	}
	_ = conn.Close()
	return nil
}

// Scan walks the process table for ashd processes and classifies each
// one. The current process is skipped so a self-hosted run from inside
// an ash-managed project doesn't try to introspect its own pid. Returns
// nil if `ps` fails — the binary surfaces "no processes found" rather
// than treating an empty listing as a successful sweep.
func Scan() []Daemon {
	procs, err := processLister()
	if err != nil {
		return nil
	}
	self := os.Getpid()
	out := make([]Daemon, 0, 4)
	for _, p := range procs {
		if p.PID == self {
			continue
		}
		fields := strings.Fields(p.Cmdline)
		if len(fields) == 0 || !isAshdProgram(fields[0]) {
			continue
		}
		out = append(out, classify(p))
	}
	return out
}

// classify reads --root and --socket out of argv, then probes each. We
// require *some* signal — if neither flag parses, we cannot tell what
// project the daemon belongs to, so StatusUnknown is the honest answer
// and Cleanup will skip it.
func classify(p processInfo) Daemon {
	d := Daemon{PID: p.PID, Cmd: p.Cmdline}
	root, gotRoot := extractFlag(p.Cmdline, "--root")
	sock, gotSock := extractFlag(p.Cmdline, "--socket")
	d.Root = root
	d.Socket = sock

	if !gotRoot && !gotSock {
		d.Status = StatusUnknown
		d.Reason = "could not parse --root or --socket from argv"
		return d
	}

	// "Could not check" is treated as healthy: we only mark zombie on
	// evidence of brokenness, not on absence of one half of the pair.
	rootOK := !gotRoot || rootExists(root)
	sockOK := !gotSock || socketDialer(sock) == nil

	if rootOK && sockOK {
		d.Status = StatusAlive
		return d
	}
	d.Status = StatusZombie
	switch {
	case !rootOK && !sockOK:
		d.Reason = "root dir missing and socket unreachable"
	case !rootOK:
		d.Reason = "root dir missing"
	default:
		d.Reason = "socket unreachable"
	}
	return d
}

// extractFlag pulls value from `--flag value` or `--flag=value` in
// cmdline. Returns ok=false when the flag is absent or terminates the
// argv without a value.
func extractFlag(cmdline, flag string) (string, bool) {
	fields := strings.Fields(cmdline)
	eqPrefix := flag + "="
	for i, f := range fields {
		if f == flag && i+1 < len(fields) {
			return fields[i+1], true
		}
		if strings.HasPrefix(f, eqPrefix) {
			return f[len(eqPrefix):], true
		}
	}
	return "", false
}

// isAshdProgram reports whether arg0 names the ashd binary. Matches
// `ashd`, `bin/ashd`, `/usr/local/bin/ashd`, etc. — suffix-style on the
// last path segment so install layout doesn't matter. Critically does
// *not* match `myashd` or `ashd-clean`: we strictly require the basename
// to equal "ashd".
func isAshdProgram(arg0 string) bool {
	if arg0 == "ashd" {
		return true
	}
	if i := strings.LastIndexByte(arg0, '/'); i >= 0 {
		return arg0[i+1:] == "ashd"
	}
	return false
}

// Cleanup signals every Daemon in `daemons` whose Status is StatusZombie.
// Alive and unknown daemons are passed through untouched. The kill+wait
// per PID delegates to stop.CleanupOrphans so the grace window and
// SIGTERM→SIGKILL escalation match the `ash stop` curative path.
func Cleanup(daemons []Daemon) []stop.Orphan {
	var pids []int
	for _, d := range daemons {
		if d.Status == StatusZombie {
			pids = append(pids, d.PID)
		}
	}
	return stop.CleanupOrphans(pids)
}
