package usage

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stazelabs/ash/internal/ledger"
	"github.com/stazelabs/ash/internal/proto"
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

func TestParseArgs_Defaults(t *testing.T) {
	a, perr := ParseArgs(map[string]any{})
	if perr != nil {
		t.Fatalf("unexpected: %+v", perr)
	}
	if a.Since != DefaultSince {
		t.Errorf("Since: got %v, want %v (default)", a.Since, DefaultSince)
	}
	if a.Session != "current" {
		t.Errorf("Session: got %q, want %q", a.Session, "current")
	}
}

func TestParseArgs_Since(t *testing.T) {
	a, perr := ParseArgs(map[string]any{"since": "1h"})
	if perr != nil {
		t.Fatalf("unexpected: %+v", perr)
	}
	if a.Since != time.Hour {
		t.Errorf("Since: got %v, want 1h", a.Since)
	}
}

func TestParseArgs_RejectsBadSince(t *testing.T) {
	_, perr := ParseArgs(map[string]any{"since": "not-a-duration"})
	if perr == nil || perr.Code != "args" {
		t.Errorf("expected args error; got %+v", perr)
	}
}

// TestRunWithLedger_EmptyReturnsZeroes pins the empty-window contract:
// no calls in the window means Calls=0 and PerVerb empty, but the
// result still carries the requested since/session for context.
func TestRunWithLedger_EmptyReturnsZeroes(t *testing.T) {
	l := openTestLedger(t)
	r, perr := RunWithLedger(l, &Args{Since: time.Hour, Session: "current"})
	if perr != nil {
		t.Fatalf("RunWithLedger: %+v", perr)
	}
	if r.Calls != 0 || len(r.PerVerb) != 0 {
		t.Errorf("empty ledger: got %d calls / %d verbs, want 0/0", r.Calls, len(r.PerVerb))
	}
	if r.Session != "current" {
		t.Errorf("Session not echoed: got %q", r.Session)
	}
}

// TestRunWithLedger_CountsAndUnique covers the basic aggregation —
// total calls and the distinct-args count per verb.
func TestRunWithLedger_CountsAndUnique(t *testing.T) {
	l := openTestLedger(t)
	now := time.Now()
	mk := func(id uint64, verb string, args string, off time.Duration) {
		t.Helper()
		if _, err := l.Record(&ledger.Call{
			Timestamp:   now.Add(off),
			RequestID:   id,
			Verb:        verb,
			ArgsMsgpack: []byte(args),
			OK:          true,
		}); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}
	mk(1, "find", "argA", -10*time.Minute)
	mk(2, "find", "argA", -9*time.Minute)
	mk(3, "find", "argB", -8*time.Minute)
	mk(4, "grep", "argX", -7*time.Minute)

	r, perr := RunWithLedger(l, &Args{Since: time.Hour, Session: "current"})
	if perr != nil {
		t.Fatalf("RunWithLedger: %+v", perr)
	}
	if r.Calls != 4 {
		t.Errorf("Calls: got %d, want 4", r.Calls)
	}
	stats := byVerb(r)
	if stats["find"].Calls != 3 || stats["find"].UniqueArgs != 2 {
		t.Errorf("find stats: %+v; want 3 calls / 2 unique", stats["find"])
	}
	if stats["grep"].Calls != 1 || stats["grep"].UniqueArgs != 1 {
		t.Errorf("grep stats: %+v; want 1 call / 1 unique", stats["grep"])
	}
}

// TestRunWithLedger_CachePairsWithinTTL pins the cache-eligibility
// heuristic: consecutive same-verb same-args calls within the 5-minute
// TTL count as cache pairs; outside that window they don't.
func TestRunWithLedger_CachePairsWithinTTL(t *testing.T) {
	l := openTestLedger(t)
	now := time.Now()
	mk := func(id uint64, off time.Duration) {
		t.Helper()
		if _, err := l.Record(&ledger.Call{
			Timestamp:   now.Add(off),
			RequestID:   id,
			Verb:        "find",
			ArgsMsgpack: []byte("argA"),
			OK:          true,
		}); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}
	// Three same-args calls. The first two are 1 minute apart (cache
	// pair). The third is 10 minutes after the second (outside TTL).
	mk(1, -20*time.Minute)
	mk(2, -19*time.Minute) // pair: 1m gap, same args
	mk(3, -9*time.Minute)  // gap: 10m, no pair

	r, perr := RunWithLedger(l, &Args{Since: time.Hour, Session: "current"})
	if perr != nil {
		t.Fatalf("RunWithLedger: %+v", perr)
	}
	stats := byVerb(r)
	if stats["find"].CachePairs != 1 {
		t.Errorf("CachePairs: got %d, want 1 (one within TTL, one outside)", stats["find"].CachePairs)
	}
}

