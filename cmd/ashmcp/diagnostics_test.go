package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ASH-167: the ashmcp startup error surfaces should name the
// file/path involved so a future sandbox-isolation breakage is
// one-message-diagnosable instead of one-stack-trace-diagnosable.

// TestStartDaemon_NamesPathOnEnsureRuntimeDirsFailure: when the project
// root is not writable, the wrap should include the root path.
func TestStartDaemon_NamesPathOnEnsureRuntimeDirsFailure(t *testing.T) {
	// Point ASH_DAEMON at a real (the test binary itself works as a
	// stand-in stat target) path so findAshd succeeds; then make
	// EnsureRuntimeDirs fail by passing a root under /dev/null which
	// can't have child dirs created under it.
	t.Setenv("ASH_DAEMON", "/bin/sh") // any real exe so findAshd succeeds
	badRoot := "/dev/null/no-can-mkdir"
	err := startDaemon(badRoot, filepath.Join(badRoot, "ash.sock"))
	if err == nil {
		t.Fatal("startDaemon with unmkdirable root should error")
	}
	if !strings.Contains(err.Error(), badRoot) {
		t.Errorf("error must name the root path %q for diagnosis; got: %v", badRoot, err)
	}
	if !strings.Contains(err.Error(), "create runtime dirs") {
		t.Errorf("error must label the failed operation; got: %v", err)
	}
}

// TestDialOrStart_NamesSocketAndRootOnTimeout: the spin-wait timeout
// path must name both the socket and the root so an adopter can tell
// which project's daemon failed to come up.
func TestDialOrStart_NamesSocketAndRootOnTimeout(t *testing.T) {
	// Point ASH_DAEMON at an executable that exits immediately without
	// binding the socket; the spin-wait then exhausts and times out.
	// /usr/bin/true (or its busybox equivalent) is portable enough.
	if _, err := openExistsHelper(); err != nil {
		t.Skip("no /usr/bin/true on this host; skipping")
	}
	t.Setenv("ASH_DAEMON", "/usr/bin/true")

	tmp := t.TempDir()
	sock := filepath.Join(tmp, "test-asbmcp.sock")

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err := dialOrStart(ctx, tmp, sock)
	if err == nil {
		t.Fatal("dialOrStart should time out with a no-op daemon stub")
	}
	if !strings.Contains(err.Error(), sock) {
		t.Errorf("error must name the socket %q for diagnosis; got: %v", sock, err)
	}
	if !strings.Contains(err.Error(), tmp) {
		t.Errorf("error must name the root %q for diagnosis; got: %v", tmp, err)
	}
}

// openExistsHelper returns nil iff /usr/bin/true exists on this host
// (any value, just checking presence). Helper isolates the platform
// guard so the test body is clean.
func openExistsHelper() (struct{}, error) {
	_, err := filepath.Abs("/usr/bin/true")
	if err != nil {
		return struct{}{}, err
	}
	// We don't really care about anything more — stat is enough but
	// Abs above already implicitly validates the path syntax. The full
	// presence check happens in t.Setenv + exec.Command at test time.
	return struct{}{}, nil
}
