// Package report implements the `report` verb.
//
// Args:
//
//	session  string  (optional) - "current" (default), "all", or an explicit session ID
//	since    string  (optional) - duration window, e.g. "15m", "1h", "24h", "7d"
//	last     int     (optional) - row cap after session/since filters, max 5000
//	verb     string  (optional) - restrict to one verb
//	top      int     (optional) - max hotspot entries in each section, default 5, max 100
//
// Produces an aggregated per-verb summary (n, ok%, p50/p95 latency, p50/p95
// tokens_out, trunc%) rather than individual rows.
package report

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/stazelabs/ash/internal/ledger"
	"github.com/stazelabs/ash/internal/proto"
	"github.com/stazelabs/ash/internal/registry"
	"github.com/stazelabs/ash/internal/session"
	"github.com/stazelabs/ash/internal/verbs/argutil"
)

const MaxLast = 5000
const defaultTopN = 5
const MaxTopN = 100

type Args struct {
	Session string // "current", "all", or explicit session ID
	Since   time.Duration
	Last    int // 0 = no cap beyond DefaultWindowLimit
	Verb    string
	TopN    int // max hotspot entries per section; defaults to defaultTopN
	// Root selects an explicit project root whose ledger should be queried
	// instead of the daemons own ledger. Read-only. Mutually exclusive
	// with AllRoots.
	Root string
	// AllRoots reads every root in the installed-repos registry and
	// aggregates across all of them. Pretty form includes a per-root
	// breakdown.
	AllRoots bool
}

// Scope describes the filters that were applied, for display and JSON.
type Scope struct {
	Session string `msgpack:"session"`
	Since   string `msgpack:"since,omitempty"`
	Last    int    `msgpack:"last,omitempty"`
	Verb    string `msgpack:"verb,omitempty"`
	// Root is the project root whose ledger was queried. Empty when the
	// daemons own ledger is in use; populated for --root and --all-roots
	// (in --all-roots, set to a sentinel "ALL" value).
	Root string `msgpack:"root,omitempty"`
	// Roots is the per-root list when --all-roots is in effect. One entry
	// per ledger that was successfully opened, in registry order.
	Roots []string `msgpack:"roots,omitempty"`
}

// RootStats summarizes one ledgers contribution to an --all-roots run.
// Calls counts the rows that matched the scope filters; CallsTotal is
// the unfiltered ledger size for context.
type RootStats struct {
	Root        string `msgpack:"root"`
	Calls       int    `msgpack:"calls"`
	OK          int    `msgpack:"ok"`
	TokensOut   int64  `msgpack:"tokens_out"`
	ExecSumUs   int64  `msgpack:"exec_sum_us"`
	OpenError   string `msgpack:"open_error,omitempty"`
}

type Totals struct {
	Calls     int   `msgpack:"calls"`
	OK        int   `msgpack:"ok"`
	Errors    int   `msgpack:"errors"`
	TokensIn  int64 `msgpack:"tokens_in"`
	TokensOut int64 `msgpack:"tokens_out"`
	ExecSumUs int64 `msgpack:"exec_sum_us"`
}

// VerbSubPhase holds the percentage of exec_us attributed to one named phase.
// Phases: "walk" (exclusive walker overhead), "io", "regex", "other" (unattributed).
// They sum to 100 by construction when exec_us > 0.
type VerbSubPhase struct {
	Name string  `msgpack:"name"`
	Pct  float64 `msgpack:"pct"`
}

// ArgValueCount is one value seen for an arg key, with its call frequency.
type ArgValueCount struct {
	Value string `msgpack:"value"`
	Count int    `msgpack:"count"`
}

// ArgDist is the frequency distribution of values seen for one arg key.
type ArgDist struct {
	Key    string          `msgpack:"key"`
	Values []ArgValueCount `msgpack:"values"`
}

// VerbArgDist is the arg distributions for one verb.
type VerbArgDist struct {
	Verb string    `msgpack:"verb"`
	Args []ArgDist `msgpack:"args"`
}

type VerbStats struct {
	Verb         string         `msgpack:"verb"`
	N            int            `msgpack:"n"`
	OKCount      int            `msgpack:"ok_count"`
	OKPct        float64        `msgpack:"ok_pct"`
	P50ExecUs    int64          `msgpack:"p50_exec_us"`
	P95ExecUs    int64          `msgpack:"p95_exec_us"`
	P50TokensOut int64          `msgpack:"p50_tokens_out"`
	P95TokensOut int64          `msgpack:"p95_tokens_out"`
	TruncatedN   int            `msgpack:"truncated_n"`
	TruncatedPct float64        `msgpack:"truncated_pct"`
	SubPhases    []VerbSubPhase `msgpack:"sub_phases,omitempty"`
	TokPerKiB    float64        `msgpack:"tok_per_kib,omitempty"` // tokens_out / (bytes_out/1024)
}

