// Package bench implements the `ash bench` verb. For each case in
// internal/bench.Cases (or a filtered subset), it runs the ash verb
// in-process against the same runner registry the daemon uses, and
// runs the bash equivalent in a sandboxed subprocess. Both responses
// are tokenized with the same cl100k_base encoder so the comparison
// is honest — no estimator on either side.
//
// The bench result is returned as the verb's response and is *not*
// persisted to a separate ledger table in this first cut. The bench
// call itself is recorded by the daemon like any other verb.
//
// Args:
//
//	verb   string  (optional) - restrict to cases for one verb
//	case   string  (optional) - run a single named case
//	limit  int     (optional) - cap number of cases run after filters
package bench

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/stazelabs/ash/internal/bench"
	"github.com/stazelabs/ash/internal/ledger"
	"github.com/stazelabs/ash/internal/proto"
	"github.com/stazelabs/ash/internal/verbs/argutil"
)

// Deps carries everything the bench verb needs to dispatch ash verbs
// in-process. The daemon supplies these via closure when registering
// the runner; tests can construct them directly.
type Deps struct {
	Counter *ledger.Counter
	// Run dispatches a single verb call against the live runner registry
	// and returns (data, error). Mirrors verbs.Runner.Run but takes verb
	// name and is supplied by the daemon to avoid a circular import.
	Run func(verb string, args map[string]any) (any, *proto.Error)
	// Pretty renders the canonical pretty body for a verb's response.
	Pretty func(verb string, req *proto.Request, rsp *proto.Response) string
	// Ledger persists each bench run + its per-case rows into the
	// bench_runs / bench_case_results tables. Optional — nil disables
	// persistence (used in unit tests that don't want a sqlite db).
	Ledger *ledger.Ledger
	// DaemonStart is the daemon's process start time, used for
	// daemon_uptime_us in the provenance row. Zero value disables.
	DaemonStart time.Time
	// ProjectRoot is the daemon's configured root path. Used to read
	// repo_sha + repo_dirty from the project's git HEAD. Empty disables.
	ProjectRoot string
}

type Args struct {
	Verb   string
	Case   string
	Limit  int
	Repeat int     // measured iterations per case per side; default 1
	Warmup int     // unmeasured iterations per case per side; default 1 when Repeat>1, else 0

	// Trend / comparison flags. When any of these is set, the verb does
	// not run a fresh bench by itself (except --baseline, which runs a
	// new bench then compares against the rolling baseline).
	List       bool    // --list: list recent runs and exit
	ListLimit  int     // --list-limit N (default 20)
	CompareA   string  // --compare A,B → run_uuid A. Special tokens: "latest", "baseline"
	CompareB   string  //                  run_uuid B
	Baseline   string  // --baseline <dur>: run new bench, compare to rolling baseline window (e.g. "7d")
	RegressTokPct int // --regress-tokens 10 (default 10, meaning Δtok > +10%)
	RegressLatPct int // --regress-latency 20 (default 20, meaning Δlat > +20%)

	// Publishable artifact flags.
	RecordBaseline bool // --record-baseline: run fresh bench, write bench/baseline.json + bench/latency-snapshot.json
	ExportMd       bool // --export-md: render the latest persisted run as Markdown
}

// CaseResult is one row of the bench output. Latency is reported as
// p50 + min over Args.Repeat measured iterations; AshLatencyUs and
// BashLatencyUs are aliases for the p50 values, kept for backwards
// compatibility with consumers of the JSON wire shape.
type CaseResult struct {
	Name             string `msgpack:"name"`
	Verb             string `msgpack:"verb"`
	Why              string `msgpack:"why,omitempty"`
	AshTokens        int    `msgpack:"ash_tokens"`
	BashTokens       int    `msgpack:"bash_tokens"`
	AshLatencyUs     int64  `msgpack:"ash_latency_us"` // alias for AshLatencyUsP50
	BashLatencyUs    int64  `msgpack:"bash_latency_us"`
	AshLatencyUsP50  int64  `msgpack:"ash_latency_us_p50,omitempty"`
	AshLatencyUsMin  int64  `msgpack:"ash_latency_us_min,omitempty"`
	BashLatencyUsP50 int64  `msgpack:"bash_latency_us_p50,omitempty"`
	BashLatencyUsMin int64  `msgpack:"bash_latency_us_min,omitempty"`
	AshBytes         int    `msgpack:"ash_bytes"`
	BashBytes        int    `msgpack:"bash_bytes"`
	AshOK            bool   `msgpack:"ash_ok"`
	AshErr           string `msgpack:"ash_err,omitempty"`
	BashCmd          string `msgpack:"bash_cmd"`
	BashExit         int    `msgpack:"bash_exit"`
	BashRunErr       string `msgpack:"bash_run_err,omitempty"`
	BashTruncated    bool   `msgpack:"bash_truncated,omitempty"`
}

