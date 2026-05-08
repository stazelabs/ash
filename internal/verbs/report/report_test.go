package report

import (
	"testing"
	"time"

	"github.com/stazelabs/ash/internal/ledger"
)

func TestPercentile(t *testing.T) {
	tests := []struct {
		vals []int64
		p    float64
		want int64
	}{
		{nil, 0.5, 0},
		{[]int64{100}, 0.5, 100},
		{[]int64{100}, 0.95, 100},
		{[]int64{10, 20, 30, 40, 50}, 0.50, 30},
		{[]int64{10, 20, 30, 40, 50}, 0.95, 40}, // floor(0.95*4)=3 → sorted[3]=40
		{[]int64{50, 10, 30, 20, 40}, 0.50, 30}, // unsorted input
		{[]int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}, 0.90, 9}, // floor(0.90*9)=8 → sorted[8]=9
	}
	for _, tt := range tests {
		got := percentile(tt.vals, tt.p)
		if got != tt.want {
			t.Errorf("percentile(%v, %.2f) = %d, want %d", tt.vals, tt.p, got, tt.want)
		}
	}
}

func TestPct(t *testing.T) {
	if pct(0, 0) != 0 {
		t.Error("pct(0,0) should be 0")
	}
	if pct(1, 2) != 50 {
		t.Errorf("pct(1,2) = %v, want 50", pct(1, 2))
	}
	if pct(3, 3) != 100 {
		t.Errorf("pct(3,3) = %v, want 100", pct(3, 3))
	}
}

func TestParseDuration(t *testing.T) {
	tests := []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{"1h", time.Hour, false},
		{"15m", 15 * time.Minute, false},
		{"1d", 24 * time.Hour, false},
		{"7d", 7 * 24 * time.Hour, false},
		{"0d", 0, true},
		{"-1d", 0, true},
		{"bad", 0, true},
	}
	for _, tt := range tests {
		got, err := parseDuration(tt.in)
		if tt.wantErr {
			if err == nil {
				t.Errorf("parseDuration(%q) expected error, got nil", tt.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseDuration(%q) unexpected error: %v", tt.in, err)
		}
		if got != tt.want {
			t.Errorf("parseDuration(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestParseArgs(t *testing.T) {
	// Defaults
	a, err := ParseArgs(map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if a.Session != "current" {
		t.Errorf("default session = %q, want 'current'", a.Session)
	}
	if a.Last != 0 || a.Since != 0 || a.Verb != "" {
		t.Errorf("unexpected non-zero default: last=%d since=%v verb=%q", a.Last, a.Since, a.Verb)
	}

	// All fields
	a, err = ParseArgs(map[string]any{
		"session": "all",
		"since":   "1h",
		"last":    float64(50),
		"verb":    "find",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if a.Session != "all" {
		t.Errorf("session = %q, want 'all'", a.Session)
	}
	if a.Since != time.Hour {
		t.Errorf("since = %v, want 1h", a.Since)
	}
	if a.Last != 50 {
		t.Errorf("last = %d, want 50", a.Last)
	}
	if a.Verb != "find" {
		t.Errorf("verb = %q, want 'find'", a.Verb)
	}

	// last > MaxLast is clamped
	a, _ = ParseArgs(map[string]any{"last": float64(99999)})
	if a.Last != MaxLast {
		t.Errorf("last not clamped: got %d", a.Last)
	}
}

func makeCalls(verb string, n int, execUs int64, ok bool, truncated bool) []ledger.Call {
	calls := make([]ledger.Call, n)
	for i := range calls {
		calls[i] = ledger.Call{
			Verb:          verb,
			OK:            ok,
			Truncated:     truncated,
			LatencyExecUs: execUs + int64(i)*10,
			TokensOut:     100 + i,
		}
	}
	return calls
}

func TestAggregate_Empty(t *testing.T) {
	r := aggregate(nil, Scope{Session: "current"})
	if r.Totals.Calls != 0 {
		t.Errorf("expected 0 calls, got %d", r.Totals.Calls)
	}
	if len(r.ByVerb) != 0 {
		t.Errorf("expected empty ByVerb, got %v", r.ByVerb)
	}
}

func TestAggregate_SingleVerb(t *testing.T) {
	calls := makeCalls("find", 4, 1000, true, false)
	calls[2].Truncated = true // 1 truncated

	r := aggregate(calls, Scope{Session: "current"})
	if r.Totals.Calls != 4 {
		t.Errorf("Totals.Calls = %d, want 4", r.Totals.Calls)
	}
	if r.Totals.OK != 4 {
		t.Errorf("Totals.OK = %d, want 4", r.Totals.OK)
	}
	if len(r.ByVerb) != 1 {
		t.Fatalf("len(ByVerb) = %d, want 1", len(r.ByVerb))
	}
	vs := r.ByVerb[0]
	if vs.Verb != "find" {
		t.Errorf("Verb = %q, want 'find'", vs.Verb)
	}
	if vs.N != 4 {
		t.Errorf("N = %d, want 4", vs.N)
	}
	if vs.OKPct != 100 {
		t.Errorf("OKPct = %.1f, want 100", vs.OKPct)
	}
	if vs.TruncatedN != 1 {
		t.Errorf("TruncatedN = %d, want 1", vs.TruncatedN)
	}
}

func TestAggregate_MultiVerb(t *testing.T) {
	calls := append(makeCalls("find", 3, 1000, true, false), makeCalls("grep", 2, 5000, false, false)...)
	r := aggregate(calls, Scope{Session: "current"})

	if len(r.ByVerb) != 2 {
		t.Fatalf("len(ByVerb) = %d, want 2", len(r.ByVerb))
	}
	if r.ByVerb[0].Verb != "find" {
		t.Errorf("first verb = %q, want 'find'", r.ByVerb[0].Verb)
	}
	if r.ByVerb[1].Verb != "grep" {
		t.Errorf("second verb = %q, want 'grep'", r.ByVerb[1].Verb)
	}
	if r.ByVerb[1].OKPct != 0 {
		t.Errorf("grep ok%% = %.1f, want 0", r.ByVerb[1].OKPct)
	}
	if r.Totals.Errors != 2 {
		t.Errorf("Totals.Errors = %d, want 2", r.Totals.Errors)
	}
}

func TestFmtUs(t *testing.T) {
	tests := []struct{ us int64; want string }{
		{0, "0us"},
		{142, "142us"},
		{999, "999us"},
		{1000, "1.0ms"},
		{2400, "2.4ms"},
		{999999, "1000.0ms"},
		{1_000_000, "1.0s"},
		{26_400_000, "26.4s"},
	}
	for _, tt := range tests {
		got := fmtUs(tt.us)
		if got != tt.want {
			t.Errorf("fmtUs(%d) = %q, want %q", tt.us, got, tt.want)
		}
	}
}
