package bench

import (
	"testing"

	"github.com/stazelabs/ash/internal/bench"
)

// pctDelta — used in pretty rendering. Edge cases matter because
// division-by-zero hides as "+inf"; we want the sentinel, not a
// crash or a NaN in the human-facing output.
func TestPctDelta(t *testing.T) {
	cases := []struct {
		ash, bash int
		want      string
	}{
		{ash: 0, bash: 0, want: "0%"},
		{ash: 10, bash: 0, want: "+inf"},
		{ash: 50, bash: 100, want: "-50%"},
		{ash: 150, bash: 100, want: "+50%"},
		{ash: 100, bash: 100, want: "+0%"},
	}
	for _, c := range cases {
		if got := pctDelta(c.ash, c.bash); got != c.want {
			t.Errorf("pctDelta(%d, %d) = %q, want %q", c.ash, c.bash, got, c.want)
		}
	}
}

func TestPctDeltaInt64(t *testing.T) {
	cases := []struct {
		ash, bash int64
		want      string
	}{
		{ash: 0, bash: 0, want: "0%"},
		{ash: 10, bash: 0, want: "+inf"},
		{ash: 800, bash: 1000, want: "-20%"},
		{ash: 1200, bash: 1000, want: "+20%"},
	}
	for _, c := range cases {
		if got := pctDeltaInt64(c.ash, c.bash); got != c.want {
			t.Errorf("pctDeltaInt64(%d, %d) = %q, want %q", c.ash, c.bash, got, c.want)
		}
	}
}

// percentileUs uses the lower-rank convention: floor((n-1)*p). Pin the
// exact rank arithmetic — an off-by-one here silently corrupts every
// p50 reported in bench output, and the latency snapshot files we
// commit to bench/latency-snapshot.json.
func TestPercentileUs(t *testing.T) {
	if got := percentileUs(nil, 0.5); got != 0 {
		t.Errorf("empty samples: got %d, want 0", got)
	}
	if got := percentileUs([]int64{42}, 0.5); got != 42 {
		t.Errorf("single sample: got %d, want 42", got)
	}

	// floor((n-1)*p) for n=5: p=0→0, p=0.25→1, p=0.5→2, p=0.75→3, p=1→4.
	samples := []int64{50, 10, 30, 20, 40} // unsorted input — function must sort
	cases := []struct {
		p    float64
		want int64
	}{
		{p: 0.0, want: 10},
		{p: 0.25, want: 20},
		{p: 0.5, want: 30},
		{p: 0.75, want: 40},
		{p: 1.0, want: 50},
	}
	for _, c := range cases {
		if got := percentileUs(samples, c.p); got != c.want {
			t.Errorf("percentileUs(p=%v) = %d, want %d", c.p, got, c.want)
		}
	}

	// percentileUs must not mutate input.
	in := []int64{3, 1, 2}
	_ = percentileUs(in, 0.5)
	if in[0] != 3 || in[1] != 1 || in[2] != 2 {
		t.Errorf("input mutated: %v", in)
	}
}

func TestMinUs(t *testing.T) {
	if got := minUs(nil); got != 0 {
		t.Errorf("empty: got %d, want 0", got)
	}
	if got := minUs([]int64{7}); got != 7 {
		t.Errorf("single: got %d, want 7", got)
	}
	if got := minUs([]int64{30, 10, 20}); got != 10 {
		t.Errorf("min: got %d, want 10", got)
	}
	// Negative samples: minUs returns the most-negative value.
	if got := minUs([]int64{-1, -5, -3}); got != -5 {
		t.Errorf("negative min: got %d, want -5", got)
	}
}

