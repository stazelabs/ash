package metrics

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
		t.Fatalf("ledger.Open: %v", err)
	}
	t.Cleanup(func() { l.Close() })
	return l
}

func record(t *testing.T, l *ledger.Ledger, verb string, ti, to int) {
	t.Helper()
	if _, err := l.Record(&ledger.Call{
		Timestamp: time.Now(),
		Verb:      verb,
		OK:        true,
		TokensIn:  ti,
		TokensOut: to,
	}); err != nil {
		t.Fatalf("Record(%s): %v", verb, err)
	}
}

// TestParseArgs_WireShape verifies that the last int arg accepts a
// string-typed value (the wire shape from CLI parseFlags) and rejects
// garbage. Guards against a future implementation skipping argutil and
// silently breaking the string→int coercion path.
func TestParseArgs_WireShape(t *testing.T) {
	a, perr := ParseArgs(map[string]any{"last": "10"})
	if perr != nil {
		t.Fatalf("string last rejected: %v", perr)
	}
	if a.Last != 10 {
		t.Errorf("last: got %d, want 10", a.Last)
	}
	_, perr = ParseArgs(map[string]any{"last": "abc"})
	if perr == nil {
		t.Error("expected error for last=abc")
	}
}

func TestParseArgs_Defaults(t *testing.T) {
	a, perr := ParseArgs(map[string]any{})
	if perr != nil {
		t.Fatalf("empty args rejected: %v", perr)
	}
	if a.Last != DefaultLast {
		t.Errorf("default Last: got %d, want %d", a.Last, DefaultLast)
	}
	if a.Verb != "" {
		t.Errorf("default Verb: got %q, want empty", a.Verb)
	}
}

func TestParseArgs_HardCapClamps(t *testing.T) {
	a, perr := ParseArgs(map[string]any{"last": MaxLast + 1})
	if perr != nil {
		t.Fatalf("over-cap last rejected: %v", perr)
	}
	if a.Last != MaxLast {
		t.Errorf("expected clamp to MaxLast (%d), got %d", MaxLast, a.Last)
	}
}

func TestRunWithLedger_EmptyLedger(t *testing.T) {
	l := openTestLedger(t)
	r, perr := RunWithLedger(l, &Args{Last: 20})
	if perr != nil {
		t.Fatalf("RunWithLedger: %v", perr)
	}
	if r.Count != 0 || len(r.Rows) != 0 {
		t.Errorf("empty ledger: Count=%d len(Rows)=%d, want 0/0", r.Count, len(r.Rows))
	}
}

func TestRunWithLedger_OrderAndRoundTrip(t *testing.T) {
	l := openTestLedger(t)
	record(t, l, "read", 10, 100)
	record(t, l, "grep", 20, 200)
	record(t, l, "find", 30, 300)

	r, perr := RunWithLedger(l, &Args{Last: 20})
	if perr != nil {
		t.Fatalf("RunWithLedger: %v", perr)
	}
	if r.Count != 3 {
		t.Fatalf("Count: got %d, want 3", r.Count)
	}
	// QueryRecent returns rows in descending insertion order (id DESC).
	wantOrder := []string{"find", "grep", "read"}
	for i, want := range wantOrder {
		if r.Rows[i].Verb != want {
			t.Errorf("Rows[%d].Verb: got %q, want %q", i, r.Rows[i].Verb, want)
		}
	}
	// Token fields should round-trip from ledger.Call to metrics.Row.
	if r.Rows[2].TokensIn != 10 || r.Rows[2].TokensOut != 100 {
		t.Errorf("token round-trip on oldest row: ti=%d to=%d, want 10/100",
			r.Rows[2].TokensIn, r.Rows[2].TokensOut)
	}
}

func TestRunWithLedger_VerbFilter(t *testing.T) {
	l := openTestLedger(t)
	record(t, l, "read", 0, 0)
	record(t, l, "grep", 0, 0)
	record(t, l, "read", 0, 0)
	record(t, l, "find", 0, 0)

	r, perr := RunWithLedger(l, &Args{Last: 20, Verb: "read"})
	if perr != nil {
		t.Fatalf("RunWithLedger: %v", perr)
	}
	if r.Count != 2 {
		t.Fatalf("Count for verb=read: got %d, want 2", r.Count)
	}
	for _, row := range r.Rows {
		if row.Verb != "read" {
			t.Errorf("row leaked through filter: %q", row.Verb)
		}
	}
}

func TestRunWithLedger_LastCap(t *testing.T) {
	l := openTestLedger(t)
	for i := 0; i < 10; i++ {
		record(t, l, "read", 0, 0)
	}

	r, perr := RunWithLedger(l, &Args{Last: 3})
	if perr != nil {
		t.Fatalf("RunWithLedger: %v", perr)
	}
	if r.Count != 3 {
		t.Errorf("Count with Last=3: got %d, want 3", r.Count)
	}
}

// TestResultFromCalls_FieldMapping guards against silent field drops when
// ledger.Call grows a new column. If the Row struct gains a field, this
// test should fail until ResultFromCalls is extended to fill it.
func TestResultFromCalls_FieldMapping(t *testing.T) {
	now := time.Now()
	calls := []ledger.Call{{
		Timestamp:         now,
		Verb:              "grep",
		OK:                false,
		ErrCode:           "args",
		TokensIn:          1,
		TokensOut:         2,
		LatencyExecUs:     3,
		BytesIn:           4,
		BytesOut:          5,
		Truncated:         true,
		WalkUs:            6,
		IOUs:              7,
		RegexUs:           8,
		RegexCompileUs:    9,
		LatencyDispatchUs: 10,
		TokensOutEmit:     11,
		BytesOutEmit:      12,
		TokensCacheHit:    13,
		TokensCacheMiss:   14,
	}}
	r := ResultFromCalls(calls)
	if r.Count != 1 || len(r.Rows) != 1 {
		t.Fatalf("Count=%d len(Rows)=%d, want 1/1", r.Count, len(r.Rows))
	}
	want := Row{
		Timestamp:         now.UnixNano(),
		Verb:              "grep",
		OK:                false,
		ErrCode:           "args",
		TokensIn:          1,
		TokensOut:         2,
		LatencyExecUs:     3,
		BytesIn:           4,
		BytesOut:          5,
		Truncated:         true,
		WalkUs:            6,
		IOUs:              7,
		RegexUs:           8,
		RegexCompileUs:    9,
		LatencyDispatchUs: 10,
		TokensOutEmit:     11,
		BytesOutEmit:      12,
		TokensCacheHit:    13,
		TokensCacheMiss:   14,
	}
	if r.Rows[0] != want {
		t.Errorf("ResultFromCalls mapping drift:\n got  %+v\n want %+v", r.Rows[0], want)
	}
}