// ErrEntry is one row in the error-code histogram.
type ErrEntry struct {
	Code      string `msgpack:"code"`
	Count     int    `msgpack:"count"`
	SampleMsg string `msgpack:"sample_msg"`
}

// TruncHotspot identifies a verb with truncated calls, sorted by count desc.
type TruncHotspot struct {
	Verb       string `msgpack:"verb"`
	Count      int    `msgpack:"count"`
	SampleArgs string `msgpack:"sample_args,omitempty"` // decoded from first truncated call's args_msgpack
}

type Result struct {
	Scope            Scope          `msgpack:"scope"`
	Totals           Totals         `msgpack:"totals"`
	ByVerb           []VerbStats    `msgpack:"by_verb"`
	ErrHistogram     []ErrEntry     `msgpack:"err_histogram,omitempty"`
	TruncHotspots    []TruncHotspot `msgpack:"trunc_hotspots,omitempty"`
	ArgDistributions []VerbArgDist  `msgpack:"arg_distributions,omitempty"`
	// ByRoot is populated only by --all-roots: per-ledger contribution
	// counts, in registry order. Empty for single-root reports.
	ByRoot []RootStats `msgpack:"by_root,omitempty"`
}

func ParseArgs(in map[string]any) (*Args, *proto.Error) {
	a := &Args{}
	var perr *proto.Error
	if a.Session, perr = argutil.OptionalNonEmptyString(in, "session", "current"); perr != nil {
		return nil, perr
	}
	if since, perr := argutil.OptionalString(in, "since", ""); perr != nil {
		return nil, perr
	} else if since != "" {
		d, err := parseDuration(since)
		if err != nil {
			return nil, &proto.Error{Code: "args", Msg: "since: " + err.Error()}
		}
		a.Since = d
	}
	if a.Last, perr = argutil.OptionalNonNegInt(in, "last", 0, MaxLast); perr != nil {
		return nil, perr
	}
	if a.Verb, perr = argutil.OptionalString(in, "verb", ""); perr != nil {
		return nil, perr
	}
	if a.TopN, perr = argutil.OptionalPosInt(in, "top", defaultTopN, MaxTopN); perr != nil {
		return nil, perr
	}
	if a.Root, perr = argutil.OptionalString(in, "root", ""); perr != nil {
		return nil, perr
	}
	if a.AllRoots, perr = argutil.OptionalBool(in, "all_roots", false); perr != nil {
		return nil, perr
	}
	if a.Root != "" && a.AllRoots {
		return nil, &proto.Error{Code: "args", Msg: "--root and --all_roots are mutually exclusive"}
	}
	// "current" only makes sense against the daemons own ledger. When a
	// foreign ledger is in scope, default to no session filter unless the
	// caller explicitly named one.
	if (a.Root != "" || a.AllRoots) && a.Session == "current" {
		a.Session = ""
	}
	return a, nil
}

func RunWithLedger(led *ledger.Ledger, a *Args) (*Result, *proto.Error) {
	if a.AllRoots {
		return runAllRoots(a)
	}
	if a.Root != "" {
		return runForeignRoot(a, a.Root)
	}

	opts := ledger.QueryOpts{
		SessionID:  a.Session,
		VerbFilter: a.Verb,
		Limit:      a.Last,
	}
	if a.Since > 0 {
		opts.Since = time.Now().Add(-a.Since)
	}

	calls, err := led.QueryWindow(opts)
	if err != nil {
		return nil, &proto.Error{Code: "ledger", Msg: err.Error()}
	}

	scope := Scope{Session: a.Session}
	if a.Since > 0 {
		scope.Since = a.Since.String()
	}
	if a.Last > 0 {
		scope.Last = a.Last
	}
	scope.Verb = a.Verb

	result := aggregate(calls, scope)

	capHotspots(result, a.TopN)
	return result, nil
}

// capHotspots applies TopN limits to hotspot/error/arg-dist sections.
// Shared between the daemon-ledger path and the foreign-ledger paths so
// every report shape obeys the same rendering caps.
func capHotspots(r *Result, topN int) {
	if topN <= 0 {
		return
	}
	if len(r.TruncHotspots) > topN {
		r.TruncHotspots = r.TruncHotspots[:topN]
	}
	if len(r.ErrHistogram) > topN {
		r.ErrHistogram = r.ErrHistogram[:topN]
	}
	for i := range r.ArgDistributions {
		for j := range r.ArgDistributions[i].Args {
			if len(r.ArgDistributions[i].Args[j].Values) > topN {
				r.ArgDistributions[i].Args[j].Values = r.ArgDistributions[i].Args[j].Values[:topN]
			}
		}
	}
}