// VerbSummary aggregates CaseResult rows by verb.
type VerbSummary struct {
	Verb              string `msgpack:"verb"`
	Cases             int    `msgpack:"cases"`
	AshTokensTotal    int    `msgpack:"ash_tokens_total"`
	BashTokensTotal   int    `msgpack:"bash_tokens_total"`
	AshLatencyUsTotal int64  `msgpack:"ash_latency_us_total"`
	BashLatencyUsTotal int64 `msgpack:"bash_latency_us_total"`
}

type Result struct {
	RunUUID  string        `msgpack:"run_uuid,omitempty"`
	Cases    []CaseResult  `msgpack:"cases"`
	ByVerb   []VerbSummary `msgpack:"by_verb"`
	Overall  VerbSummary   `msgpack:"overall"`
	NotRun   []string      `msgpack:"not_run,omitempty"` // case names skipped (e.g. translation gap)
	NotRunWhy map[string]string `msgpack:"not_run_why,omitempty"`
}

func ParseArgs(in map[string]any) (*Args, *proto.Error) {
	a := &Args{}
	var perr *proto.Error
	if a.Verb, perr = argutil.OptionalString(in, "verb", ""); perr != nil {
		return nil, perr
	}
	if a.Case, perr = argutil.OptionalString(in, "case", ""); perr != nil {
		return nil, perr
	}
	if a.Limit, perr = argutil.OptionalNonNegInt(in, "limit", 0, 100); perr != nil {
		return nil, perr
	}
	if a.Repeat, perr = argutil.OptionalNonNegInt(in, "repeat", 1, 50); perr != nil {
		return nil, perr
	}
	if a.Repeat == 0 {
		a.Repeat = 1
	}
	// Warmup default = 1 when Repeat>1, else 0. Sentinel -1 in the wire
	// shape would be cleaner but argutil.OptionalNonNegInt won't allow
	// negatives, so we use the absence-of-key check directly.
	if _, hasWarmup := in["warmup"]; hasWarmup {
		if a.Warmup, perr = argutil.OptionalNonNegInt(in, "warmup", 0, 10); perr != nil {
			return nil, perr
		}
	} else if a.Repeat > 1 {
		a.Warmup = 1
	}

	if a.List, perr = argutil.OptionalBool(in, "list", false); perr != nil {
		return nil, perr
	}
	if a.ListLimit, perr = argutil.OptionalNonNegInt(in, "list_limit", 20, 1000); perr != nil {
		return nil, perr
	}
	if a.CompareA, perr = argutil.OptionalString(in, "compare_a", ""); perr != nil {
		return nil, perr
	}
	if a.CompareB, perr = argutil.OptionalString(in, "compare_b", ""); perr != nil {
		return nil, perr
	}
	if s, sperr := argutil.OptionalString(in, "compare", ""); sperr != nil {
		return nil, sperr
	} else if s != "" {
		// --compare A,B convenience form. The CLI splits on comma since
		// users almost always type the comma form rather than two flags.
		parts := strings.SplitN(s, ",", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return nil, &proto.Error{Code: "args", Msg: "compare must be \"A,B\" with both sides non-empty"}
		}
		a.CompareA = parts[0]
		a.CompareB = parts[1]
	}
	if a.Baseline, perr = argutil.OptionalString(in, "baseline", ""); perr != nil {
		return nil, perr
	}
	if a.RegressTokPct, perr = argutil.OptionalNonNegInt(in, "regress_tokens", 10, 1000); perr != nil {
		return nil, perr
	}
	if a.RegressLatPct, perr = argutil.OptionalNonNegInt(in, "regress_latency", 20, 1000); perr != nil {
		return nil, perr
	}
	if a.RecordBaseline, perr = argutil.OptionalBool(in, "record_baseline", false); perr != nil {
		return nil, perr
	}
	if a.ExportMd, perr = argutil.OptionalBool(in, "export_md", false); perr != nil {
		return nil, perr
	}
	return a, nil
}

