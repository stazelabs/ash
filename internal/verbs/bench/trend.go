package bench

// Trend tooling: --list, --compare, --baseline. These flags read from
// the bench_runs / bench_case_results tables (Phase 1 persistence) and
// emit comparison views without (or in addition to) running a fresh
// bench themselves.

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/stazelabs/ash/internal/ledger"
	"github.com/stazelabs/ash/internal/proto"
	"github.com/stazelabs/ash/internal/verbs/argutil"
)

// RunSummary is the listed/header form of one bench_runs row.
type RunSummary struct {
	RunUUID         string    `msgpack:"run_uuid"`
	Timestamp       time.Time `msgpack:"ts"`
	AshVersion      string    `msgpack:"ash_version"`
	AshCommitSHA    string    `msgpack:"ash_commit_sha,omitempty"`
	CaseSetVersion  string    `msgpack:"case_set_version"`
	RepoSHA         string    `msgpack:"repo_sha,omitempty"`
	RepoDirty       bool      `msgpack:"repo_dirty,omitempty"`
	Platform        string    `msgpack:"platform,omitempty"`
	RepeatN         int       `msgpack:"repeat_n"`
	WarmupN         int       `msgpack:"warmup_n"`
	Cases           int       `msgpack:"cases"`
	AshTokensTotal  int       `msgpack:"ash_tokens_total"`
	BashTokensTotal int       `msgpack:"bash_tokens_total"`
	DeltaTokPct     float64   `msgpack:"delta_tok_pct"`
}

// ListResult is the response shape for --list.
type ListResult struct {
	Kind string       `msgpack:"kind"` // always "list"
	Runs []RunSummary `msgpack:"runs"`
}

// CaseDelta is one row in a CompareResult.
type CaseDelta struct {
	CaseName       string  `msgpack:"case_name"`
	Verb           string  `msgpack:"verb"`
	AshTokA        int     `msgpack:"ash_tok_a"`
	AshTokB        int     `msgpack:"ash_tok_b"`
	BashTokA       int     `msgpack:"bash_tok_a,omitempty"`
	BashTokB       int     `msgpack:"bash_tok_b,omitempty"`
	DeltaTokPct    float64 `msgpack:"delta_tok_pct"`
	AshLatUsP50A   int64   `msgpack:"ash_lat_us_p50_a"`
	AshLatUsP50B   int64   `msgpack:"ash_lat_us_p50_b"`
	DeltaLatPct    float64 `msgpack:"delta_lat_pct"`
	Regressed      bool    `msgpack:"regressed,omitempty"`
	Improved       bool    `msgpack:"improved,omitempty"`
	OnlyInA        bool    `msgpack:"only_in_a,omitempty"`
	OnlyInB        bool    `msgpack:"only_in_b,omitempty"`
}

// CompareResult is the response shape for --compare and --baseline.
type CompareResult struct {
	Kind          string      `msgpack:"kind"` // always "compare"
	A             RunSummary  `msgpack:"a"`
	B             RunSummary  `msgpack:"b"`
	PerCase       []CaseDelta `msgpack:"per_case"`
	Regressions   []CaseDelta `msgpack:"regressions,omitempty"`
	Improvements  []CaseDelta `msgpack:"improvements,omitempty"`
	CaseSetMatch  bool        `msgpack:"case_set_match"`
	RegressTokPct int         `msgpack:"regress_tok_pct"`
	RegressLatPct int         `msgpack:"regress_lat_pct"`
	// FreshRunUUID is non-empty when --baseline triggered a new bench
	// (so the caller can correlate the comparison's "B" side with the
	// just-persisted run).
	FreshRunUUID string `msgpack:"fresh_run_uuid,omitempty"`
}

const (
	kindList    = "list"
	kindCompare = "compare"

	// Special run-uuid tokens accepted by --compare.
	tokenLatest   = "latest"
	tokenBaseline = "baseline" // resolved against bench/baseline.json (Phase 5)
)

// runList returns the most recent runs as a ListResult.
func runList(d Deps, a *Args) (*ListResult, *proto.Error) {
	if d.Ledger == nil {
		return nil, &proto.Error{Code: "config", Msg: "bench --list: ledger not wired"}
	}
	limit := a.ListLimit
	if limit <= 0 {
		limit = 20
	}
	rows, err := d.Ledger.QueryBenchRuns(limit)
	if err != nil {
		return nil, &proto.Error{Code: "ledger", Msg: err.Error()}
	}
	out := &ListResult{Kind: kindList, Runs: make([]RunSummary, 0, len(rows))}
	for _, r := range rows {
		out.Runs = append(out.Runs, summarizeRun(d.Ledger, r))
	}
	return out, nil
}

