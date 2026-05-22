package daemoncleanup

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func withFakeLister(t *testing.T, procs []processInfo) {
	t.Helper()
	prev := processLister
	processLister = func() ([]processInfo, error) { return procs, nil }
	t.Cleanup(func() { processLister = prev })
}

func withFakeDialer(t *testing.T, reachable map[string]bool) {
	t.Helper()
	prev := socketDialer
	socketDialer = func(s string) error {
		if reachable[s] {
			return nil
		}
		return errors.New("connection refused")
	}
	t.Cleanup(func() { socketDialer = prev })
}

func withFakeRootExists(t *testing.T, existing map[string]bool) {
	t.Helper()
	prev := rootExists
	rootExists = func(p string) bool { return existing[p] }
	t.Cleanup(func() { rootExists = prev })
}

// spawnSleeper starts a `sleep 30` child whose zombie is reaped in a
// background goroutine — without that, the kill-and-poll cleanup path
// would see the PID lingering in the process table forever. Mirrors the
// pattern in internal/verbs/stop/stop_test.go for the same reasons.
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

func TestExtractFlag(t *testing.T) {
	cases := []struct {
		name, line, flag, want string
		ok                     bool
	}{
		{"separated root", "ashd --root /a --socket /b", "--root", "/a", true},
		{"equals root", "ashd --root=/a --socket=/b", "--root", "/a", true},
		{"separated socket", "ashd --root /a --socket /b", "--socket", "/b", true},
		{"equals socket", "ashd --root=/a --socket=/b", "--socket", "/b", true},
		{"absent", "ashd", "--root", "", false},
		{"trailing flag no value", "ashd --root", "--root", "", false},
		{"prefix-only match", "ashd --rooted /a", "--root", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := extractFlag(tc.line, tc.flag)
			if got != tc.want || ok != tc.ok {
				t.Errorf("extractFlag(%q, %q) = (%q, %v), want (%q, %v)",
					tc.line, tc.flag, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestIsAshdProgram(t *testing.T) {
	cases := []struct {
		arg0 string
		want bool
	}{
		{"ashd", true},
		{"bin/ashd", true},
		{"/usr/local/bin/ashd", true},
		{"./bin/ashd", true},
		{"myashd", false},
		{"ashd-clean", false},
		{"/usr/local/bin/ashd-clean", false},
		{"ash", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := isAshdProgram(tc.arg0); got != tc.want {
			t.Errorf("isAshdProgram(%q) = %v, want %v", tc.arg0, got, tc.want)
		}
	}
}

func TestScan_ClassifiesAliveZombieUnknown(t *testing.T) {
	self := os.Getpid()
	withFakeLister(t, []processInfo{
		{PID: 1001, Cmdline: "bin/ashd --root /alive --socket /tmp/alive.sock"},
		{PID: 1002, Cmdline: "bin/ashd --root /deleted --socket /tmp/dead.sock"},
		{PID: 1003, Cmdline: "bin/ashd --root=/alive --socket=/tmp/unreachable.sock"},
		{PID: 1004, Cmdline: "bin/ashd"},                                  // unknown — no flags
		{PID: 1005, Cmdline: "tail -f /tmp/alive.sock"},                   // not ashd
		{PID: 1006, Cmdline: "/opt/myashd --socket /tmp/x"},               // suffix-only impostor
		{PID: self, Cmdline: "bin/ashd --root /me --socket /tmp/me.sock"}, // self
	})
	withFakeRootExists(t, map[string]bool{"/alive": true})
	withFakeDialer(t, map[string]bool{"/tmp/alive.sock": true})

	got := Scan()
	if len(got) != 4 {
		t.Fatalf("got %d daemons, want 4: %+v", len(got), got)
	}

	byPID := map[int]Daemon{}
	for _, d := range got {
		byPID[d.PID] = d
	}

	if d := byPID[1001]; d.Status != StatusAlive {
		t.Errorf("1001: status=%s, want alive (%+v)", d.Status, d)
	}
	if d := byPID[1002]; d.Status != StatusZombie ||
		!strings.Contains(d.Reason, "root dir missing") {
		t.Errorf("1002: status=%s reason=%q, want zombie + root-missing reason",
			d.Status, d.Reason)
	}
	if d := byPID[1003]; d.Status != StatusZombie || d.Reason != "socket unreachable" {
		t.Errorf("1003: status=%s reason=%q, want zombie + socket-unreachable",
			d.Status, d.Reason)
	}
	if d := byPID[1004]; d.Status != StatusUnknown {
		t.Errorf("1004: status=%s, want unknown (%+v)", d.Status, d)
	}
}

func TestScan_BothBrokenReportsCombined(t *testing.T) {
	withFakeLister(t, []processInfo{
		{PID: 2001, Cmdline: "bin/ashd --root /gone --socket /tmp/gone.sock"},
	})
	withFakeRootExists(t, map[string]bool{})
	withFakeDialer(t, map[string]bool{})

	got := Scan()
	if len(got) != 1 || got[0].Status != StatusZombie {
		t.Fatalf("got %+v, want one zombie", got)
	}
	if got[0].Reason != "root dir missing and socket unreachable" {
		t.Errorf("reason=%q, want combined", got[0].Reason)
	}
}

func TestScan_OnlyRootParseable_NotZombieWhenRootExists(t *testing.T) {
	// A daemon argv where --socket can't be parsed (e.g. truncated by
	// `ps` even with -ww, vanishingly rare) but --root is present and
	// the directory exists. Don't false-positive as a zombie: we have
	// no evidence of brokenness.
	withFakeLister(t, []processInfo{
		{PID: 3001, Cmdline: "bin/ashd --root /alive"},
	})
	withFakeRootExists(t, map[string]bool{"/alive": true})
	withFakeDialer(t, map[string]bool{})

	got := Scan()
	if len(got) != 1 || got[0].Status != StatusAlive {
		t.Fatalf("got %+v, want alive (no socket evidence)", got)
	}
}

func TestScan_PsFailureReturnsNil(t *testing.T) {
	prev := processLister
	processLister = func() ([]processInfo, error) {
		return nil, errors.New("ps blew up")
	}
	t.Cleanup(func() { processLister = prev })
	if got := Scan(); got != nil {
		t.Errorf("Scan() with ps error: got %+v, want nil", got)
	}
}

func TestCleanup_OnlySignalsZombies(t *testing.T) {
	// Two real sleepers: one marked alive, one marked zombie. Cleanup
	// must only signal the zombie. An unknown entry is ignored.
	alive := spawnSleeper(t)
	zombie := spawnSleeper(t)

	daemons := []Daemon{
		{PID: alive.Process.Pid, Status: StatusAlive,
			Root: "/alive", Socket: "/tmp/alive.sock"},
		{PID: zombie.Process.Pid, Status: StatusZombie,
			Root: "/gone", Socket: "/tmp/gone.sock", Reason: "root dir missing"},
		{PID: 99, Status: StatusUnknown},
	}
	killed := Cleanup(daemons)
	if len(killed) != 1 {
		t.Fatalf("got %d killed, want 1 (%+v)", len(killed), killed)
	}
	if killed[0].PID != zombie.Process.Pid {
		t.Fatalf("killed pid %d, want %d", killed[0].PID, zombie.Process.Pid)
	}
	if !killed[0].Exited {
		t.Fatalf("zombie reported as not exited: %+v", killed[0])
	}
	if !waitGone(zombie.Process.Pid, 2*time.Second) {
		t.Fatalf("zombie pid %d still alive after Cleanup", zombie.Process.Pid)
	}
	if err := syscall.Kill(alive.Process.Pid, 0); err != nil {
		t.Fatalf("alive sleeper was signalled by Cleanup: %v", err)
	}
}

func TestCleanup_NoZombiesReturnsNil(t *testing.T) {
	daemons := []Daemon{
		{PID: 1, Status: StatusAlive},
		{PID: 2, Status: StatusUnknown},
	}
	if got := Cleanup(daemons); got != nil {
		t.Errorf("got %+v, want nil", got)
	}
}

func TestScan_RealRootExistsImpl(t *testing.T) {
	// Sanity-check the default rootExists against an actual tempdir,
	// so the production implementation is exercised at least once.
	dir := t.TempDir()
	withFakeLister(t, []processInfo{
		{PID: 4001, Cmdline: "bin/ashd --root " + dir + " --socket /tmp/none.sock"},
	})
	withFakeDialer(t, map[string]bool{})
	// don't override rootExists — exercise the real one
	got := Scan()
	if len(got) != 1 {
		t.Fatalf("got %d, want 1", len(got))
	}
	if got[0].Status != StatusZombie || got[0].Reason != "socket unreachable" {
		t.Fatalf("status=%s reason=%q, want zombie + socket unreachable",
			got[0].Status, got[0].Reason)
	}

	// After removing the dir, both probes fail → combined reason.
	if err := os.Remove(dir); err != nil {
		t.Fatalf("remove tempdir: %v", err)
	}
	got = Scan()
	if got[0].Reason != "root dir missing and socket unreachable" {
		t.Fatalf("after remove: reason=%q", got[0].Reason)
	}
}

func TestDialUnixOnce_ActualSocket(t *testing.T) {
	// Exercise the real dialer so coverage isn't only via the seam.
	sock := filepath.Join(t.TempDir(), "probe.sock")
	if err := dialUnixOnce(sock); err == nil {
		t.Fatalf("dialUnixOnce on missing socket returned nil, want error")
	}
}
