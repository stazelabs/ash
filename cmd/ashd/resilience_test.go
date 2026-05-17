package main

// ASH-49 daemon resilience tests: read deadline, graceful shutdown drain,
// and the optional concurrency cap. The legacy net.Pipe + handle() path
// already covers happy-case dispatch (see main_test.go); these exercise
// the defensive behavior under hostile conditions a long-lived daemon
// will eventually face.

import (
	"net"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stazelabs/ash/internal/ledger"
	"github.com/stazelabs/ash/internal/verbs"
)

// TestHandle_ReadDeadlineExpires verifies that an idle connection times
// out per ASH-49: handle() must return when no frame arrives within the
// deadline rather than pinning the goroutine forever. Without the fix,
// this test would hang until t.Fatal() fires the test timeout.
func TestHandle_ReadDeadlineExpires(t *testing.T) {
	dir := t.TempDir()
	led, err := ledger.Open(filepath.Join(dir, "ledger.db"), dir, "test")
	if err != nil {
		t.Fatalf("ledger.Open: %v", err)
	}
	defer led.Close()
	runners := verbs.Runners(led, nil, time.Time{}, "", nil, nil)
	pretty := verbs.PrettyHandlers()

	srv, cli := net.Pipe()
	defer cli.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		handle(srv, led, runners, pretty, 50*time.Millisecond)
	}()

	// Never send a frame. handle() should hit the read deadline and exit.
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handle did not return after read deadline expired")
	}
}

// TestDrainHandlers_CleanReturn covers the happy path: all handlers
// finish before grace expires, drainHandlers returns true.
func TestDrainHandlers_CleanReturn(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		time.Sleep(20 * time.Millisecond)
	}()
	if !drainHandlers(&wg, time.Second) {
		t.Errorf("expected clean drain")
	}
}

// TestDrainHandlers_GraceExceeded covers the loud path: a handler that
// outlasts grace causes drainHandlers to return false. The goroutine is
// abandoned (wg never reaches zero); we don't care once the test is
// done because the process exits.
func TestDrainHandlers_GraceExceeded(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		time.Sleep(500 * time.Millisecond)
	}()
	if drainHandlers(&wg, 50*time.Millisecond) {
		t.Errorf("expected grace-exceeded false return")
	}
	wg.Wait() // tidy up so -race is happy.
}

// TestDrainHandlers_ZeroGrace returns false immediately without waiting.
// The "no grace configured" semantic.
func TestDrainHandlers_ZeroGrace(t *testing.T) {
	var wg sync.WaitGroup
	start := time.Now()
	if drainHandlers(&wg, 0) {
		t.Errorf("expected false on zero grace")
	}
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Errorf("zero grace should return immediately, took %v", elapsed)
	}
}

// TestAcceptLoop_GracefulDrain wires acceptLoop against a real UDS listener,
// dispatches slow handlers, closes the listener (the canonical shutdown
// path), and verifies drainHandlers waits for them to finish.
func TestAcceptLoop_GracefulDrain(t *testing.T) {
	tmp := t.TempDir()
	sock := filepath.Join(tmp, "drain.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	var inflight atomic.Int32
	var completed atomic.Int32
	handler := func(conn net.Conn) {
		defer conn.Close()
		inflight.Add(1)
		defer inflight.Add(-1)
		// Simulate work that outlasts the listener-close moment.
		time.Sleep(80 * time.Millisecond)
		completed.Add(1)
	}

	var wg sync.WaitGroup
	loopDone := make(chan struct{})
	go func() {
		defer close(loopDone)
		acceptLoop(ln, nil, &wg, handler)
	}()

	// Spin up a few connections, each kicking off a slow handler.
	const N = 3
	conns := make([]net.Conn, 0, N)
	for i := 0; i < N; i++ {
		c, err := net.DialTimeout("unix", sock, time.Second)
		if err != nil {
			t.Fatalf("dial %d: %v", i, err)
		}
		conns = append(conns, c)
	}
	// Give the loop a chance to dispatch all of them.
	time.Sleep(20 * time.Millisecond)
	if got := inflight.Load(); got < N {
		t.Logf("only %d/%d handlers running yet — proceeding", got, N)
	}

	// Trigger shutdown.
	ln.Close()
	for _, c := range conns {
		c.Close()
	}

	// acceptLoop should return promptly once the listener errors.
	select {
	case <-loopDone:
	case <-time.After(time.Second):
		t.Fatal("acceptLoop did not return after listener close")
	}

	// drainHandlers with a generous grace should drain cleanly.
	if !drainHandlers(&wg, 2*time.Second) {
		t.Fatalf("expected clean drain; %d completed of %d", completed.Load(), N)
	}
	if got := completed.Load(); int(got) != N {
		t.Errorf("expected %d handlers to complete, got %d", N, got)
	}
}

// TestAcceptLoop_SemaphoreCap verifies that the optional concurrency cap
// actually blocks: with cap=1 and a slow handler, a second connection's
// handler must not start until the first finishes.
func TestAcceptLoop_SemaphoreCap(t *testing.T) {
	tmp := t.TempDir()
	sock := filepath.Join(tmp, "sem.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	var concurrent atomic.Int32
	var maxObserved atomic.Int32
	handler := func(conn net.Conn) {
		defer conn.Close()
		c := concurrent.Add(1)
		for {
			old := maxObserved.Load()
			if c <= old || maxObserved.CompareAndSwap(old, c) {
				break
			}
		}
		time.Sleep(80 * time.Millisecond)
		concurrent.Add(-1)
	}

	sem := make(chan struct{}, 1)
	var wg sync.WaitGroup
	go acceptLoop(ln, sem, &wg, handler)

	const N = 4
	for i := 0; i < N; i++ {
		c, err := net.DialTimeout("unix", sock, time.Second)
		if err != nil {
			t.Fatalf("dial %d: %v", i, err)
		}
		t.Cleanup(func() { c.Close() })
	}

	// Wait for all to complete by polling concurrent + maxObserved.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if maxObserved.Load() >= 1 && concurrent.Load() == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if got := maxObserved.Load(); got != 1 {
		t.Errorf("with cap=1, max observed concurrent handlers should be 1, got %d", got)
	}

	ln.Close()
	wg.Wait()
}
