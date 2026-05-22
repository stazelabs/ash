package bench

import (
	"strings"
	"testing"
	"time"

	"github.com/stazelabs/ash/internal/ledger"
)

// pctChange handles zero a as "no signal" → 0%, NOT +inf or NaN. The
// surrounding aggregation code relies on this contract.
func TestPctChange(t *testing.T) {
	cases := []struct {
		a, b int
		want float64
	}{
		{a: 0, b: 0, want: 0},
		{a: 0, b: 100, want: 0}, // zero-base must be 0, not +inf
		{a: 100, b: 100, want: 0},
		{a: 100, b: 150, want: 50},
		{a: 100, b: 50, want: -50},
		{a: 200, b: 100, want: -50},
	}
	for _, c := range cases {
		if got := pctChange(c.a, c.b); got != c.want {
			t.Errorf("pctChange(%d, %d) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestPctChangeInt64(t *testing.T) {
	cases := []struct {
		a, b int64
		want float64
	}{
		{a: 0, b: 0, want: 0},
		{a: 0, b: 1000, want: 0},
		{a: 1000, b: 1200, want: 20},
		{a: 1000, b: 800, want: -20},
	}
	for _, c := range cases {
		if got := pctChangeInt64(c.a, c.b); got != c.want {
			t.Errorf("pctChangeInt64(%d, %d) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

// classifyDelta — the regression / improvement gate. Asymmetric:
// regression triggers on EITHER axis crossing; improvement requires
// BOTH. This bug class would silently let token regressions through
// when latency improved (or vice versa).
func TestClassifyDelta(t *testing.T) {
	cases := []struct {
		name                  string
		dTok, dLat            float64
		regTok, regLat        float64
		wantRegress, wantImpr bool
	}{
		// Below thresholds → neither.
		{name: "small_changes", dTok: 5, dLat: 10, regTok: 10, regLat: 20, wantRegress: false, wantImpr: false},
		// Token regression alone.
		{name: "tok_above_threshold", dTok: 15, dLat: 0, regTok: 10, regLat: 20, wantRegress: true, wantImpr: false},
		// Latency regression alone.
		{name: "lat_above_threshold", dTok: 0, dLat: 25, regTok: 10, regLat: 20, wantRegress: true, wantImpr: false},
		// Improvement requires BOTH axes.
		{name: "both_improved", dTok: -15, dLat: -25, regTok: 10, regLat: 20, wantRegress: false, wantImpr: true},
		// Only one axis improved → neither.
		{name: "only_tok_improved", dTok: -15, dLat: -5, regTok: 10, regLat: 20, wantRegress: false, wantImpr: false},
		{name: "only_lat_improved", dTok: -5, dLat: -25, regTok: 10, regLat: 20, wantRegress: false, wantImpr: false},
		// Edge: exactly at threshold → not regressed (strict >).
		{name: "exactly_at_threshold", dTok: 10, dLat: 20, regTok: 10, regLat: 20, wantRegress: false, wantImpr: false},
		// Tok regression overrides lat improvement.
		{name: "tok_regress_lat_improve", dTok: 15, dLat: -25, regTok: 10, regLat: 20, wantRegress: true, wantImpr: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotR, gotI := classifyDelta(c.dTok, c.dLat, c.regTok, c.regLat)
			if gotR != c.wantRegress || gotI != c.wantImpr {
				t.Errorf("got (regress=%v, impr=%v), want (regress=%v, impr=%v)",
					gotR, gotI, c.wantRegress, c.wantImpr)
			}
		})
	}
}

func TestShortRunID(t *testing.T) {
	cases := []struct{ in, want string }{
		{in: "deadbeefcafe1234", want: "deadbeef"},
		{in: "short", want: "short"},
		{in: "", want: ""},
		{in: "12345678", want: "12345678"}, // exactly 8 → no truncation (len > 8 is the test)
	}
	for _, c := range cases {
		if got := shortRunID(c.in); got != c.want {
			t.Errorf("shortRunID(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestMin(t *testing.T) {
	if got := min(3, 5); got != 3 {
		t.Errorf("min(3, 5) = %d, want 3", got)
	}
	if got := min(5, 3); got != 3 {
		t.Errorf("min(5, 3) = %d, want 3", got)
	}
	if got := min(0, 0); got != 0 {
		t.Errorf("min(0, 0) = %d, want 0", got)
	}
}

func TestIndexByCase(t *testing.T) {
	rows := []ledger.BenchCaseResult{
		{CaseName: "a", Verb: "find"},
		{CaseName: "b", Verb: "grep"},
	}
	m := indexByCase(rows)
	if len(m) != 2 {
		t.Errorf("map size: got %d, want 2", len(m))
	}
	if m["a"].Verb != "find" || m["b"].Verb != "grep" {
		t.Errorf("contents: %+v", m)
	}
	if _, ok := m["missing"]; ok {
		t.Error("missing key should not be present")
	}
}

func TestUnionCaseNames(t *testing.T) {
	a := map[string]ledger.BenchCaseResult{"x": {}, "y": {}}
	b := map[string]ledger.BenchCaseResult{"y": {}, "z": {}}
	names := unionCaseNames(a, b)
	if len(names) != 3 {
		t.Errorf("union size: got %d, want 3 (x, y, z)", len(names))
	}
	seen := map[string]bool{}
	for _, n := range names {
		seen[n] = true
	}
	for _, want := range []string{"x", "y", "z"} {
		if !seen[want] {
			t.Errorf("missing %q from union: %v", want, names)
		}
	}
}

// buildCompare — the centerpiece of `ash bench --compare`. Pin per-case
// joining, the regression/improvement flagging, and the case-set
// match signal.
func TestBuildCompare_RegressionAndImprovement(t *testing.T) {
	a := RunSummary{RunUUID: "A", CaseSetVersion: "cs-X"}
	b := RunSummary{RunUUID: "B", CaseSetVersion: "cs-X"}
	casesA := []ledger.BenchCaseResult{
		{CaseName: "regress", Verb: "find", AshTokens: 100, BashTokens: 200, AshLatencyUsP50: 50},
		{CaseName: "improve", Verb: "grep", AshTokens: 1000, BashTokens: 1500, AshLatencyUsP50: 1000},
		{CaseName: "neutral", Verb: "read", AshTokens: 100, BashTokens: 100, AshLatencyUsP50: 100},
		{CaseName: "only_a", Verb: "stat", AshTokens: 50, BashTokens: 60, AshLatencyUsP50: 5},
	}
	casesB := []ledger.BenchCaseResult{
		// regress: tokens +25% (above default 10%), latency +60% (above default 20%) — regression.
		{CaseName: "regress", Verb: "find", AshTokens: 125, BashTokens: 200, AshLatencyUsP50: 80},
		// improve: tokens -50%, latency -50% — improvement.
		{CaseName: "improve", Verb: "grep", AshTokens: 500, BashTokens: 1500, AshLatencyUsP50: 500},
		// neutral: identical — neither.
		{CaseName: "neutral", Verb: "read", AshTokens: 100, BashTokens: 100, AshLatencyUsP50: 100},
		{CaseName: "only_b", Verb: "diff", AshTokens: 70, BashTokens: 100, AshLatencyUsP50: 7},
	}
	args := &Args{RegressTokPct: 10, RegressLatPct: 20}

	cr := buildCompare(a, b, casesA, casesB, args)
	if !cr.CaseSetMatch {
		t.Error("CaseSetMatch should be true when both runs share case_set_version")
	}
	if cr.RegressTokPct != 10 || cr.RegressLatPct != 20 {
		t.Errorf("thresholds: %+v", cr)
	}
	if len(cr.PerCase) != 5 { // regress, improve, neutral, only_a, only_b
		t.Errorf("PerCase len: got %d, want 5", len(cr.PerCase))
	}

	byName := map[string]CaseDelta{}
	for _, d := range cr.PerCase {
		byName[d.CaseName] = d
	}
	if !byName["regress"].Regressed || byName["regress"].Improved {
		t.Errorf("regress: got %+v", byName["regress"])
	}
	if byName["improve"].Regressed || !byName["improve"].Improved {
		t.Errorf("improve: got %+v", byName["improve"])
	}
	if byName["neutral"].Regressed || byName["neutral"].Improved {
		t.Errorf("neutral: got %+v", byName["neutral"])
	}
	if !byName["only_a"].OnlyInA || byName["only_a"].OnlyInB {
		t.Errorf("only_a flag: %+v", byName["only_a"])
	}
	if byName["only_b"].OnlyInA || !byName["only_b"].OnlyInB {
		t.Errorf("only_b flag: %+v", byName["only_b"])
	}
	if len(cr.Regressions) != 1 || cr.Regressions[0].CaseName != "regress" {
		t.Errorf("Regressions list: %+v", cr.Regressions)
	}
	if len(cr.Improvements) != 1 || cr.Improvements[0].CaseName != "improve" {
		t.Errorf("Improvements list: %+v", cr.Improvements)
	}
}

func TestBuildCompare_CaseSetMismatch(t *testing.T) {
	a := RunSummary{RunUUID: "A", CaseSetVersion: "cs-X"}
	b := RunSummary{RunUUID: "B", CaseSetVersion: "cs-Y"}
	cr := buildCompare(a, b, nil, nil, &Args{})
	if cr.CaseSetMatch {
		t.Error("CaseSetMatch must be false when case-set versions differ")
	}
}

func TestBuildCompare_ThresholdDefaults(t *testing.T) {
	cr := buildCompare(RunSummary{}, RunSummary{}, nil, nil, &Args{}) // zero args → defaults
	if cr.RegressTokPct != 10 || cr.RegressLatPct != 20 {
		t.Errorf("threshold defaults: tok=%d, lat=%d", cr.RegressTokPct, cr.RegressLatPct)
	}
}

func TestPrettyList_Empty(t *testing.T) {
	out := prettyList(&ListResult{Kind: kindList})
	if !strings.Contains(out, "0 run(s)") {
		t.Errorf("zero-run header missing: %s", out)
	}
	if !strings.Contains(out, "no runs persisted yet") {
		t.Errorf("guidance missing: %s", out)
	}
}

func TestPrettyList_WithRuns(t *testing.T) {
	lr := &ListResult{
		Kind: kindList,
		Runs: []RunSummary{
			{
				RunUUID:         "deadbeefcafe1234abcd",
				Timestamp:       time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
				CaseSetVersion:  "cs-feedface00000000",
				DeltaTokPct:     -12,
				AshTokensTotal:  100,
				BashTokensTotal: 200,
				RepoSHA:         "abc123def456",
				RepoDirty:       true,
			},
		},
	}
	out := prettyList(lr)
	for _, want := range []string{
		"1 run(s)",
		"deadbeef", // truncated UUID
		"2026-01-02",
		"-12%",
		"100/200",
		"abc123d-dirty", // prettyList uses first 7 chars + -dirty suffix
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestPrettyCompare(t *testing.T) {
	cr := &CompareResult{
		Kind:          kindCompare,
		A:             RunSummary{RunUUID: "AAAAAAAAxxxx", CaseSetVersion: "cs-X", RepoSHA: "abc123def"},
		B:             RunSummary{RunUUID: "BBBBBBBByyyy", CaseSetVersion: "cs-X", RepoSHA: "111222333"},
		CaseSetMatch:  true,
		RegressTokPct: 10,
		RegressLatPct: 20,
		FreshRunUUID:  "CCCCCCCCzzzz",
		PerCase: []CaseDelta{
			{CaseName: "case_regressed", Verb: "find", AshTokA: 100, AshTokB: 130, DeltaTokPct: 30, Regressed: true},
			{CaseName: "case_improved", Verb: "grep", AshTokA: 200, AshTokB: 100, DeltaTokPct: -50, Improved: true},
			{CaseName: "only_a_case", Verb: "stat", OnlyInA: true},
			{CaseName: "only_b_case", Verb: "diff", OnlyInB: true},
		},
		Regressions:  []CaseDelta{{CaseName: "case_regressed"}},
		Improvements: []CaseDelta{{CaseName: "case_improved"}},
	}
	out := prettyCompare(cr)
	for _, want := range []string{
		"A=AAAAAAAA",
		"B=BBBBBBBB",
		"case-set: matched (cs-X)",
		"abc123d..1112223",
		"thresholds: Δtok>+10% or Δlat>+20%",
		"fresh run: CCCCCCCC",
		"REGRESS",
		"IMPROVE",
		"ONLY_A",
		"ONLY_B",
		"regressions: 1   improvements: 1",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("prettyCompare missing %q\nactual:\n%s", want, out)
		}
	}
}

func TestPrettyCompare_CaseSetMismatch(t *testing.T) {
	cr := &CompareResult{
		Kind:         kindCompare,
		A:            RunSummary{CaseSetVersion: "cs-X"},
		B:            RunSummary{CaseSetVersion: "cs-Y"},
		CaseSetMatch: false,
	}
	out := prettyCompare(cr)
	if !strings.Contains(out, "MISMATCH") {
		t.Errorf("expected MISMATCH in mismatched case-set output: %s", out)
	}
}
