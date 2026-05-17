package lsp

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// goplsAvailable reports whether the gopls binary can be resolved on
// $PATH. The integration tests below skip cleanly when it isn't, so the
// regular `go test ./...` run on a developer box without gopls is still
// green.
func goplsAvailable(t *testing.T) string {
	t.Helper()
	bin, err := exec.LookPath("gopls")
	if err != nil {
		t.Skip("gopls not on PATH; skipping integration test")
	}
	return bin
}

// goModWorkspace builds a minimal valid Go module at root so gopls
// initializes without complaint. The fixture is a single package "p"
// with one exported symbol.
func goModWorkspace(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "go.mod"), "module example.test/p\n\ngo 1.21\n")
	mustWrite(t, filepath.Join(dir, "p.go"), "package p\n\n// Hello is the greeted name.\nfunc Hello() string { return \"hi\" }\n")
	return dir
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestBrokerDisabled(t *testing.T) {
	b := New(Config{Enabled: false, Root: t.TempDir()})
	if err := b.Ensure(context.Background()); err == nil {
		t.Fatalf("Ensure on disabled broker should return lsp_disabled")
	} else {
		var lerr *Error
		if !errors.As(err, &lerr) || lerr.Code != "lsp_disabled" {
			t.Fatalf("want lsp_disabled, got %v", err)
		}
	}
	// Notify on a disabled broker is a no-op.
	b.Notify(context.Background(), "/does/not/exist.go")
}

// TestBrokerInitAndDocumentSymbol covers the headline ASH-136 verification
// bullet: spawn gopls, complete the initialize handshake, and answer a
// trivial textDocument/documentSymbol round-trip.
func TestBrokerInitAndDocumentSymbol(t *testing.T) {
	goplsAvailable(t)
	root := goModWorkspace(t)

	var initDur time.Duration
	var initErr error
	var cbWG sync.WaitGroup
	cbWG.Add(1)
	b := New(
		Config{Enabled: true, Root: root},
		WithInitCallback(func(d time.Duration, err error) {
			initDur = d
			initErr = err
			cbWG.Done()
		}),
	)
	defer b.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := b.Ensure(ctx); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	cbWG.Wait()
	if initErr != nil {
		t.Fatalf("init callback reports error: %v", initErr)
	}
	if initDur <= 0 {
		t.Fatalf("init callback reports zero duration; want > 0")
	}
	if b.LastInit() != initDur {
		t.Fatalf("LastInit=%v want %v", b.LastInit(), initDur)
	}

	// Drive a didOpen via Notify so gopls has the file in its
	// in-memory view, then ask for symbols. Notify is the same path
	// write/edit use through the sink.
	pgoPath := filepath.Join(root, "p.go")
	b.Notify(ctx, pgoPath)

	// documentSymbol response shape varies (SymbolInformation[] vs
	// DocumentSymbol[]); decoding the raw payload as a generic slice is
	// enough — we only need to confirm gopls returned at least one
	// symbol for the file.
	var symbols []map[string]any
	uri := pathToURI(pgoPath)
	if err := b.Request(ctx, "textDocument/documentSymbol", map[string]any{
		"textDocument": map[string]any{"uri": uri},
	}, &symbols); err != nil {
		t.Fatalf("documentSymbol: %v", err)
	}
	if len(symbols) == 0 {
		t.Fatalf("documentSymbol returned no symbols; want >= 1")
	}
}

// TestBrokerCloseIsIdempotent verifies that Close can be called more
// than once (e.g. defer + signal-handler path) without error.
func TestBrokerCloseIsIdempotent(t *testing.T) {
	goplsAvailable(t)
	root := goModWorkspace(t)
	b := New(Config{Enabled: true, Root: root})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := b.Ensure(ctx); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if err := b.Close(); err != nil {
		t.Fatalf("Close 1: %v", err)
	}
	if err := b.Close(); err != nil {
		t.Fatalf("Close 2 (idempotent): %v", err)
	}
}

// TestBrokerRespawnAfterKill covers the verification bullet "killing
// gopls mid-session triggers a re-spawn on the next call". We start the
// broker, capture the subprocess PID, SIGKILL it, and expect the next
// Ensure to spawn a fresh gopls (different PID).
func TestBrokerRespawnAfterKill(t *testing.T) {
	goplsAvailable(t)
	root := goModWorkspace(t)
	b := New(Config{Enabled: true, Root: root})
	defer b.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := b.Ensure(ctx); err != nil {
		t.Fatalf("Ensure (initial): %v", err)
	}
	firstPID := func() int {
		b.mu.Lock()
		defer b.mu.Unlock()
		if b.cmd == nil || b.cmd.Process == nil {
			return 0
		}
		return b.cmd.Process.Pid
	}()
	if firstPID == 0 {
		t.Fatalf("first gopls subprocess has no PID")
	}

	// Kill the subprocess and wait for the reader goroutine to notice.
	proc, err := os.FindProcess(firstPID)
	if err != nil {
		t.Fatalf("FindProcess: %v", err)
	}
	if err := proc.Kill(); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	// Wait for the cmd.Wait() in the reader path to flip ProcessState.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		b.mu.Lock()
		alive := b.alive()
		b.mu.Unlock()
		if !alive {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if err := b.Ensure(ctx); err != nil {
		t.Fatalf("Ensure (after kill): %v", err)
	}
	secondPID := func() int {
		b.mu.Lock()
		defer b.mu.Unlock()
		return b.cmd.Process.Pid
	}()
	if secondPID == firstPID {
		t.Fatalf("respawn produced same PID %d; want a fresh subprocess", secondPID)
	}
}

// TestBrokerGoplsNotFound covers the "no gopls binary" error code.
func TestBrokerGoplsNotFound(t *testing.T) {
	b := New(Config{Enabled: true, Root: t.TempDir(), GoplsPath: "/definitely/does/not/exist/gopls"})
	defer b.Close()
	err := b.Ensure(context.Background())
	if err == nil {
		t.Fatalf("Ensure with bogus gopls path should fail")
	}
	var lerr *Error
	if !errors.As(err, &lerr) || lerr.Code != "gopls_not_found" {
		t.Fatalf("want gopls_not_found, got %v", err)
	}
}

// TestNotifySinkRoundTrip exercises SetSink + Notify without spawning
// gopls — just confirms the indirection wires up the path correctly.
func TestNotifySinkRoundTrip(t *testing.T) {
	var got string
	var mu sync.Mutex
	SetSink(func(p string) {
		mu.Lock()
		defer mu.Unlock()
		got = p
	})
	t.Cleanup(func() { SetSink(nil) })
	Notify("/tmp/example.go")
	mu.Lock()
	defer mu.Unlock()
	if got != "/tmp/example.go" {
		t.Fatalf("sink received %q; want /tmp/example.go", got)
	}
}

// TestPathToURIShape sanity-checks the LSP URI form for an absolute
// posix path — gopls is picky about its workspace folder URIs.
func TestPathToURIShape(t *testing.T) {
	got := pathToURI("/Users/x/p.go")
	want := "file:///Users/x/p.go"
	if got != want {
		t.Fatalf("pathToURI = %q; want %q", got, want)
	}
}