// TestRunWithLedger_ExcludesUsageVerb confirms the verb skips its own
// kind in the output — a usage call asking "how cache-friendly were my
// calls?" shouldn't include itself in the answer (circular).
func TestRunWithLedger_ExcludesUsageVerb(t *testing.T) {
	l := openTestLedger(t)
	now := time.Now()
	if _, err := l.Record(&ledger.Call{
		Timestamp: now, RequestID: 1, Verb: "usage", OK: true,
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if _, err := l.Record(&ledger.Call{
		Timestamp: now, RequestID: 2, Verb: "find", OK: true,
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	r, perr := RunWithLedger(l, &Args{Since: time.Hour, Session: "current"})
	if perr != nil {
		t.Fatalf("RunWithLedger: %+v", perr)
	}
	for _, vs := range r.PerVerb {
		if vs.Verb == "usage" {
			t.Errorf("usage verb leaked into output: %+v", vs)
		}
	}
}

// TestRunWithLedger_TurnsSummary pins the ASH-188 behaviour: when the
// ledger has turn rows in the window, Turns is populated with the
// per-window aggregate; when there are none it stays nil so the
// pretty surface remains byte-identical to the pre-ASH-188 proxy.
func TestRunWithLedger_TurnsSummary(t *testing.T) {
	l := openTestLedger(t)
	now := time.Now()
	// One call to keep PerVerb non-empty so the absence of Turns is
	// distinguishable from an empty-window response.
	if _, err := l.Record(&ledger.Call{
		Timestamp: now, RequestID: 1, Verb: "find", OK: true,
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	r, perr := RunWithLedger(l, &Args{Since: time.Hour, Session: "current"})
	if perr != nil {
		t.Fatalf("RunWithLedger empty turns: %+v", perr)
	}
	if r.Turns != nil {
		t.Fatalf("no turn rows: want Turns=nil, got %+v", r.Turns)
	}

	if _, ok := l.InsertTurn(&ledger.Turn{
		TurnID: "msg_a", Timestamp: now, CacheReadTokens: 100, InputTokens: 10, OutputTokens: 50,
	}); !ok {
		t.Fatal("InsertTurn msg_a: want ok=true")
	}
	if _, ok := l.InsertTurn(&ledger.Turn{
		TurnID: "msg_b", Timestamp: now, CacheReadTokens: 200, CacheCreationTokens: 30, InputTokens: 5,
	}); !ok {
		t.Fatal("InsertTurn msg_b: want ok=true")
	}

	r, perr = RunWithLedger(l, &Args{Since: time.Hour, Session: "current"})
	if perr != nil {
		t.Fatalf("RunWithLedger with turns: %+v", perr)
	}
	if r.Turns == nil {
		t.Fatal("with turn rows: want Turns populated, got nil")
	}
	want := TurnsSummary{
		Turns: 2, InputTokens: 15, OutputTokens: 50,
		CacheReadTokens: 300, CacheCreationTokens: 30,
	}
	if *r.Turns != want {
		t.Errorf("Turns summary: got %+v, want %+v", *r.Turns, want)
	}
}

// TestPrettyResponse_TurnsLineHitRate confirms the rendered hit rate
// uses the canonical denominator (cache_read + cache_creation + input).
// 800 cached / (800+200+0) = 80%.
func TestPrettyResponse_TurnsLineHitRate(t *testing.T) {
	r := &Result{
		Since: "1h", Session: "current", Calls: 1,
		PerVerb: []VerbStats{{Verb: "find", Calls: 1, UniqueArgs: 1}},
		Turns: &TurnsSummary{
			Turns: 3, CacheReadTokens: 800, CacheCreationTokens: 200,
		},
	}
	rsp := &proto.Response{OK: true, Data: proto.MustData(r)}
	pretty := PrettyResponse(nil, rsp)
	if !strings.Contains(pretty, "cache: 80.0%") {
		t.Errorf("expected 'cache: 80.0%%' line, got:\n%s", pretty)
	}
	if !strings.Contains(pretty, "across 3 turns") {
		t.Errorf("expected 'across 3 turns' marker, got:\n%s", pretty)
	}
}

func byVerb(r *Result) map[string]VerbStats {
	m := make(map[string]VerbStats, len(r.PerVerb))
	for _, vs := range r.PerVerb {
		m[vs.Verb] = vs
	}
	return m
}