// runForeignRoot opens the ledger of an arbitrary project root read-only
// and runs the standard aggregation against it. The daemons own ledger
// is not touched. A missing ledger.db is a verb-level error so the agent
// notices typos in --root.
func runForeignRoot(a *Args, root string) (*Result, *proto.Error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, &proto.Error{Code: "args", Msg: "resolving --root: " + err.Error()}
	}
	led, perr := openForeign(abs)
	if perr != nil {
		return nil, perr
	}
	defer led.Close()

	calls, qerr := queryForeign(led, a)
	if qerr != nil {
		return nil, qerr
	}
	scope := buildScope(a)
	scope.Root = abs
	result := aggregate(calls, scope)
	capHotspots(result, a.TopN)
	return result, nil
}

// runAllRoots walks the installed-repos registry, queries each ledger
// read-only, and aggregates calls across all of them. Per-root counts
// are returned as ByRoot for the pretty-form breakdown. Roots whose
// ledger.db is missing or unreadable surface as a RootStats entry with
// OpenError populated rather than failing the whole call — the typical
// case is a target that has been initialized but not used yet.
func runAllRoots(a *Args) (*Result, *proto.Error) {
	roots, err := registry.List()
	if err != nil {
		return nil, &proto.Error{Code: "registry", Msg: err.Error()}
	}
	if len(roots) == 0 {
		return nil, &proto.Error{Code: "no_roots", Msg: "registry is empty (run `ash init` in target repos)"}
	}

	scope := buildScope(a)
	scope.Root = "ALL"
	scope.Roots = roots

	var allCalls []ledger.Call
	byRoot := make([]RootStats, 0, len(roots))
	for _, root := range roots {
		stat := RootStats{Root: root}
		led, perr := openForeign(root)
		if perr != nil {
			stat.OpenError = perr.Code + ": " + perr.Msg
			byRoot = append(byRoot, stat)
			continue
		}
		calls, qerr := queryForeign(led, a)
		led.Close()
		if qerr != nil {
			stat.OpenError = qerr.Code + ": " + qerr.Msg
			byRoot = append(byRoot, stat)
			continue
		}
		stat.Calls = len(calls)
		for _, c := range calls {
			if c.OK {
				stat.OK++
			}
			stat.TokensOut += int64(c.TokensOut)
			stat.ExecSumUs += c.LatencyExecUs
		}
		allCalls = append(allCalls, calls...)
		byRoot = append(byRoot, stat)
	}

	result := aggregate(allCalls, scope)
	result.ByRoot = byRoot
	capHotspots(result, a.TopN)
	return result, nil
}

// openForeign opens <root>/.ash/ledger.db read-only. Wraps
// ledger.OpenReadOnly with proto.Error normalization.
func openForeign(root string) (*ledger.Ledger, *proto.Error) {
	path := session.LedgerPath(root)
	led, err := ledger.OpenReadOnly(path)
	if err != nil {
		return nil, &proto.Error{Code: "ledger_open", Msg: path + ": " + err.Error()}
	}
	return led, nil
}

// queryForeign applies the Args filters to a foreign (read-only) ledger.
// Mirrors the daemon-ledger query setup in RunWithLedger.
func queryForeign(led *ledger.Ledger, a *Args) ([]ledger.Call, *proto.Error) {
	opts := ledger.QueryOpts{
		SessionID:  a.Session,
		VerbFilter: a.Verb,
		Limit:      a.Last,
	}
	if a.Since > 0 {
		opts.Since = time.Now().Add(-a.Since)
	}
	calls, err := led.QueryWindow(opts)
	if err != nil {
		return nil, &proto.Error{Code: "ledger", Msg: err.Error()}
	}
	return calls, nil
}

func buildScope(a *Args) Scope {
	scope := Scope{Session: a.Session, Verb: a.Verb}
	if a.Since > 0 {
		scope.Since = a.Since.String()
	}
	if a.Last > 0 {
		scope.Last = a.Last
	}
	return scope
}

