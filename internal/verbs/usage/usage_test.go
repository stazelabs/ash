package usage

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stazelabs/ash/internal/ledger"
)

func openTestLedger(t *testing.T) *ledger.Ledger {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ledger.db")
	l, err := ledger.Open(path, "/test/root", "test")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { l.Close() })
	return l
}

func TestParseArgs_RejectsAllZero(t *testing.T) {
	if _, perr := ParseArgs(map[string]any{}); perr == nil {
		t.Fatal("expected error when both hit and miss are 0")
	} else if perr.Code != "args" {
		t.Errorf("want code=args, got %q", perr.Code)
	}
}

func TestParseArgs_AcceptsOnlyHit(t *testing.T) {
	a, perr := ParseArgs(map[string]any{"hit": 100})
	if perr != nil {
		t.Fatalf("unexpected: %+v", perr)
	}
	if a.Hit != 100 || a.Miss != 0 {
		t.Errorf("hit=%d miss=%d (want 100/0)", a.Hit, a.Miss)
	}
}

func TestParseArgs_AcceptsForRequestID(t *testing.T) {
	a, perr := ParseArgs(map[string]any{"hit": 1, "for": uint64(42)})
	if perr != nil {
		t.Fatalf("unexpected: %+v", perr)
	}
	if a.For != 42 {
		t.Errorf("for=%d want 42", a.For)
	}
}

func TestRunWithLedger_AnnotatesMostRecentNonUsageRow(t *testing.T) {
	l := openTestLedger(t)
	if _, err := l.Record(&ledger.Call{Timestamp: time.Now(), RequestID: 1, Verb: "find", OK: true}); err != nil {
		t.Fatalf("Record 1: %v", err)
	}
	grepRow, err := l.Record(&ledger.Call{Timestamp: time.Now(), RequestID: 2, Verb: "grep", OK: true})
	if err != nil {
		t.Fatalf("Record 2: %v", err)
	}

	r, perr := RunWithLedger(l, &Args{Hit: 1500, Miss: 30})
	if perr != nil {
		t.Fatalf("RunWithLedger: %+v", perr)
	}
	if r.RowID != grepRow {
		t.Errorf("row: got %d want %d", r.RowID, grepRow)
	}
	if r.RequestID != 2 || r.Verb != "grep" {
		t.Errorf("annotated wrong row: req=%d verb=%q", r.RequestID, r.Verb)
	}
	if r.Hit != 1500 || r.Miss != 30 {
		t.Errorf("hit/miss: got %d/%d want 1500/30", r.Hit, r.Miss)
	}

	calls, err := l.QueryRecent(10, "")
	if err != nil {
		t.Fatalf("QueryRecent: %v", err)
	}
	var got *ledger.Call
	for i := range calls {
		if calls[i].RowID == grepRow {
			got = &calls[i]
			break
		}
	}
	if got == nil {
		t.Fatal("grep row missing from QueryRecent")
	}
	if got.TokensCacheHit != 1500 || got.TokensCacheMiss != 30 {
		t.Errorf("ledger after annotate: hit=%d miss=%d (want 1500/30)",
			got.TokensCacheHit, got.TokensCacheMiss)
	}
}

func TestRunWithLedger_AnnotatesByRequestID(t *testing.T) {
	l := openTestLedger(t)
	findRow, err := l.Record(&ledger.Call{Timestamp: time.Now(), RequestID: 100, Verb: "find", OK: true})
	if err != nil {
		t.Fatalf("Record 1: %v", err)
	}
	if _, err := l.Record(&ledger.Call{Timestamp: time.Now(), RequestID: 200, Verb: "grep", OK: true}); err != nil {
		t.Fatalf("Record 2: %v", err)
	}

	r, perr := RunWithLedger(l, &Args{Hit: 7, Miss: 8, For: 100})
	if perr != nil {
		t.Fatalf("RunWithLedger: %+v", perr)
	}
	if r.RowID != findRow {
		t.Errorf("row: got %d want %d (the find row, not the most recent)", r.RowID, findRow)
	}
	if r.Verb != "find" {
		t.Errorf("verb: got %q want find", r.Verb)
	}
}

func TestRunWithLedger_RejectsAnnotatingUsageRow(t *testing.T) {
	l := openTestLedger(t)
	if _, err := l.Record(&ledger.Call{Timestamp: time.Now(), RequestID: 1, Verb: "usage", OK: true}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	_, perr := RunWithLedger(l, &Args{Hit: 1, For: 1})
	if perr == nil {
		t.Fatal("expected error when annotating a usage row")
	}
	if perr.Code != "args" {
		t.Errorf("code: got %q want args", perr.Code)
	}
}

func TestRunWithLedger_NotFound(t *testing.T) {
	l := openTestLedger(t)

	// Empty session, no prior row.
	_, perr := RunWithLedger(l, &Args{Hit: 1})
	if perr == nil || perr.Code != "not_found" {
		t.Errorf("empty session: code=%v want not_found", perr)
	}

	// Missing request_id.
	if _, err := l.Record(&ledger.Call{Timestamp: time.Now(), RequestID: 1, Verb: "find", OK: true}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	_, perr = RunWithLedger(l, &Args{Hit: 1, For: 9999})
	if perr == nil || perr.Code != "not_found" {
		t.Errorf("bad request_id: code=%v want not_found", perr)
	}
}
