package stop

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// withFakeLister swaps processLister for the duration of the test, restoring
// it on Cleanup. Tests use this to inject synthetic process listings so the
// orphan-detection logic can be exercised without forging ashd argv.
func withFakeLister(t *testing.T, procs []processInfo) {
	t.Helper()
	prev := processLister
	processLister = func() ([]processInfo, error) { return procs, nil }
	t.Cleanup(func() { processLister = prev })
}

// spawnSleeper starts a `sleep 30` child and returns it. A background
// goroutine reaps the zombie when the child exits — without that, the
// child shows up as alive in `kill(pid, 0)` forever (the PID stays in
// the process table until reaped), so the polling-based liveness check
// orphan cleanup uses would never confirm the exit. Real daemons are not
// children of `ash stop`, so the production code never hits this case.
func spawnSleeper(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("spawn sleeper: %v", err)
	}
	go func() { _ = cmd.Wait() }()
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	})
	return cmd
}

func waitGone(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err != nil {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return syscall.Kill(pid, 0) != nil
}

func TestMatchesAshdSocket(t *testing.T) {
	const sock = "/tmp/ash-abc.sock"
	cases := []struct {
		name    string
		cmdline string
		want    bool
	}{
		{"flag separated", "/usr/local/bin/ashd --root /p --socket " + sock, true},
		{"flag equals", "bin/ashd --socket=" + sock, true},
		{"bare ashd", "ashd --root /p --socket " + sock, true},
		{"wrong socket", "ashd --socket /tmp/ash-other.sock", false},
		{"not ashd", "tail -f " + sock, false},
		{"ashd suffix only", "myashd --socket " + sock, false},
		{"empty", "", false},
		{"missing socket arg", "ashd --root /p", false},
		{"socket flag without value", "ashd --socket", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := matchesAshdSocket(tc.cmdline, sock); got != tc.want {
				t.Errorf("matchesAshdSocket(%q) = %v, want %v", tc.cmdline, got, tc.want)
			}
		})
	}
}

func TestFindOrphanDaemons_FiltersExcludedAndSelf(t *testing.T) {
	sock := "/tmp/ash-deadbeef.sock"
	self := os.Getpid()
	withFakeLister(t, []processInfo{
		{PID: 1001, Cmdline: "bin/ashd --root /p --socket " + sock},
		{PID: 1002, Cmdline: "bin/ashd --socket=" + sock},
		{PID: 1003, Cmdline: "bin/ashd --socket /tmp/ash-other.sock"},
		{PID: 1004, Cmdline: "tail -f " + sock},
		{PID: self, Cmdline: "bin/ashd --socket " + sock},
	})

	got := findOrphanDaemons(sock, map[int]bool{1001: true})
	want := []int{1002}
	if len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("findOrphanDaemons: got %v, want %v", got, want)
	}
}

func TestFindOrphanDaemons_EmptySock(t *testing.T) {
	withFakeLister(t, []processInfo{{PID: 1, Cmdline: "ashd --socket /tmp/x"}})
	if got := findOrphanDaemons("", nil); got != nil {
		t.Fatalf("empty sock should return nil, got %v", got)
	}
}

func TestStopDaemon_AlreadyStopped_NoPidfile(t *testing.T) {
	dir := t.TempDir()
	r, err := StopDaemon(filepath.Join(dir, "ashd.pid"), "")
	if err != nil {
		t.Fatalf("StopDaemon: %v", err)
	}
	if r.Status != "already_stopped" {
		t.Fatalf("status: got %q, want already_stopped", r.Status)
	}
}