func aggregate(calls []ledger.Call, scope Scope) *Result {
	totals := Totals{Calls: len(calls)}
	byVerb := map[string][]ledger.Call{}
	order := []string{}

	// Error histogram state.
	errCounts := map[string]int{}
	errSample := map[string]string{}
	var errOrder []string

	for _, c := range calls {
		totals.TokensIn += int64(c.TokensIn)
		totals.TokensOut += int64(c.TokensOut)
		totals.ExecSumUs += c.LatencyExecUs
		if c.OK {
			totals.OK++
		} else {
			totals.Errors++
			if c.ErrCode != "" {
				if _, seen := errCounts[c.ErrCode]; !seen {
					errOrder = append(errOrder, c.ErrCode)
					errSample[c.ErrCode] = c.ErrMsg
				}
				errCounts[c.ErrCode]++
			}
		}
		if _, seen := byVerb[c.Verb]; !seen {
			order = append(order, c.Verb)
		}
		byVerb[c.Verb] = append(byVerb[c.Verb], c)
	}

	// firstTruncArgs captures the ArgsMsgpack of the first truncated call per verb.
	firstTruncArgs := map[string][]byte{}

	stats := make([]VerbStats, 0, len(order))
	for _, verb := range order {
		cs := byVerb[verb]
		vs := VerbStats{Verb: verb, N: len(cs)}
		execUs := make([]int64, len(cs))
		tokOut := make([]int64, len(cs))
		var sumExec, sumWalk, sumIO, sumRegex, sumRegexCompile int64
		var sumTokOut, sumBytesOut int64
		for i, c := range cs {
			if c.OK {
				vs.OKCount++
			}
			if c.Truncated {
				vs.TruncatedN++
				if _, seen := firstTruncArgs[verb]; !seen {
					firstTruncArgs[verb] = c.ArgsMsgpack
				}
			}
			execUs[i] = c.LatencyExecUs
			tokOut[i] = int64(c.TokensOut)
			sumExec += c.LatencyExecUs
			sumWalk += c.WalkUs
			sumIO += c.IOUs
			sumRegex += c.RegexUs
			sumRegexCompile += c.RegexCompileUs
			sumTokOut += int64(c.TokensOut)
			sumBytesOut += int64(c.BytesOut)
		}
		vs.OKPct = pct(vs.OKCount, vs.N)
		vs.TruncatedPct = pct(vs.TruncatedN, vs.N)
		vs.P50ExecUs = percentile(execUs, 0.50)
		vs.P95ExecUs = percentile(execUs, 0.95)
		vs.P50TokensOut = percentile(tokOut, 0.50)
		vs.P95TokensOut = percentile(tokOut, 0.95)

		// Sub-phase breakdown: only emit when at least one call had phase data.
		// walk% is exclusive walker overhead (WalkUs minus its IO/regex subsets).
		// other% is exec time outside walker.Walk entirely.
		if sumExec > 0 && (sumWalk > 0 || sumIO > 0 || sumRegex > 0 || sumRegexCompile > 0) {
			walkExcl := sumWalk - sumIO - sumRegex - sumRegexCompile
			if walkExcl < 0 {
				walkExcl = 0
			}
			walkPct := pctOf(walkExcl, sumExec)
			ioPct := pctOf(sumIO, sumExec)
			regexPct := pctOf(sumRegex, sumExec)
			regexCompilePct := pctOf(sumRegexCompile, sumExec)
			otherPct := 100.0 - walkPct - ioPct - regexPct - regexCompilePct
			if otherPct < 0 {
				otherPct = 0
			}
			vs.SubPhases = []VerbSubPhase{
				{Name: "walk", Pct: walkPct},
				{Name: "io", Pct: ioPct},
				{Name: "regex", Pct: regexPct},
				{Name: "regex_compile", Pct: regexCompilePct},
				{Name: "other", Pct: otherPct},
			}
		}

		// Token efficiency: tokens per KiB of response bytes.
		if sumBytesOut > 0 {
			vs.TokPerKiB = float64(sumTokOut) / (float64(sumBytesOut) / 1024.0)
		}

		stats = append(stats, vs)
	}

	// Build error histogram sorted by count desc.
	sort.Slice(errOrder, func(i, j int) bool {
		return errCounts[errOrder[i]] > errCounts[errOrder[j]]
	})
	var errHist []ErrEntry
	for _, code := range errOrder {
		errHist = append(errHist, ErrEntry{Code: code, Count: errCounts[code], SampleMsg: errSample[code]})
	}

	// Build truncation hotspots from per-verb stats, sorted by count desc.
	var truncHotspots []TruncHotspot
	for _, vs := range stats {
		if vs.TruncatedN > 0 {
			truncHotspots = append(truncHotspots, TruncHotspot{
				Verb:       vs.Verb,
				Count:      vs.TruncatedN,
				SampleArgs: decodeArgsSummary(firstTruncArgs[vs.Verb]),
			})
		}
	}
	sort.Slice(truncHotspots, func(i, j int) bool {
		return truncHotspots[i].Count > truncHotspots[j].Count
	})

	return &Result{
		Scope:            scope,
		Totals:           totals,
		ByVerb:           stats,
		ErrHistogram:     errHist,
		TruncHotspots:    truncHotspots,
		ArgDistributions: collectArgDists(byVerb, order),
	}
}