// runCompare resolves CompareA and CompareB into RunSummaries + per-case
// rows and returns a CompareResult.
func runCompare(d Deps, a *Args) (*CompareResult, *proto.Error) {
	if d.Ledger == nil {
		return nil, &proto.Error{Code: "config", Msg: "bench --compare: ledger not wired"}
	}
	runA, casesA, perr := resolveRun(d, a.CompareA)
	if perr != nil {
		return nil, perr
	}
	runB, casesB, perr := resolveRun(d, a.CompareB)
	if perr != nil {
		return nil, perr
	}
	return buildCompare(*runA, *runB, casesA, casesB, a), nil
}

// runWithBaseline runs a fresh bench, then compares the just-run
// against the per-case median over the rolling window in a.Baseline
// (e.g. "7d"). The comparison's B side is the fresh run; A is the
// rolling baseline.
func runWithBaseline(d Deps, a *Args) (*CompareResult, *proto.Error) {
	dur, err := argutil.ParseDuration(a.Baseline)
	if err != nil {
		return nil, &proto.Error{Code: "args", Msg: "baseline: " + err.Error()}
	}

	// Run the bench fresh via the standard path (skip the dispatch in
	// RunWithDeps so we can't recurse).
	fresh, perr := runStandard(d, &Args{
		Verb:   a.Verb,
		Case:   a.Case,
		Limit:  a.Limit,
		Repeat: a.Repeat,
		Warmup: a.Warmup,
	})
	if perr != nil {
		return nil, perr
	}

	// Resolve the fresh run as the "B" side.
	runB, casesB, perr := resolveRun(d, fresh.RunUUID)
	if perr != nil {
		return nil, perr
	}

	// Build the "A" side from the rolling baseline aggregation.
	since := time.Now().Add(-dur)
	baselineCases, qerr := d.Ledger.QueryBenchBaseline(since)
	if qerr != nil {
		return nil, &proto.Error{Code: "ledger", Msg: qerr.Error()}
	}
	if len(baselineCases) == 0 {
		return nil, &proto.Error{Code: "no_baseline", Msg: fmt.Sprintf("no bench runs in window %s", a.Baseline)}
	}
	casesA := make([]ledger.BenchCaseResult, 0, len(baselineCases))
	for _, r := range baselineCases {
		casesA = append(casesA, r)
	}
	runA := RunSummary{
		RunUUID:        "baseline-" + a.Baseline,
		Timestamp:      since,
		CaseSetVersion: runB.CaseSetVersion,
		Cases:          len(baselineCases),
	}
	cmp := buildCompare(runA, *runB, casesA, casesB, a)
	cmp.FreshRunUUID = fresh.RunUUID
	return cmp, nil
}

// resolveRun maps a token (UUID, "latest", "baseline") into
// (RunSummary, per-case rows). "baseline" is reserved for Phase 5
// (bench/baseline.json) and currently errors.
func resolveRun(d Deps, token string) (*RunSummary, []ledger.BenchCaseResult, *proto.Error) {
	if token == "" {
		return nil, nil, &proto.Error{Code: "args", Msg: "compare: empty run token"}
	}
	if token == tokenBaseline {
		bf, err := loadBaselineFile(d.ProjectRoot)
		if err != nil {
			return nil, nil, &proto.Error{Code: "no_baseline", Msg: "load bench/baseline.json: " + err.Error()}
		}
		rs, rows := baselineToRunSummary(bf)
		return &rs, rows, nil
	}
	if token == tokenLatest {
		runs, err := d.Ledger.QueryBenchRuns(1)
		if err != nil {
			return nil, nil, &proto.Error{Code: "ledger", Msg: err.Error()}
		}
		if len(runs) == 0 {
			return nil, nil, &proto.Error{Code: "no_runs", Msg: "no bench runs persisted yet"}
		}
		token = runs[0].RunUUID
	}
	run, cases, err := d.Ledger.QueryBenchRun(token)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, &proto.Error{Code: "not_found", Msg: "no run with uuid " + token}
		}
		return nil, nil, &proto.Error{Code: "ledger", Msg: err.Error()}
	}
	s := summarizeRun(d.Ledger, *run)
	return &s, cases, nil
}