// RunWithDeps is the single entry point for the bench verb. It
// dispatches on Args trend flags before falling through to the
// standard fresh-bench path. The any return type is the union over
// the three result shapes (Result, ListResult, CompareResult); the
// Kind field on List/Compare distinguishes them at decode time.
func RunWithDeps(d Deps, a *Args) (any, *proto.Error) {
	switch {
	case a.List:
		return runList(d, a)
	case a.CompareA != "":
		return runCompare(d, a)
	case a.Baseline != "":
		return runWithBaseline(d, a)
	case a.RecordBaseline:
		return runRecordBaseline(d, a)
	case a.ExportMd:
		return runExportMd(d, a)
	}
	return runStandard(d, a)
}

// runStandard runs a fresh bench: select cases, run each ash+bash
// pair, persist the result, and return the typed Result.
func runStandard(d Deps, a *Args) (*Result, *proto.Error) {
	if d.Counter == nil || d.Run == nil || d.Pretty == nil {
		return nil, &proto.Error{Code: "config", Msg: "bench: deps not wired"}
	}

	cases := selectCases(a)
	res := &Result{
		Cases:     make([]CaseResult, 0, len(cases)),
		NotRunWhy: map[string]string{},
	}

	for _, c := range cases {
		row, skipped, why := runCase(d, c, a.Repeat, a.Warmup)
		if skipped {
			res.NotRun = append(res.NotRun, c.Name)
			res.NotRunWhy[c.Name] = why
			continue
		}
		res.Cases = append(res.Cases, row)
	}

	res.ByVerb = aggregateByVerb(res.Cases)
	res.Overall = aggregateOverall(res.Cases)
	res.RunUUID = persistRun(d, res, a)

	// Best-effort cleanup of write/edit fixtures. Failure here only
	// leaves files behind in .ash/bench-tmp/ — gitignored, harmless.
	if err := bench.CleanupBenchTmpDir(); err != nil {
		log.Printf("bench: tmpdir cleanup: %v", err)
	}
	return res, nil
}

// persistRun writes the bench result to the bench_runs +
// bench_case_results tables. Best-effort: returns the run_uuid on
// success, "" on any failure (no ledger wired, provenance read failed,
// db write failed). Failures are logged but do not bubble up — bench
// results in the response are the source of truth for the immediate
// caller; persistence is for trend tracking only.
func persistRun(d Deps, res *Result, a *Args) string {
	if d.Ledger == nil {
		return ""
	}
	runUUID := newRunUUID()
	prov := bench.CaptureProvenance(d.DaemonStart, d.ProjectRoot)
	run := &ledger.BenchRun{
		RunUUID:        runUUID,
		Timestamp:      time.Now(),
		AshVersion:     prov.AshVersion,
		AshCommitSHA:   prov.AshCommitSHA,
		CaseSetVersion: prov.CaseSetVersion,
		RepoSHA:        prov.RepoSHA,
		RepoDirty:      prov.RepoDirty,
		Platform:       prov.Platform,
		CPUCount:       prov.CPUCount,
		DaemonUptimeUs: prov.DaemonUptimeUs,
		RepeatN:        a.Repeat,
		WarmupN:        a.Warmup,
	}
	if run.RepeatN < 1 {
		run.RepeatN = 1
	}
	rows := make([]ledger.BenchCaseResult, 0, len(res.Cases))
	for _, c := range res.Cases {
		rows = append(rows, ledger.BenchCaseResult{
			CaseName:         c.Name,
			Verb:             c.Verb,
			AshTokens:        c.AshTokens,
			BashTokens:       c.BashTokens,
			AshBytes:         c.AshBytes,
			BashBytes:        c.BashBytes,
			AshLatencyUsP50:  c.AshLatencyUsP50,
			AshLatencyUsMin:  c.AshLatencyUsMin,
			BashLatencyUsP50: c.BashLatencyUsP50,
			BashLatencyUsMin: c.BashLatencyUsMin,
			AshOK:            c.AshOK,
			AshErr:           c.AshErr,
			BashExit:         c.BashExit,
			BashRunErr:       c.BashRunErr,
			BashTruncated:    c.BashTruncated,
		})
	}
	if _, err := d.Ledger.RecordBenchRun(run, rows); err != nil {
		log.Printf("bench: persist: %v", err)
		return ""
	}
	return runUUID
}

func newRunUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("t%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

func selectCases(a *Args) []bench.Case {
	var out []bench.Case
	if a.Case != "" {
		if c := bench.FindCase(a.Case); c != nil {
			out = []bench.Case{*c}
		}
	} else {
		for _, c := range bench.Cases {
			if a.Verb != "" && c.Verb != a.Verb {
				continue
			}
			out = append(out, c)
		}
	}
	if a.Limit > 0 && len(out) > a.Limit {
		out = out[:a.Limit]
	}
	return out
}

// runCase executes one case end to end. Returns (row, skipped, reason).
//
// repeat is the number of measured iterations per side; warmup is the
// number of unmeasured iterations to discard before measurement.
// Tokens and bytes are pinned to the first measured iteration (they
// are deterministic given identical inputs); latency is reported as
// p50 and min over the measured samples.
func runCase(d Deps, c bench.Case, repeat, warmup int) (CaseResult, bool, string) {
	row := CaseResult{Name: c.Name, Verb: c.Verb, Why: c.Why}
	if repeat < 1 {
		repeat = 1
	}

	argv, terr := bench.BashFor(c)
	if terr != nil {
		return row, true, terr.Error()
	}
	row.BashCmd = strings.Join(argv, " ")

	runSetup := func() {
		if c.Setup != nil {
			if err := c.Setup(); err != nil {
				log.Printf("bench: setup %s: %v", c.Name, err)
			}
		}
	}

	for i := 0; i < warmup; i++ {
		runSetup()
		_, _, _ = runAshOnce(d, c)
		runSetup()
		_ = runBashOnce(argv)
	}

	ashLats := make([]int64, 0, repeat)
	bashLats := make([]int64, 0, repeat)

	runSetup()
	prettyAsh, perr, ashLat := runAshOnce(d, c)
	row.AshTokens = d.Counter.Count(prettyAsh)
	row.AshBytes = len(prettyAsh)
	if perr != nil {
		row.AshOK = false
		row.AshErr = perr.Code
	} else {
		row.AshOK = true
	}
	ashLats = append(ashLats, ashLat)

	runSetup()
	br := runBashOnce(argv)
	row.BashTokens = d.Counter.Count(string(br.Stdout))
	row.BashBytes = len(br.Stdout)
	row.BashExit = br.ExitCode
	row.BashRunErr = br.RunErr
	row.BashTruncated = br.Truncate
	bashLats = append(bashLats, br.Latency.Microseconds())

	for i := 1; i < repeat; i++ {
		runSetup()
		_, _, l := runAshOnce(d, c)
		ashLats = append(ashLats, l)
		runSetup()
		br := runBashOnce(argv)
		bashLats = append(bashLats, br.Latency.Microseconds())
	}

	row.AshLatencyUsP50 = percentileUs(ashLats, 0.5)
	row.AshLatencyUsMin = minUs(ashLats)
	row.BashLatencyUsP50 = percentileUs(bashLats, 0.5)
	row.BashLatencyUsMin = minUs(bashLats)
	row.AshLatencyUs = row.AshLatencyUsP50
	row.BashLatencyUs = row.BashLatencyUsP50

	return row, false, ""
}

// runAshOnce dispatches one ash verb call. Returns (prettyAsh, perr,
// latencyUs); pretty is the canonical token-counting substrate.
func runAshOnce(d Deps, c bench.Case) (string, *proto.Error, int64) {
	req := &proto.Request{V: proto.ProtocolVersion, Verb: c.Verb, Args: c.AshArgs}
	t0 := time.Now()
	data, perr := d.Run(c.Verb, c.AshArgs)
	dur := time.Since(t0)
	rsp := &proto.Response{V: proto.ProtocolVersion, ID: req.ID}
	if perr != nil {
		rsp.OK = false
		rsp.Err = perr
	} else {
		rsp.OK = true
		rsp.Data = proto.MustData(data)
	}
	return d.Pretty(c.Verb, req, rsp), perr, dur.Microseconds()
}

// runBashOnce executes one bash subprocess via the standard sandbox.
func runBashOnce(argv []string) bench.BashResult {
	ctx, cancel := context.WithTimeout(context.Background(), bench.DefaultBashTimeout)
	defer cancel()
	return bench.RunBash(ctx, argv)
}


func aggregateByVerb(rows []CaseResult) []VerbSummary {
	byVerb := map[string]*VerbSummary{}
	for _, r := range rows {
		s, ok := byVerb[r.Verb]
		if !ok {
			s = &VerbSummary{Verb: r.Verb}
			byVerb[r.Verb] = s
		}
		s.Cases++
		s.AshTokensTotal += r.AshTokens
		s.BashTokensTotal += r.BashTokens
		s.AshLatencyUsTotal += r.AshLatencyUs
		s.BashLatencyUsTotal += r.BashLatencyUs
	}
	out := make([]VerbSummary, 0, len(byVerb))
	for _, s := range byVerb {
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Verb < out[j].Verb })
	return out
}