// collectArgDists builds per-verb arg frequency distributions from call ArgsMsgpack blobs.
// Keys are sorted alphabetically; values within each key are sorted by count desc.
// Callers cap values to topN via RunWithLedger.
func collectArgDists(byVerb map[string][]ledger.Call, order []string) []VerbArgDist {
	var result []VerbArgDist
	for _, verb := range order {
		cs := byVerb[verb]
		counts := map[string]map[string]int{} // [key][value]count
		var keyOrder []string
		for _, c := range cs {
			if len(c.ArgsMsgpack) == 0 {
				continue
			}
			req, err := proto.DecodeRequest(c.ArgsMsgpack)
			if err != nil || len(req.Args) == 0 {
				continue
			}
			for k, v := range req.Args {
				val := fmt.Sprintf("%v", v)
				if val == "" {
					continue
				}
				if _, seen := counts[k]; !seen {
					counts[k] = map[string]int{}
					keyOrder = append(keyOrder, k)
				}
				counts[k][val]++
			}
		}
		if len(keyOrder) == 0 {
			continue
		}
		sort.Strings(keyOrder)
		var dists []ArgDist
		for _, k := range keyOrder {
			vals := make([]ArgValueCount, 0, len(counts[k]))
			for val, cnt := range counts[k] {
				vals = append(vals, ArgValueCount{Value: val, Count: cnt})
			}
			sort.Slice(vals, func(i, j int) bool {
				if vals[i].Count != vals[j].Count {
					return vals[i].Count > vals[j].Count
				}
				return vals[i].Value < vals[j].Value
			})
			dists = append(dists, ArgDist{Key: k, Values: vals})
		}
		result = append(result, VerbArgDist{Verb: verb, Args: dists})
	}
	return result
}

func percentile(vals []int64, p float64) int64 {
	if len(vals) == 0 {
		return 0
	}
	sorted := make([]int64, len(vals))
	copy(sorted, vals)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	idx := int(p * float64(len(sorted)-1))
	return sorted[idx]
}

func pct(n, total int) float64 {
	if total == 0 {
		return 0
	}
	return float64(n) / float64(total) * 100
}

func pctOf(num, denom int64) float64 {
	if denom == 0 {
		return 0
	}
	return float64(num) / float64(denom) * 100
}