func TestStopDaemon_CleansUpOrphansAndUnlinksSocket(t *testing.T) {
	// One "primary" sleeper (pidfile-pointed) and one "orphan" sleeper
	// both pretend to be ashd processes bound to sockPath. The fake
	// process lister names them with synthesized argv so the orphan
	// scan picks up the orphan and skips the primary.
	primary := spawnSleeper(t)
	orphan := spawnSleeper(t)

	dir := t.TempDir()
	pidPath := filepath.Join(dir, "ashd.pid")
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(primary.Process.Pid)), 0o644); err != nil {
		t.Fatalf("write pidfile: %v", err)
	}
	sockPath := filepath.Join(dir, "ash.sock")
	if err := os.WriteFile(sockPath, nil, 0o644); err != nil {
		t.Fatalf("create fake socket: %v", err)
	}

	withFakeLister(t, []processInfo{
		{PID: primary.Process.Pid, Cmdline: "bin/ashd --socket " + sockPath},
		{PID: orphan.Process.Pid, Cmdline: "bin/ashd --socket " + sockPath},
	})

	r, err := StopDaemon(pidPath, sockPath)
	if err != nil {
		t.Fatalf("StopDaemon: %v", err)
	}
	if r.Status != "stopped" {
		t.Fatalf("status: got %q, want stopped (result=%+v)", r.Status, r)
	}
	if r.PID != primary.Process.Pid {
		t.Fatalf("primary pid: got %d, want %d", r.PID, primary.Process.Pid)
	}
	if !waitGone(primary.Process.Pid, 2*time.Second) {
		t.Fatalf("primary pid %d still alive after stop", primary.Process.Pid)
	}
	if len(r.Orphans) != 1 {
		t.Fatalf("orphans: got %d, want 1 (result=%+v)", len(r.Orphans), r)
	}
	o := r.Orphans[0]
	if o.PID != orphan.Process.Pid {
		t.Fatalf("orphan pid: got %d, want %d", o.PID, orphan.Process.Pid)
	}
	if !o.Exited {
		t.Fatalf("orphan reported as not exited: %+v", o)
	}
	if !waitGone(orphan.Process.Pid, 2*time.Second) {
		t.Fatalf("orphan pid %d still alive after stop", orphan.Process.Pid)
	}
	if !r.SocketUnlinked {
		t.Fatalf("expected socket_unlinked=true (result=%+v)", r)
	}
	if _, err := os.Stat(sockPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket file still present: %v", err)
	}
}

func TestStopDaemon_NoPrimaryStillCleansOrphans(t *testing.T) {
	// PID file points at a long-dead PID. Orphans should still be
	// signalled and the socket file unlinked — this is the "stop
	// reliably cleans up the mess that already exists" property the
	// ticket asks for.
	orphan := spawnSleeper(t)

	dir := t.TempDir()
	pidPath := filepath.Join(dir, "ashd.pid")
	// PID 1 is init; signalling it would fail anyway, but to be safe
	// we use a PID we know is dead: spawn a sleeper and immediately
	// wait for it, then reuse its PID.
	dead := exec.Command("sleep", "0")
	if err := dead.Run(); err != nil {
		t.Fatalf("dead sleeper: %v", err)
	}
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(dead.ProcessState.Pid())), 0o644); err != nil {
		t.Fatalf("write pidfile: %v", err)
	}
	sockPath := filepath.Join(dir, "ash.sock")
	if err := os.WriteFile(sockPath, nil, 0o644); err != nil {
		t.Fatalf("create fake socket: %v", err)
	}

	withFakeLister(t, []processInfo{
		{PID: orphan.Process.Pid, Cmdline: "bin/ashd --socket " + sockPath},
	})

	r, err := StopDaemon(pidPath, sockPath)
	if err != nil {
		t.Fatalf("StopDaemon: %v", err)
	}
	if r.Status != "already_stopped" {
		t.Fatalf("status: got %q, want already_stopped", r.Status)
	}
	if len(r.Orphans) != 1 {
		t.Fatalf("orphans: got %d, want 1", len(r.Orphans))
	}
	if !waitGone(orphan.Process.Pid, 2*time.Second) {
		t.Fatalf("orphan pid %d still alive", orphan.Process.Pid)
	}
	if !r.SocketUnlinked {
		t.Fatalf("expected socket_unlinked=true (result=%+v)", r)
	}
}

func TestFindAshdPIDs_Exported(t *testing.T) {
	sock := "/tmp/ash-cafef00d.sock"
	withFakeLister(t, []processInfo{
		{PID: 2001, Cmdline: "bin/ashd --socket " + sock},
		{PID: 2002, Cmdline: "bin/ashd --root /p --socket=" + sock},
	})
	got := FindAshdPIDs(sock)
	if len(got) != 2 {
		t.Fatalf("FindAshdPIDs: got %v, want 2 entries", got)
	}
}

func TestPrettyResult_OrphansAndSocket(t *testing.T) {
	r := &Result{
		PID:               1234,
		SignalSent:        true,
		ExitedWithinGrace: true,
		ElapsedMs:         42,
		Status:            "stopped",
		Orphans: []Orphan{
			{PID: 5678, Signal: "SIGTERM", Exited: true, ElapsedMs: 100},
			{PID: 9012, Signal: "SIGKILL", Exited: true, ElapsedMs: 2050},
		},
		SocketUnlinked: true,
	}
	out := PrettyResult(r)
	for _, want := range []string{
		"§stop: stopped (42ms)",
		"pid:     1234",
		"orphan:  pid=5678 signal=SIGTERM exited=yes",
		"orphan:  pid=9012 signal=SIGKILL exited=yes",
		"socket:  unlinked",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("PrettyResult missing %q\n%s", want, out)
		}
	}
}