func aggregateOverall(rows []CaseResult) VerbSummary {
	o := VerbSummary{Verb: "overall"}
	for _, r := range rows {
		o.Cases++
		o.AshTokensTotal += r.AshTokens
		o.BashTokensTotal += r.BashTokens
		o.AshLatencyUsTotal += r.AshLatencyUs
		o.BashLatencyUsTotal += r.BashLatencyUs
	}
	return o
}

// PrettyResponse renders the bench result. Switches on the response
// Kind field to pick between the standard run (legacy flat shape with
// no Kind), the --list output, and the --compare/--baseline output.
func PrettyResponse(req *proto.Request, rsp *proto.Response) string {
	if !rsp.OK {
		return proto.PrettyResponseHeader(rsp)
	}
	var probe struct {
		Kind string `msgpack:"kind"`
	}
	_ = proto.UnmarshalData(rsp, &probe)
	switch probe.Kind {
	case kindList:
		var lr ListResult
		if err := proto.UnmarshalData(rsp, &lr); err != nil {
			return "ok\n<unrecognized bench --list result>"
		}
		return prettyList(&lr)
	case kindCompare:
		var cr CompareResult
		if err := proto.UnmarshalData(rsp, &cr); err != nil {
			return "ok\n<unrecognized bench --compare result>"
		}
		return prettyCompare(&cr)
	case kindRecord:
		var r RecordBaselineResult
		if err := proto.UnmarshalData(rsp, &r); err != nil {
			return "ok\n<unrecognized bench --record-baseline result>"
		}
		return prettyRecord(&r)
	case kindExport:
		var r ExportMdResult
		if err := proto.UnmarshalData(rsp, &r); err != nil {
			return "ok\n<unrecognized bench --export-md result>"
		}
		return r.Body
	}
	var r Result
	if err := proto.UnmarshalData(rsp, &r); err != nil {
		return "ok\n<unrecognized bench result>"
	}

	var b strings.Builder
	b.WriteString("=== ash bench: ")
	fmt.Fprintf(&b, "%d case(s)", len(r.Cases))
	if len(r.NotRun) > 0 {
		fmt.Fprintf(&b, ", %d skipped", len(r.NotRun))
	}
	b.WriteString(" ===\n")

	// per-case rows
	fmt.Fprintf(&b, "%-26s %-8s %8s %8s %7s %10s %10s %7s\n",
		"case", "verb", "ash_tok", "bash_tok", "Δtok%", "ash_us", "bash_us", "Δlat%")
	for _, row := range r.Cases {
		dtok := pctDelta(row.AshTokens, row.BashTokens)
		dlat := pctDeltaInt64(row.AshLatencyUs, row.BashLatencyUs)
		extra := ""
		if !row.AshOK {
			extra += " ash_err=" + row.AshErr
		}
		if row.BashRunErr != "" {
			extra += " bash_err=" + truncStr(row.BashRunErr, 24)
		}
		if row.BashTruncated {
			extra += " bash_truncated"
		}
		fmt.Fprintf(&b, "%-26s %-8s %8d %8d %7s %10d %10d %7s%s\n",
			truncStr(row.Name, 26), row.Verb,
			row.AshTokens, row.BashTokens, dtok,
			row.AshLatencyUs, row.BashLatencyUs, dlat,
			extra)
	}

	if len(r.NotRun) > 0 {
		b.WriteString("\nnot run (no bash translation or other gap):\n")
		for _, n := range r.NotRun {
			fmt.Fprintf(&b, "  %s — %s\n", n, r.NotRunWhy[n])
		}
	}

	// per-verb summary
	if len(r.ByVerb) > 0 {
		b.WriteString("\nby verb:\n")
		fmt.Fprintf(&b, "  %-8s %5s %12s %12s %7s %12s %12s %7s\n",
			"verb", "n", "ash_tok", "bash_tok", "Δtok%", "ash_us", "bash_us", "Δlat%")
		for _, s := range r.ByVerb {
			fmt.Fprintf(&b, "  %-8s %5d %12d %12d %7s %12d %12d %7s\n",
				s.Verb, s.Cases,
				s.AshTokensTotal, s.BashTokensTotal,
				pctDelta(s.AshTokensTotal, s.BashTokensTotal),
				s.AshLatencyUsTotal, s.BashLatencyUsTotal,
				pctDeltaInt64(s.AshLatencyUsTotal, s.BashLatencyUsTotal))
		}
	}

	// overall
	o := r.Overall
	fmt.Fprintf(&b, "\noverall: n=%d  ash_tok=%d  bash_tok=%d  Δtok%%=%s  ash_us=%d  bash_us=%d  Δlat%%=%s\n",
		o.Cases, o.AshTokensTotal, o.BashTokensTotal,
		pctDelta(o.AshTokensTotal, o.BashTokensTotal),
		o.AshLatencyUsTotal, o.BashLatencyUsTotal,
		pctDeltaInt64(o.AshLatencyUsTotal, o.BashLatencyUsTotal))

	return strings.TrimRight(b.String(), "\n")
}