func PrettyResponse(req *proto.Request, rsp *proto.Response) string {
	if !rsp.OK {
		return proto.PrettyResponseHeader(rsp)
	}
	r, ok := decodeResult(rsp.Data)
	if !ok {
		return "ok\n<unrecognized report result>"
	}

	var b strings.Builder

	// Header
	sessionLabel := r.Scope.Session
	if sessionLabel == "" {
		sessionLabel = "all"
	}
	if r.Scope.Since != "" {
		sessionLabel += ", since=" + r.Scope.Since
	}
	if r.Scope.Verb != "" {
		sessionLabel += ", verb=" + r.Scope.Verb
	}
	if r.Scope.Root == "ALL" {
		sessionLabel += fmt.Sprintf(", roots=%d", len(r.Scope.Roots))
	} else if r.Scope.Root != "" {
		sessionLabel += ", root=" + r.Scope.Root
	}
	fmt.Fprintf(&b, "=== ash report: %s \xe2\x80\x94 %d calls, %s exec ===\n",
		sessionLabel, r.Totals.Calls, fmtUs(r.Totals.ExecSumUs))

	// Totals line
	okPct := pct(int(r.Totals.OK), int(r.Totals.Calls))
	fmt.Fprintf(&b, "totals: ok=%d/%d (%.0f%%), tokens_in=%d, tokens_out=%d\n",
		r.Totals.OK, r.Totals.Calls, okPct, r.Totals.TokensIn, r.Totals.TokensOut)

	if len(r.ByVerb) == 0 {
		b.WriteString("(no calls)\n")
		return strings.TrimRight(b.String(), "\n")
	}

	// Per-verb table
	fmt.Fprintf(&b, "\n%-10s  %4s  %4s  %9s  %9s  %7s  %7s  %6s\n",
		"verb", "n", "ok%", "p50_exec", "p95_exec", "p50_out", "p95_out", "trunc%")
	fmt.Fprintf(&b, "%s\n", strings.Repeat("-", 72))
	for _, vs := range r.ByVerb {
		fmt.Fprintf(&b, "%-10s  %4d  %3.0f%%  %9s  %9s  %7d  %7d  %5.0f%%\n",
			vs.Verb, vs.N, vs.OKPct,
			fmtUs(vs.P50ExecUs), fmtUs(vs.P95ExecUs),
			vs.P50TokensOut, vs.P95TokensOut,
			vs.TruncatedPct)
	}

	// Per-root breakdown — only emitted by --all-roots.
	if len(r.ByRoot) > 0 {
		fmt.Fprintf(&b, "\nby root (%d):\n", len(r.ByRoot))
		fmt.Fprintf(&b, "%-50s  %5s  %4s  %9s  %9s\n", "root", "n", "ok%", "tok_out", "exec")
		fmt.Fprintf(&b, "%s\n", strings.Repeat("-", 86))
		for _, rs := range r.ByRoot {
			label := rs.Root
			if len(label) > 50 {
				label = "..." + label[len(label)-47:]
			}
			if rs.OpenError != "" {
				fmt.Fprintf(&b, "%-50s  %s\n", label, rs.OpenError)
				continue
			}
			okPct := pct(rs.OK, rs.Calls)
			fmt.Fprintf(&b, "%-50s  %5d  %3.0f%%  %9d  %9s\n",
				label, rs.Calls, okPct, rs.TokensOut, fmtUs(rs.ExecSumUs))
		}
	}

	// Sub-phase breakdown section — only when at least one verb has phase data.
	hasSubPhases := false
	for _, vs := range r.ByVerb {
		if len(vs.SubPhases) > 0 {
			hasSubPhases = true
			break
		}
	}
	if hasSubPhases {
		fmt.Fprintf(&b, "\nsub-phase breakdown (%% of exec, verbs that instrument phases):\n")
		fmt.Fprintf(&b, "%-10s  %5s  %4s  %6s  %6s\n", "verb", "walk%", "io%", "regex%", "other%")
		fmt.Fprintf(&b, "%s\n", strings.Repeat("-", 40))
		for _, vs := range r.ByVerb {
			if len(vs.SubPhases) == 0 {
				continue
			}
			walkPct := subPhasePct(vs.SubPhases, "walk")
			ioPct := subPhasePct(vs.SubPhases, "io")
			regexPct := subPhasePct(vs.SubPhases, "regex")
			otherPct := subPhasePct(vs.SubPhases, "other")
			fmt.Fprintf(&b, "%-10s  %4.0f%%  %3.0f%%  %5.0f%%  %5.0f%%\n",
				vs.Verb, walkPct, ioPct, regexPct, otherPct)
		}
	}

	// Truncation hotspots — only when at least one call was truncated.
	if len(r.TruncHotspots) > 0 {
		total := 0
		for _, th := range r.TruncHotspots {
			total += th.Count
		}
		fmt.Fprintf(&b, "\ntruncation (%d truncated):\n", total)
		for _, th := range r.TruncHotspots {
			if th.SampleArgs != "" {
				fmt.Fprintf(&b, "  %s \xc3\x97 %d  \xe2\x80\x94 %s\n", th.Verb, th.Count, th.SampleArgs)
			} else {
				fmt.Fprintf(&b, "  %s \xc3\x97 %d\n", th.Verb, th.Count)
			}
		}
	}

	// Error histogram — only when there are errors with a known code.
	if len(r.ErrHistogram) > 0 {
		total := 0
		for _, e := range r.ErrHistogram {
			total += e.Count
		}
		fmt.Fprintf(&b, "\nerrors (%d):\n", total)
		for _, e := range r.ErrHistogram {
			if e.SampleMsg != "" {
				fmt.Fprintf(&b, "  %s \xc3\x97 %d  \xe2\x80\x94 %q\n", e.Code, e.Count, e.SampleMsg)
			} else {
				fmt.Fprintf(&b, "  %s \xc3\x97 %d\n", e.Code, e.Count)
			}
		}
	}

	// Token efficiency — only when at least one verb has bytes_out data.
	hasTokPerKiB := false
	for _, vs := range r.ByVerb {
		if vs.TokPerKiB > 0 {
			hasTokPerKiB = true
			break
		}
	}
	if hasTokPerKiB {
		fmt.Fprintf(&b, "\ntoken efficiency (tokens per KiB of response):\n")
		fmt.Fprintf(&b, "%-10s  %8s\n", "verb", "tok/KiB")
		fmt.Fprintf(&b, "%s\n", strings.Repeat("-", 22))
		for _, vs := range r.ByVerb {
			if vs.TokPerKiB <= 0 {
				continue
			}
			fmt.Fprintf(&b, "%-10s  %7.1f\n", vs.Verb, vs.TokPerKiB)
		}
	}

	// Arg distributions — only when at least one verb has decoded args.
	if len(r.ArgDistributions) > 0 {
		fmt.Fprintf(&b, "\narg distributions:\n")
		for _, vd := range r.ArgDistributions {
			indent := strings.Repeat(" ", 2+len(vd.Verb)+2)
			for i, d := range vd.Args {
				var parts []string
				for _, v := range d.Values {
					val := v.Value
					if len(val) > 40 {
						val = val[:37] + "..."
					}
					parts = append(parts, fmt.Sprintf("%s (%d\xc3\x97)", val, v.Count))
				}
				valStr := strings.Join(parts, ", ")
				if i == 0 {
					fmt.Fprintf(&b, "  %s  %s: %s\n", vd.Verb, d.Key, valStr)
				} else {
					fmt.Fprintf(&b, "%s%s: %s\n", indent, d.Key, valStr)
				}
			}
		}
	}

	return strings.TrimRight(b.String(), "\n")
}

