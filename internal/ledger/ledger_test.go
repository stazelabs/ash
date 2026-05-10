package ledger

import (
	"path/filepath"
	"testing"
	"time"
)

func openTestLedger(t *testing.T) *Ledger {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ledger.db")
	l, err := Open(path, "/test/root", "test")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { l.Close() })
	return l
}

func insertCall(t *testing.T, l *Ledger, ts time.Time) {
	t.Helper()
	_, err := l.Record(&Call{
		Timestamp: ts,
		Verb:      "read",
		OK:        true,
	})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
}

func countRows(t *testing.T, l *Ledger, table string) int {
	t.Helper()
	var n int
	if err := l.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func TestCleanup_NoOp(t *testing.T) {
	l := openTestLedger(t)
	insertCall(t, l, time.Now())

	cr, err := l.Cleanup(CleanupCfg{})
	if err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if cr.DeletedCalls != 0 || cr.DeletedSessions != 0 {
		t.Errorf("no-op cleanup deleted something: calls=%d sessions=%d", cr.DeletedCalls, cr.DeletedSessions)
	}
}

func TestCleanup_MaxAge(t *testing.T) {
	l := openTestLedger(t)

	// Insert one old call (40 days ago) and one recent call (5 days ago).
	insertCall(t, l, time.Now().Add(-40*24*time.Hour))
	insertCall(t, l, time.Now().Add(-5*24*time.Hour))

	beforeCalls := countRows(t, l, "calls")
	if beforeCalls != 2 {
		t.Fatalf("expected 2 calls before cleanup, got %d", beforeCalls)
	}

	cr, err := l.Cleanup(CleanupCfg{MaxAge: 30 * 24 * time.Hour})
	if err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if cr.DeletedCalls != 1 {
		t.Errorf("DeletedCalls: want 1, got %d", cr.DeletedCalls)
	}
	if countRows(t, l, "calls") != 1 {
		t.Errorf("expected 1 call remaining after age cleanup")
	}
}

func TestCleanup_MaxRows(t *testing.T) {
	l := openTestLedger(t)
	for i := 0; i < 10; i++ {
		insertCall(t, l, time.Now().Add(-time.Duration(i)*time.Hour))
	}

	cr, err := l.Cleanup(CleanupCfg{MaxRows: 5})
	if err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if cr.DeletedCalls != 5 {
		t.Errorf("DeletedCalls: want 5, got %d", cr.DeletedCalls)
	}
	if countRows(t, l, "calls") != 5 {
		t.Errorf("expected 5 calls remaining")
	}
}

func TestCleanup_MaxRows_NoExcess(t *testing.T) {
	l := openTestLedger(t)
	insertCall(t, l, time.Now())
	insertCall(t, l, time.Now())

	cr, err := l.Cleanup(CleanupCfg{MaxRows: 10})
	if err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if cr.DeletedCalls != 0 {
		t.Errorf("should not delete when under cap, got %d", cr.DeletedCalls)
	}
}

func TestCleanup_OrphanedSessions(t *testing.T) {
	l := openTestLedger(t)

	// Old call from current session — will be age-deleted.
	insertCall(t, l, time.Now().Add(-40*24*time.Hour))

	// Sessions count: current session + the one we just inserted into (same session).
	// After age-deletion, the current session has no calls and would be orphaned,
	// but Cleanup must protect it.
	sessionsBefore := countRows(t, l, "sessions")

	cr, err := l.Cleanup(CleanupCfg{MaxAge: 30 * 24 * time.Hour})
	if err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if cr.DeletedCalls != 1 {
		t.Errorf("DeletedCalls: want 1, got %d", cr.DeletedCalls)
	}
	// Current session must survive even though it has no calls now.
	sessionsAfter := countRows(t, l, "sessions")
	if sessionsAfter != sessionsBefore {
		t.Errorf("current session was deleted: before=%d after=%d", sessionsBefore, sessionsAfter)
	}
}

func TestCleanup_Vacuum(t *testing.T) {
	l := openTestLedger(t)
	insertCall(t, l, time.Now())

	_, err := l.Cleanup(CleanupCfg{Vacuum: true})
	if err != nil {
		t.Fatalf("Cleanup with Vacuum: %v", err)
	}
}