// pctDelta returns ash relative to bash as a percentage delta.
// Negative means ash is smaller (the win); positive means ash is larger.
func pctDelta(ash, bash int) string {
	if bash == 0 {
		if ash == 0 {
			return "0%"
		}
		return "+inf"
	}
	d := float64(ash-bash) / float64(bash) * 100
	return fmt.Sprintf("%+.0f%%", d)
}

func pctDeltaInt64(ash, bash int64) string {
	if bash == 0 {
		if ash == 0 {
			return "0%"
		}
		return "+inf"
	}
	d := float64(ash-bash) / float64(bash) * 100
	return fmt.Sprintf("%+.0f%%", d)
}

func truncStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

// percentileUs returns the p-th percentile of samples in microseconds.
// Uses the lower-rank convention (no interpolation): the value at
// floor((n-1)*p). Empty samples → 0.
func percentileUs(samples []int64, p float64) int64 {
	if len(samples) == 0 {
		return 0
	}
	s := append([]int64(nil), samples...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	idx := int(float64(len(s)-1) * p)
	if idx < 0 {
		idx = 0
	}
	if idx >= len(s) {
		idx = len(s) - 1
	}
	return s[idx]
}

// minUs returns the minimum sample, or 0 for empty input.
func minUs(samples []int64) int64 {
	if len(samples) == 0 {
		return 0
	}
	m := samples[0]
	for _, v := range samples[1:] {
		if v < m {
			m = v
		}
	}
	return m
}


