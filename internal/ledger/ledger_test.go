package ledger

import (
	"path/filepath"
	"testing"
	"time"
)

// TestRecord_DetectsClosedDB locks in the contract the daemon depends on:
// when the underlying database is unwritable, Record must return a non-nil
// error so the daemon can surface it via Metrics.LedgerError. This is the
// "loud failure" path for instrumentation.
func TestRecord_DetectsClosedDB(t *testing.T) {
	dir := t.TempDir()
	led, err := Open(filepath.Join(dir, "ledger.db"), dir, "test")
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	// Successful record on a healthy DB.
	if _, err := led.Record(&Call{
		RequestID:    1,
		Timestamp:    time.Now(),
		Verb:         "read",
		OK:           true,
		TokensMethod: TokensMethod,
	}); err != nil {
		t.Fatalf("first record on healthy DB failed: %v", err)
	}

	// Close the DB and assert the next Record errors out loudly.
	if err := led.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	_, err = led.Record(&Call{
		RequestID: 2,
		Timestamp: time.Now(),
		Verb:      "read",
		OK:        true,
	})
	if err == nil {
		t.Fatal("expected non-nil error from Record after Close, got nil")
	}
}

func TestOpen_CreatesDirAndSchema(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "subdir", "ledger.db")
	led, err := Open(path, dir, "test")
	if err != nil {
		t.Fatalf("Open should create parent dirs: %v", err)
	}
	defer led.Close()
	if led.SessionID() == "" {
		t.Error("expected non-empty session id")
	}
	if led.Counter() == nil {
		t.Error("expected non-nil counter")
	}
}