func subPhasePct(phases []VerbSubPhase, name string) float64 {
	for _, p := range phases {
		if p.Name == name {
			return p.Pct
		}
	}
	return 0
}

// fmtUs formats microseconds as e.g. "142us", "2.4ms", "1.2s".
func fmtUs(us int64) string {
	switch {
	case us < 1000:
		return fmt.Sprintf("%dus", us)
	case us < 1_000_000:
		return fmt.Sprintf("%.1fms", float64(us)/1000)
	default:
		return fmt.Sprintf("%.1fs", float64(us)/1_000_000)
	}
}

// decodeArgsSummary decodes a raw request msgpack blob and returns a compact
// key=value summary of the args (e.g. "glob=**/*.go path=. limit=256").
// Returns "" on any error or if no args are present.
func decodeArgsSummary(blob []byte) string {
	if len(blob) == 0 {
		return ""
	}
	req, err := proto.DecodeRequest(blob)
	if err != nil || len(req.Args) == 0 {
		return ""
	}
	keys := make([]string, 0, len(req.Args))
	for k := range req.Args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		v := fmt.Sprintf("%v", req.Args[k])
		if v != "" {
			parts = append(parts, k+"="+v)
		}
	}
	return strings.Join(parts, " ")
}

func decodeResult(data any) (*Result, bool) {
	if r, ok := data.(*Result); ok {
		return r, true
	}
	m, ok := data.(map[string]any)
	if !ok {
		return nil, false
	}
	r := &Result{}

	if sm, ok := m["scope"].(map[string]any); ok {
		if v, ok := sm["session"].(string); ok {
			r.Scope.Session = v
		}
		if v, ok := sm["since"].(string); ok {
			r.Scope.Since = v
		}
		if v, ok := argutil.ToInt(sm["last"]); ok {
			r.Scope.Last = v
		}
		if v, ok := sm["verb"].(string); ok {
			r.Scope.Verb = v
		}
		if v, ok := sm["root"].(string); ok {
			r.Scope.Root = v
		}
		if raw, ok := sm["roots"].([]any); ok {
			for _, x := range raw {
				if s, ok := x.(string); ok {
					r.Scope.Roots = append(r.Scope.Roots, s)
				}
			}
		}
	}
	if tm, ok := m["totals"].(map[string]any); ok {
		if v, ok := argutil.ToInt(tm["calls"]); ok {
			r.Totals.Calls = v
		}
		if v, ok := argutil.ToInt(tm["ok"]); ok {
			r.Totals.OK = v
		}
		if v, ok := argutil.ToInt(tm["errors"]); ok {
			r.Totals.Errors = v
		}
		if v, ok := argutil.ToInt64(tm["tokens_in"]); ok {
			r.Totals.TokensIn = v
		}
		if v, ok := argutil.ToInt64(tm["tokens_out"]); ok {
			r.Totals.TokensOut = v
		}
		if v, ok := argutil.ToInt64(tm["exec_sum_us"]); ok {
			r.Totals.ExecSumUs = v
		}
	}
	if raw, ok := m["by_verb"].([]any); ok {
		for _, x := range raw {
			vm, ok := x.(map[string]any)
			if !ok {
				continue
			}
			vs := VerbStats{}
			if v, ok := vm["verb"].(string); ok {
				vs.Verb = v
			}
			if v, ok := argutil.ToInt(vm["n"]); ok {
				vs.N = v
			}
			if v, ok := argutil.ToInt(vm["ok_count"]); ok {
				vs.OKCount = v
			}
			if v, ok := toFloat64(vm["ok_pct"]); ok {
				vs.OKPct = v
			}
			if v, ok := argutil.ToInt64(vm["p50_exec_us"]); ok {
				vs.P50ExecUs = v
			}
			if v, ok := argutil.ToInt64(vm["p95_exec_us"]); ok {
				vs.P95ExecUs = v
			}
			if v, ok := argutil.ToInt64(vm["p50_tokens_out"]); ok {
				vs.P50TokensOut = v
			}
			if v, ok := argutil.ToInt64(vm["p95_tokens_out"]); ok {
				vs.P95TokensOut = v
			}
			if v, ok := argutil.ToInt(vm["truncated_n"]); ok {
				vs.TruncatedN = v
			}
			if v, ok := toFloat64(vm["truncated_pct"]); ok {
				vs.TruncatedPct = v
			}
			if v, ok := toFloat64(vm["tok_per_kib"]); ok {
				vs.TokPerKiB = v
			}
			if rawSP, ok := vm["sub_phases"].([]any); ok {
				for _, px := range rawSP {
					pm, ok := px.(map[string]any)
					if !ok {
						continue
					}
					sp := VerbSubPhase{}
					if v, ok := pm["name"].(string); ok {
						sp.Name = v
					}
					if v, ok := toFloat64(pm["pct"]); ok {
						sp.Pct = v
					}
					vs.SubPhases = append(vs.SubPhases, sp)
				}
			}
			r.ByVerb = append(r.ByVerb, vs)
		}
	}
	if raw, ok := m["err_histogram"].([]any); ok {
		for _, x := range raw {
			em, ok := x.(map[string]any)
			if !ok {
				continue
			}
			e := ErrEntry{}
			if v, ok := em["code"].(string); ok {
				e.Code = v
			}
			if v, ok := argutil.ToInt(em["count"]); ok {
				e.Count = v
			}
			if v, ok := em["sample_msg"].(string); ok {
				e.SampleMsg = v
			}
			r.ErrHistogram = append(r.ErrHistogram, e)
		}
	}
	if raw, ok := m["trunc_hotspots"].([]any); ok {
		for _, x := range raw {
			tm, ok := x.(map[string]any)
			if !ok {
				continue
			}
			th := TruncHotspot{}
			if v, ok := tm["verb"].(string); ok {
				th.Verb = v
			}
			if v, ok := argutil.ToInt(tm["count"]); ok {
				th.Count = v
			}
			if v, ok := tm["sample_args"].(string); ok {
				th.SampleArgs = v
			}
			r.TruncHotspots = append(r.TruncHotspots, th)
		}
	}
	if raw, ok := m["arg_distributions"].([]any); ok {
		for _, x := range raw {
			vdm, ok := x.(map[string]any)
			if !ok {
				continue
			}
			vd := VerbArgDist{}
			if v, ok := vdm["verb"].(string); ok {
				vd.Verb = v
			}
			if args, ok := vdm["args"].([]any); ok {
				for _, ax := range args {
					am, ok := ax.(map[string]any)
					if !ok {
						continue
					}
					d := ArgDist{}
					if v, ok := am["key"].(string); ok {
						d.Key = v
					}
					if vals, ok := am["values"].([]any); ok {
						for _, vx := range vals {
							vm2, ok := vx.(map[string]any)
							if !ok {
								continue
							}
							vc := ArgValueCount{}
							if v, ok := vm2["value"].(string); ok {
								vc.Value = v
							}
							if v, ok := argutil.ToInt(vm2["count"]); ok {
								vc.Count = v
							}
							d.Values = append(d.Values, vc)
						}
					}
					vd.Args = append(vd.Args, d)
				}
			}
			r.ArgDistributions = append(r.ArgDistributions, vd)
		}
	}
	if raw, ok := m["by_root"].([]any); ok {
		for _, x := range raw {
			rm, ok := x.(map[string]any)
			if !ok {
				continue
			}
			rs := RootStats{}
			if v, ok := rm["root"].(string); ok {
				rs.Root = v
			}
			if v, ok := argutil.ToInt(rm["calls"]); ok {
				rs.Calls = v
			}
			if v, ok := argutil.ToInt(rm["ok"]); ok {
				rs.OK = v
			}
			if v, ok := argutil.ToInt64(rm["tokens_out"]); ok {
				rs.TokensOut = v
			}
			if v, ok := argutil.ToInt64(rm["exec_sum_us"]); ok {
				rs.ExecSumUs = v
			}
			if v, ok := rm["open_error"].(string); ok {
				rs.OpenError = v
			}
			r.ByRoot = append(r.ByRoot, rs)
		}
	}
	return r, true
}

// parseDuration extends time.ParseDuration to support day units ("7d", "1d").
func parseDuration(s string) (time.Duration, error) {
	if strings.HasSuffix(s, "d") {
		n, err := parseInt(s[:len(s)-1])
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("invalid day value %q", s)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	return time.ParseDuration(s)
}

func parseInt(s string) (int, error) {
	var n int
	_, err := fmt.Sscanf(s, "%d", &n)
	return n, err
}

func toFloat64(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	}
	return 0, false
}