func TestTruncStr(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{in: "hello", n: 10, want: "hello"},
		{in: "hello", n: 5, want: "hello"},
		{in: "hello", n: 3, want: "he…"},
		{in: "hello", n: 1, want: "h"},
		{in: "hello", n: 0, want: ""},
	}
	for _, c := range cases {
		if got := truncStr(c.in, c.n); got != c.want {
			t.Errorf("truncStr(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
	}
}

// selectCases — filtering / limit logic over the static Cases list.
func TestSelectCases_ByName(t *testing.T) {
	out := selectCases(&Args{Case: "find_shallow"})
	if len(out) != 1 || out[0].Name != "find_shallow" {
		t.Fatalf("by name: got %+v", out)
	}
}

func TestSelectCases_NameMiss(t *testing.T) {
	out := selectCases(&Args{Case: "no-such-case"})
	if len(out) != 0 {
		t.Errorf("name miss: got %d cases, want 0", len(out))
	}
}

func TestSelectCases_ByVerb(t *testing.T) {
	out := selectCases(&Args{Verb: "stat"})
	if len(out) == 0 {
		t.Fatal("verb=stat should match at least one case")
	}
	for _, c := range out {
		if c.Verb != "stat" {
			t.Errorf("expected verb=stat: %+v", c)
		}
	}
}

func TestSelectCases_AllWhenUnfiltered(t *testing.T) {
	out := selectCases(&Args{})
	if len(out) != len(bench.Cases) {
		t.Errorf("unfiltered: got %d, want %d", len(out), len(bench.Cases))
	}
}

func TestSelectCases_LimitApplied(t *testing.T) {
	out := selectCases(&Args{Limit: 3})
	if len(out) != 3 {
		t.Errorf("limit=3: got %d", len(out))
	}
	// Limit larger than result count should not blow up.
	out2 := selectCases(&Args{Verb: "diff", Limit: 999})
	for _, c := range out2 {
		if c.Verb != "diff" {
			t.Errorf("verb filter broken under large limit: %+v", c)
		}
	}
}

func TestAggregateByVerb(t *testing.T) {
	rows := []CaseResult{
		{Verb: "find", AshTokens: 100, BashTokens: 200, AshLatencyUs: 10, BashLatencyUs: 20},
		{Verb: "find", AshTokens: 50, BashTokens: 80, AshLatencyUs: 5, BashLatencyUs: 8},
		{Verb: "grep", AshTokens: 300, BashTokens: 500, AshLatencyUs: 30, BashLatencyUs: 50},
	}
	out := aggregateByVerb(rows)
	if len(out) != 2 {
		t.Fatalf("expected 2 verb groups, got %d", len(out))
	}
	// Sorted alphabetically — find before grep.
	if out[0].Verb != "find" || out[1].Verb != "grep" {
		t.Errorf("order: got %s, %s, want find, grep", out[0].Verb, out[1].Verb)
	}
	if out[0].Cases != 2 || out[0].AshTokensTotal != 150 || out[0].BashTokensTotal != 280 {
		t.Errorf("find rollup wrong: %+v", out[0])
	}
	if out[1].Cases != 1 || out[1].AshTokensTotal != 300 {
		t.Errorf("grep rollup wrong: %+v", out[1])
	}
}

func TestAggregateByVerb_Empty(t *testing.T) {
	if out := aggregateByVerb(nil); len(out) != 0 {
		t.Errorf("empty: got %d", len(out))
	}
}

func TestAggregateOverall(t *testing.T) {
	rows := []CaseResult{
		{AshTokens: 100, BashTokens: 200, AshLatencyUs: 10, BashLatencyUs: 20},
		{AshTokens: 50, BashTokens: 80, AshLatencyUs: 5, BashLatencyUs: 8},
	}
	o := aggregateOverall(rows)
	if o.Verb != "overall" {
		t.Errorf("Verb: got %q, want overall", o.Verb)
	}
	if o.Cases != 2 || o.AshTokensTotal != 150 || o.BashTokensTotal != 280 {
		t.Errorf("overall rollup: %+v", o)
	}
	if o.AshLatencyUsTotal != 15 || o.BashLatencyUsTotal != 28 {
		t.Errorf("latency totals: %+v", o)
	}
}

func TestMaxInt(t *testing.T) {
	cases := []struct{ a, b, want int }{
		{a: 1, b: 2, want: 2},
		{a: 5, b: 3, want: 5},
		{a: 0, b: 0, want: 0},
		{a: -1, b: -5, want: -1},
	}
	for _, c := range cases {
		if got := maxInt(c.a, c.b); got != c.want {
			t.Errorf("maxInt(%d, %d) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}