// summarizeRun fills a RunSummary from a BenchRun + its cases. The
// per-case totals are loaded from the ledger to derive Cases /
// AshTokensTotal / BashTokensTotal / DeltaTokPct.
func summarizeRun(led *ledger.Ledger, r ledger.BenchRun) RunSummary {
	s := RunSummary{
		RunUUID:        r.RunUUID,
		Timestamp:      r.Timestamp,
		AshVersion:     r.AshVersion,
		AshCommitSHA:   r.AshCommitSHA,
		CaseSetVersion: r.CaseSetVersion,
		RepoSHA:        r.RepoSHA,
		RepoDirty:      r.RepoDirty,
		Platform:       r.Platform,
		RepeatN:        r.RepeatN,
		WarmupN:        r.WarmupN,
	}
	_, cases, err := led.QueryBenchRun(r.RunUUID)
	if err != nil {
		return s
	}
	s.Cases = len(cases)
	for _, c := range cases {
		s.AshTokensTotal += c.AshTokens
		s.BashTokensTotal += c.BashTokens
	}
	if s.BashTokensTotal != 0 {
		s.DeltaTokPct = float64(s.AshTokensTotal-s.BashTokensTotal) / float64(s.BashTokensTotal) * 100
	}
	return s
}

// buildCompare assembles the CompareResult from two run summaries +
// their per-case rows. Per-case rows are joined on case_name; cases
// present only in one side are flagged OnlyInA / OnlyInB.
func buildCompare(a, b RunSummary, casesA, casesB []ledger.BenchCaseResult, args *Args) *CompareResult {
	regTok := args.RegressTokPct
	if regTok <= 0 {
		regTok = 10
	}
	regLat := args.RegressLatPct
	if regLat <= 0 {
		regLat = 20
	}

	mapA := indexByCase(casesA)
	mapB := indexByCase(casesB)
	names := unionCaseNames(mapA, mapB)
	sort.Strings(names)

	res := &CompareResult{
		Kind:          kindCompare,
		A:             a,
		B:             b,
		CaseSetMatch:  a.CaseSetVersion == b.CaseSetVersion && a.CaseSetVersion != "",
		RegressTokPct: regTok,
		RegressLatPct: regLat,
	}
	for _, name := range names {
		ra, hasA := mapA[name]
		rb, hasB := mapB[name]
		d := CaseDelta{CaseName: name}
		switch {
		case hasA && hasB:
			d.Verb = ra.Verb
			d.AshTokA = ra.AshTokens
			d.AshTokB = rb.AshTokens
			d.BashTokA = ra.BashTokens
			d.BashTokB = rb.BashTokens
			d.AshLatUsP50A = ra.AshLatencyUsP50
			d.AshLatUsP50B = rb.AshLatencyUsP50
			d.DeltaTokPct = pctChange(ra.AshTokens, rb.AshTokens)
			d.DeltaLatPct = pctChangeInt64(ra.AshLatencyUsP50, rb.AshLatencyUsP50)
			d.Regressed, d.Improved = classifyDelta(d.DeltaTokPct, d.DeltaLatPct, float64(regTok), float64(regLat))
		case hasA:
			d.Verb = ra.Verb
			d.AshTokA = ra.AshTokens
			d.BashTokA = ra.BashTokens
			d.AshLatUsP50A = ra.AshLatencyUsP50
			d.OnlyInA = true
		case hasB:
			d.Verb = rb.Verb
			d.AshTokB = rb.AshTokens
			d.BashTokB = rb.BashTokens
			d.AshLatUsP50B = rb.AshLatencyUsP50
			d.OnlyInB = true
		}
		res.PerCase = append(res.PerCase, d)
		if d.Regressed {
			res.Regressions = append(res.Regressions, d)
		}
		if d.Improved {
			res.Improvements = append(res.Improvements, d)
		}
	}
	return res
}

func indexByCase(rows []ledger.BenchCaseResult) map[string]ledger.BenchCaseResult {
	out := make(map[string]ledger.BenchCaseResult, len(rows))
	for _, r := range rows {
		out[r.CaseName] = r
	}
	return out
}

