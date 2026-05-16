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

// ASH-71: a recorded TokensOutNoPrefix value must round-trip through
// the INSERT and through both QueryWindow and QueryRecent scans. This
// is the regression test for the schema/INSERT/SELECT triple needing
// to stay in sync as the column count grows.
func TestRecord_TokensOutNoPrefix_RoundTrip(t *testing.T) {
	l := openTestLedger(t)
	_, err := l.Record(&Call{
		Timestamp:         time.Now(),
		Verb:              "find",
		OK:                true,
		TokensOut:         100,
		TokensOutNoPrefix: 40,
	})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}

	winCalls, err := l.QueryWindow(QueryOpts{})
	if err != nil {
		t.Fatalf("QueryWindow: %v", err)
	}
	if len(winCalls) != 1 {
		t.Fatalf("QueryWindow rows: want 1, got %d", len(winCalls))
	}
	if winCalls[0].TokensOut != 100 || winCalls[0].TokensOutNoPrefix != 40 {
		t.Errorf("QueryWindow: tokens_out=%d tokens_out_no_prefix=%d (want 100/40)",
			winCalls[0].TokensOut, winCalls[0].TokensOutNoPrefix)
	}

	recCalls, err := l.QueryRecent(10, "")
	if err != nil {
		t.Fatalf("QueryRecent: %v", err)
	}
	if len(recCalls) != 1 {
		t.Fatalf("QueryRecent rows: want 1, got %d", len(recCalls))
	}
	if recCalls[0].TokensOutNoPrefix != 40 {
		t.Errorf("QueryRecent: tokens_out_no_prefix=%d (want 40)", recCalls[0].TokensOutNoPrefix)
	}
}

// ASH-108: tokens_cache_hit / tokens_cache_miss must survive Record
// and both query paths. The columns are reserved for harness-reported
// prompt-cache accounting; daemon code does not populate them today,
// but the schema, INSERT, and SELECT triple must agree so a future
// feedback path can write them without a migration.
func TestRecord_CacheTokens_RoundTrip(t *testing.T) {
	l := openTestLedger(t)
	_, err := l.Record(&Call{
		Timestamp:       time.Now(),
		Verb:            "grep",
		OK:              true,
		TokensCacheHit:  9000,
		TokensCacheMiss: 100,
	})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}

	winCalls, err := l.QueryWindow(QueryOpts{})
	if err != nil {
		t.Fatalf("QueryWindow: %v", err)
	}
	if len(winCalls) != 1 {
		t.Fatalf("QueryWindow rows: want 1, got %d", len(winCalls))
	}
	if winCalls[0].TokensCacheHit != 9000 || winCalls[0].TokensCacheMiss != 100 {
		t.Errorf("QueryWindow: cache_hit=%d cache_miss=%d (want 9000/100)",
			winCalls[0].TokensCacheHit, winCalls[0].TokensCacheMiss)
	}

	recCalls, err := l.QueryRecent(10, "")
	if err != nil {
		t.Fatalf("QueryRecent: %v", err)
	}
	if len(recCalls) != 1 {
		t.Fatalf("QueryRecent rows: want 1, got %d", len(recCalls))
	}
	if recCalls[0].TokensCacheHit != 9000 || recCalls[0].TokensCacheMiss != 100 {
		t.Errorf("QueryRecent: cache_hit=%d cache_miss=%d (want 9000/100)",
			recCalls[0].TokensCacheHit, recCalls[0].TokensCacheMiss)
	}
}

// ASH-134: UpdateCacheStats writes hit/miss onto an already-recorded row,
// and the lookup helpers find the row by request_id or by session-recency.
// This is the round-trip test the ticket calls out as verification.
func TestUpdateCacheStats_RoundTrip(t *testing.T) {
	l := openTestLedger(t)
	rowID, err := l.Record(&Call{
		Timestamp: time.Now(),
		RequestID: 42,
		Verb:      "grep",
		OK:        true,
	})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}

	if err := l.UpdateCacheStats(rowID, 1234, 56); err != nil {
		t.Fatalf("UpdateCacheStats: %v", err)
	}

	calls, err := l.QueryRecent(10, "")
	if err != nil {
		t.Fatalf("QueryRecent: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("QueryRecent rows: want 1, got %d", len(calls))
	}
	if calls[0].TokensCacheHit != 1234 || calls[0].TokensCacheMiss != 56 {
		t.Errorf("after UpdateCacheStats: hit=%d miss=%d (want 1234/56)",
			calls[0].TokensCacheHit, calls[0].TokensCacheMiss)
	}
}

func TestFindRowByRequestID(t *testing.T) {
	l := openTestLedger(t)
	_, err := l.Record(&Call{Timestamp: time.Now(), RequestID: 100, Verb: "find", OK: true})
	if err != nil {
		t.Fatalf("Record 1: %v", err)
	}
	wantRow, err := l.Record(&Call{Timestamp: time.Now(), RequestID: 200, Verb: "grep", OK: true})
	if err != nil {
		t.Fatalf("Record 2: %v", err)
	}

	gotRow, verb, err := l.FindRowByRequestID(200)
	if err != nil {
		t.Fatalf("FindRowByRequestID: %v", err)
	}
	if gotRow != wantRow {
		t.Errorf("row: got %d want %d", gotRow, wantRow)
	}
	if verb != "grep" {
		t.Errorf("verb: got %q want %q", verb, "grep")
	}

	missRow, _, err := l.FindRowByRequestID(9999)
	if err != nil {
		t.Fatalf("FindRowByRequestID (miss): %v", err)
	}
	if missRow != 0 {
		t.Errorf("expected 0 row id for missing request, got %d", missRow)
	}
}

func TestFindMostRecentNonUsageRow(t *testing.T) {
	l := openTestLedger(t)
	_, err := l.Record(&Call{Timestamp: time.Now(), RequestID: 1, Verb: "find", OK: true})
	if err != nil {
		t.Fatalf("Record 1: %v", err)
	}
	wantRow, err := l.Record(&Call{Timestamp: time.Now(), RequestID: 2, Verb: "grep", OK: true})
	if err != nil {
		t.Fatalf("Record 2: %v", err)
	}
	// A usage row after grep — the helper must skip it.
	_, err = l.Record(&Call{Timestamp: time.Now(), RequestID: 3, Verb: "usage", OK: true})
	if err != nil {
		t.Fatalf("Record 3: %v", err)
	}

	gotRow, gotReq, gotVerb, err := l.FindMostRecentNonUsageRow(0)
	if err != nil {
		t.Fatalf("FindMostRecentNonUsageRow: %v", err)
	}
	if gotRow != wantRow {
		t.Errorf("row: got %d want %d", gotRow, wantRow)
	}
	if gotReq != 2 {
		t.Errorf("req: got %d want 2", gotReq)
	}
	if gotVerb != "grep" {
		t.Errorf("verb: got %q want %q", gotVerb, "grep")
	}
}

