// Orphan discovery and cleanup for `ash stop`. A second `ashd` process can
// end up bound to the same per-project UDS — typically after a crashed
// daemon, a rebuild-then-restart cycle, or a missed graceful shutdown —
// because Unix socket semantics let a new process unlink and rebind the
// socket file while the previous owner's listening fd stays open. The
// pidfile only remembers one daemon, so signalling its PID leaves any
// other matching `ashd` processes alive, potentially serving stale code.
//
// findOrphanDaemons scans the live process table for `ashd` processes
// whose argv contains `--socket <sock>` and returns those whose PID is
// not in the `exclude` set. The scan is cheap (single ps invocation) and
// platform-agnostic across macOS and Linux. A test seam overrides the
// process lister so cleanup logic can be exercised without forging argv.
package stop

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// orphanGracePerProc bounds how long we wait for an individual orphan to
// exit after SIGTERM before escalating to SIGKILL. The ticket called out
// "2s each"; that gives a single misbehaving orphan room to flush its
// ledger row without dragging the verb past its overall budget.
const orphanGracePerProc = 2 * time.Second

// Orphan describes one additional `ashd` process that shared the socket
// with the pidfile-pointed daemon and was cleaned up alongside it.
type Orphan struct {
	PID       int    `msgpack:"pid"`
	Signal    string `msgpack:"signal"`     // "SIGTERM" | "SIGKILL"
	Exited    bool   `msgpack:"exited"`     // true if process is gone post-cleanup
	ElapsedMs int64  `msgpack:"elapsed_ms"` // wall time from first signal to confirmed exit (or timeout)
}

// processInfo is one row of the live process scan: PID and full command
// line (argv joined with spaces). Kept narrow so the test seam stays small.
type processInfo struct {
	PID     int
	Cmdline string
}

// processLister returns the current process table. Overridable for tests.
// The default implementation shells out to `ps`, which is available on
// every macOS and Linux distribution ashd supports — no /proc dependency
// (macOS lacks /proc) and no /usr/sbin/lsof dependency (not always
// installed). `-ww` disables column truncation so long --socket paths
// survive intact.
var processLister = listProcessesPS

// FindAshdPIDs returns the PIDs of live ashd processes bound to sockPath
// (i.e. argv contains --socket sockPath), excluding the current process.
// Exposed for the auto-start path: when dialOrStart cannot reach the
// socket but ashd processes are still alive for it, the client refuses
// to spawn a second daemon and tells the user to run `ash stop`. ASH-151.
func FindAshdPIDs(sockPath string) []int {
	return findOrphanDaemons(sockPath, nil)
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

// findOrphanDaemons returns the PIDs of live `ashd` processes whose argv
// references `--socket sockPath` (in either `--socket=PATH` or
// `--socket PATH` form), filtering out PIDs in `exclude` and the current
// process. A nil/empty sockPath returns nil — we never want to kill all
// `ashd` processes, only the ones bound to *this* project's socket.
func findOrphanDaemons(sockPath string, exclude map[int]bool) []int {
	if sockPath == "" {
		return nil
	}
	procs, err := processLister()
	if err != nil {
		return nil
	}
	self := os.Getpid()
	var pids []int
	for _, p := range procs {
		if p.PID == self || exclude[p.PID] {
			continue
		}
		if !matchesAshdSocket(p.Cmdline, sockPath) {
			continue
		}
		pids = append(pids, p.PID)
	}
	return pids
}

// matchesAshdSocket reports whether cmdline names an ashd process bound
// to sockPath. We require both the `ashd` token and the socket path so
// matches don't bleed across projects (a different `ashd` on a sibling
// socket) or pick up unrelated processes that happen to mention the path
// (e.g. a `tail` or an editor with the socket open in a buffer).
//
// The `ashd` token check matches the program name suffix-style: any
// argv[0] ending in `/ashd` or exactly `ashd` qualifies. This survives
// the `bin/ashd`, `/usr/local/bin/ashd`, and absolute-path forms the
// auto-start path can produce.
func matchesAshdSocket(cmdline, sockPath string) bool {
	fields := strings.Fields(cmdline)
	if len(fields) == 0 {
		return false
	}
	if !isAshdProgram(fields[0]) {
		return false
	}
	for i, f := range fields {
		if f == "--socket" && i+1 < len(fields) && fields[i+1] == sockPath {
			return true
		}
		if strings.HasPrefix(f, "--socket=") && f[len("--socket="):] == sockPath {
			return true
		}
	}
	return false
}

func isAshdProgram(arg0 string) bool {
	if arg0 == "ashd" {
		return true
	}
	if i := strings.LastIndexByte(arg0, '/'); i >= 0 {
		return arg0[i+1:] == "ashd"
	}
	return false
}

// cleanupOrphans signals each orphan with SIGTERM, polls for exit up to
// orphanGracePerProc, then escalates to SIGKILL if the process is still
// alive. Returns one Orphan entry per input PID describing what happened.
// The function never errors: a process that's already gone, or that we
// lack permission to signal, ends up as Exited=true with Signal recording
// what we attempted.
func cleanupOrphans(pids []int) []Orphan {
	if len(pids) == 0 {
		return nil
	}
	out := make([]Orphan, 0, len(pids))
	for _, pid := range pids {
		out = append(out, killAndWait(pid))
	}
	return out
}

func killAndWait(pid int) Orphan {
	o := Orphan{PID: pid, Signal: "SIGTERM"}
	proc, err := os.FindProcess(pid)
	if err != nil {
		o.Exited = true
		return o
	}
	start := time.Now()
	// Liveness check — if the process is already gone, record exited
	// without a signal so the pretty output is honest.
	if err := proc.Signal(syscall.Signal(0)); err != nil {
		o.Exited = true
		o.ElapsedMs = time.Since(start).Milliseconds()
		return o
	}
	_ = proc.Signal(syscall.SIGTERM)
	if waitForExit(proc, orphanGracePerProc) {
		o.Exited = true
		o.ElapsedMs = time.Since(start).Milliseconds()
		return o
	}
	// SIGTERM didn't take — escalate. The ticket explicitly prefers
	// loud, immediate cleanup over leaving a stale daemon around to
	// silently serve old behavior on later calls.
	o.Signal = "SIGKILL"
	_ = proc.Signal(syscall.SIGKILL)
	if waitForExit(proc, orphanGracePerProc) {
		o.Exited = true
	}
	o.ElapsedMs = time.Since(start).Milliseconds()
	return o
}

func waitForExit(proc *os.Process, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := proc.Signal(syscall.Signal(0)); err != nil {
			return true
		}
		time.Sleep(pollInterval)
	}
	return proc.Signal(syscall.Signal(0)) != nil
}