func unionCaseNames(a, b map[string]ledger.BenchCaseResult) []string {
	seen := map[string]bool{}
	for k := range a {
		seen[k] = true
	}
	for k := range b {
		seen[k] = true
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	return out
}

// pctChange returns the percentage change from a to b. ((b-a)/a)*100.
// Returns 0 when a == 0 (avoids +inf clutter for cases that are
// genuinely zero-cost on one side).
func pctChange(a, b int) float64 {
	if a == 0 {
		return 0
	}
	return float64(b-a) / float64(a) * 100
}

func pctChangeInt64(a, b int64) float64 {
	if a == 0 {
		return 0
	}
	return float64(b-a) / float64(a) * 100
}

// classifyDelta applies the regression / improvement rule from the
// plan: a regression on either tokens or latency counts; improvement
// requires *both* directions move in the right direction.
func classifyDelta(dTok, dLat, regTok, regLat float64) (regress, improve bool) {
	if dTok > regTok || dLat > regLat {
		return true, false
	}
	if dTok < -regTok && dLat < -regLat {
		return false, true
	}
	return false, false
}

// prettyList renders the recent-runs table for `ash bench --list`.
func prettyList(lr *ListResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "§bench --list: %d run(s)\n", len(lr.Runs))
	if len(lr.Runs) == 0 {
		b.WriteString("no runs persisted yet (run `ash bench` first)")
		return b.String()
	}
	fmt.Fprintf(&b, "%-20s %-10s %-12s %-7s %s\n",
		"run_uuid (8)", "ts", "case_set", "Δtok%", "tokens (ash/bash) — repo")
	for _, r := range lr.Runs {
		uuidShort := r.RunUUID
		if len(uuidShort) > 8 {
			uuidShort = uuidShort[:8]
		}
		csv := r.CaseSetVersion
		if len(csv) > 12 {
			csv = csv[:12]
		}
		repo := ""
		if r.RepoSHA != "" {
			repo = r.RepoSHA[:min(7, len(r.RepoSHA))]
			if r.RepoDirty {
				repo += "-dirty"
			}
		}
		fmt.Fprintf(&b, "%-20s %-10s %-12s %+6.0f%% %d/%d — %s\n",
			uuidShort,
			r.Timestamp.Format("2006-01-02"),
			csv,
			r.DeltaTokPct,
			r.AshTokensTotal, r.BashTokensTotal,
			repo,
		)
	}
	return b.String()
}

// prettyCompare renders a side-by-side compare result.
func prettyCompare(cr *CompareResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "§bench compare: A=%s vs B=%s\n",
		shortRunID(cr.A.RunUUID), shortRunID(cr.B.RunUUID))
	if cr.CaseSetMatch {
		fmt.Fprintf(&b, "case-set: matched (%s)\n", cr.A.CaseSetVersion)
	} else {
		fmt.Fprintf(&b, "case-set: MISMATCH (A=%s, B=%s) — case additions/removals between runs\n",
			cr.A.CaseSetVersion, cr.B.CaseSetVersion)
	}
	if cr.A.RepoSHA != "" && cr.B.RepoSHA != "" {
		fmt.Fprintf(&b, "repo:     %s..%s\n",
			cr.A.RepoSHA[:min(7, len(cr.A.RepoSHA))],
			cr.B.RepoSHA[:min(7, len(cr.B.RepoSHA))])
	}
	fmt.Fprintf(&b, "thresholds: Δtok>+%d%% or Δlat>+%d%%\n", cr.RegressTokPct, cr.RegressLatPct)
	if cr.FreshRunUUID != "" {
		fmt.Fprintf(&b, "fresh run: %s\n", shortRunID(cr.FreshRunUUID))
	}
	b.WriteString("\n")

	fmt.Fprintf(&b, "%-26s %-8s %8s %8s %8s %10s %10s %8s %s\n",
		"case", "verb", "A_tok", "B_tok", "Δtok%", "A_us(p50)", "B_us(p50)", "Δlat%", "flag")
	for _, d := range cr.PerCase {
		flag := ""
		switch {
		case d.OnlyInA:
			flag = "ONLY_A"
		case d.OnlyInB:
			flag = "ONLY_B"
		case d.Regressed:
			flag = "REGRESS"
		case d.Improved:
			flag = "IMPROVE"
		}
		fmt.Fprintf(&b, "%-26s %-8s %8d %8d %+7.0f%% %10d %10d %+7.0f%% %s\n",
			truncStr(d.CaseName, 26), d.Verb,
			d.AshTokA, d.AshTokB, d.DeltaTokPct,
			d.AshLatUsP50A, d.AshLatUsP50B, d.DeltaLatPct,
			flag,
		)
	}
	fmt.Fprintf(&b, "\nregressions: %d   improvements: %d\n",
		len(cr.Regressions), len(cr.Improvements))
	return strings.TrimRight(b.String(), "\n")
}

func shortRunID(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}


