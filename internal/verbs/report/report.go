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
	"bytes"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/stazelabs/ash/internal/jail"
	"github.com/stazelabs/ash/internal/ledger"
	"github.com/stazelabs/ash/internal/proto"
	"github.com/stazelabs/ash/internal/registry"
	"github.com/stazelabs/ash/internal/session"
	"github.com/stazelabs/ash/internal/verbs/argutil"
	"github.com/stazelabs/ash/internal/verbs/help"
	"github.com/stazelabs/ash/internal/verbs/hook"
	"github.com/vmihailenco/msgpack/v5"
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
	Calls             int   `msgpack:"calls"`
	OK                int   `msgpack:"ok"`
	Errors            int   `msgpack:"errors"`
	TokensIn          int64 `msgpack:"tokens_in"`
	TokensOut         int64 `msgpack:"tokens_out"`
	TokensOutNoPrefix int64 `msgpack:"tokens_out_no_prefix"`
	// TokensOutEmit / BytesOutEmit are the MCP-envelope accounting from
	// ASH-123: what the harness actually consumed for rows where the
	// request arrived over `ashmcp`. Both are 0 for CLI-only sessions
	// and PrettyResponse hides them in that case.
	TokensOutEmit int64 `msgpack:"tokens_out_emit,omitempty"`
	BytesOutEmit  int64 `msgpack:"bytes_out_emit,omitempty"`
	// MCPCalls counts how many of Calls arrived over `ashmcp` (i.e.
	// rows with non-zero bytes_out_emit). When equal to Calls the
	// session is MCP-only; when 0 the emit columns stay hidden.
	MCPCalls int `msgpack:"mcp_calls,omitempty"`
	// ASH-108 cache-aware envelope. TokensCacheHit / TokensCacheMiss
	// aggregate per-call Anthropic prompt-cache accounting when the
	// harness reports it back. CacheCalls counts how many rows in the
	// window carried non-zero cache numbers — used to decide whether
	// PrettyResponse renders the cache line at all (hidden when 0 so
	// CLI-only sessions are byte-identical to today's output).
	TokensCacheHit  int64 `msgpack:"tokens_cache_hit,omitempty"`
	TokensCacheMiss int64 `msgpack:"tokens_cache_miss,omitempty"`
	CacheCalls      int   `msgpack:"cache_calls,omitempty"`
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
	Verb              string         `msgpack:"verb"`
	// Tier mirrors help.VerbSchema.Tier — "A" (inner-loop agent), "B"
	// (episodic agent), "C" (bootstrap), or "D" (instrumentation/meta).
	// Populated at aggregate time from the help registry; empty for
	// verb names not in the registry (e.g. ledger rows from a verb
	// that was renamed or removed). See docs/optimization-tiers.md.
	Tier              string         `msgpack:"tier,omitempty"`
	N                 int            `msgpack:"n"`
	OKCount           int            `msgpack:"ok_count"`
	OKPct             float64        `msgpack:"ok_pct"`
	P50ExecUs         int64          `msgpack:"p50_exec_us"`
	P95ExecUs         int64          `msgpack:"p95_exec_us"`
	P50TokensOut      int64          `msgpack:"p50_tokens_out"`
	P95TokensOut      int64          `msgpack:"p95_tokens_out"`
	TokensOutSum      int64          `msgpack:"tokens_out_sum,omitempty"`
	TokensOutNoPrefix int64          `msgpack:"tokens_out_no_prefix_sum,omitempty"`
	TruncatedN        int            `msgpack:"truncated_n"`
	TruncatedPct      float64        `msgpack:"truncated_pct"`
	SubPhases         []VerbSubPhase `msgpack:"sub_phases,omitempty"`
	TokPerKiB         float64        `msgpack:"tok_per_kib,omitempty"` // tokens_out / (bytes_out/1024)
}

// ErrEntry is one row in the error-code histogram.
type ErrEntry struct {
	Code      string `msgpack:"code"`
	Count     int    `msgpack:"count"`
	SampleMsg string `msgpack:"sample_msg"`
}

// HookDenialEntry is one row in the per-rule hook-denial histogram.
// Populated only when the report's call window includes `hook` verb
// rows. Computed by replaying hook.Decide() against each row's args
// at render time (no new ledger column), so deny patterns surface
// for the friction-prompt ritual (ASH-168) without changing the
// wire shape.
type HookDenialEntry struct {
	Rule             string `msgpack:"rule"`
	Count            int    `msgpack:"count"`
	TopSuggestedVerb string `msgpack:"top_suggested_verb,omitempty"`
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
	// HookDenials is the per-rule hook-denial histogram, populated
	// when the window includes hook rows. Computed at aggregate time
	// by replaying hook.Decide(); not persisted to the ledger.
	HookDenials []HookDenialEntry `msgpack:"hook_denials,omitempty"`
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
	if a.AllRoots, perr = argutil.OptionalBool(in, "all", false); perr != nil {
		return nil, perr
	}
	if a.Root != "" && a.AllRoots {
		return nil, &proto.Error{Code: "args", Msg: "--root and --all are mutually exclusive"}
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
	if len(r.HookDenials) > topN {
		r.HookDenials = r.HookDenials[:topN]
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
		return nil, &proto.Error{Code: "no_roots", Msg: "registry is empty", Hint: "run 'ash init' in each target repo first"}
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

// tierForVerb returns the optimization tier ("A"/"B"/"C"/"D") for a
// verb name, sourced from the canonical help registry. Returns "" for
// verbs not in the registry (e.g. ledger rows from a renamed/removed
// verb), which sort last and group under an "(other)" header in the
// pretty table. See docs/optimization-tiers.md.
func tierForVerb(name string) string {
	for _, vs := range help.Registry() {
		if vs.Verb == name {
			return vs.Tier
		}
	}
	return ""
}

// tierRank maps a tier letter to its sort key. Empty (unknown verbs)
// sorts after all known tiers so the main table starts with Tier A.
var tierRank = map[string]int{"A": 0, "B": 1, "C": 2, "D": 3, "": 4}

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
		totals.TokensOutNoPrefix += int64(c.TokensOutNoPrefix)
		totals.TokensOutEmit += int64(c.TokensOutEmit)
		totals.BytesOutEmit += int64(c.BytesOutEmit)
		if c.BytesOutEmit > 0 {
			totals.MCPCalls++
		}
		// ASH-108: prompt-cache telemetry per ledger row.
		totals.TokensCacheHit += int64(c.TokensCacheHit)
		totals.TokensCacheMiss += int64(c.TokensCacheMiss)
		if c.TokensCacheHit > 0 || c.TokensCacheMiss > 0 {
			totals.CacheCalls++
		}
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
		vs := VerbStats{Verb: verb, Tier: tierForVerb(verb), N: len(cs)}
		execUs := make([]int64, len(cs))
		tokOut := make([]int64, len(cs))
		var sumExec, sumWalk, sumIO, sumRegex, sumRegexCompile int64
		var sumTokOut, sumTokOutNoPrefix, sumBytesOut int64
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
			sumTokOutNoPrefix += int64(c.TokensOutNoPrefix)
			sumBytesOut += int64(c.BytesOut)
		}
		vs.TokensOutSum = sumTokOut
		vs.TokensOutNoPrefix = sumTokOutNoPrefix
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

	// Sort stats by (tier, -n) so the table reads Tier A → D and the
	// hottest verb in each tier is first. Stable so ties (same tier and
	// same n) preserve first-seen order from the ledger.
	sort.SliceStable(stats, func(i, j int) bool {
		ri, rj := tierRank[stats[i].Tier], tierRank[stats[j].Tier]
		if ri != rj {
			return ri < rj
		}
		return stats[i].N > stats[j].N
	})

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
		HookDenials:      computeHookDenials(byVerb["hook"]),
	}
}

// prettyArgValue strips active jail prefixes from s in two passes so
// arg-dist values both echo paths short and tolerate strings that embed
// paths mid-text (e.g. hook commands like "bin/ash diff --path /Users/.../ash/x").
// ASH-71.
func prettyArgValue(s string) string {
	prefixes := jail.PathPrefixes()
	// Pass 1: mid-string "<prefix>/" → "" (catches embedded paths).
	out := ledger.StripPrefixes(s, prefixes)
	// Pass 2: if the result is exactly a known prefix, render as ".".
	for _, p := range prefixes {
		if out == p {
			return "."
		}
	}
	return out
}

// collectArgDists builds per-verb arg frequency distributions from call ArgsMsgpack blobs.
// Keys are sorted alphabetically; values within each key are sorted by count desc.
// Callers cap values to topN via RunWithLedger.
//
// ASH-71: string values are run through prettyArgValue so the project-
// root prefix does not bloat the histogram, exact-root values render as
// ".", and embedded paths (e.g. inside hook commands) also get stripped.
// Counting happens on the stripped value, so identical underlying paths
// merge into one bucket.
func collectArgDists(byVerb map[string][]ledger.Call, order []string) []VerbArgDist {
	var result []VerbArgDist
	for _, verb := range order {
		cs := byVerb[verb]
		counts := map[string]map[string]int{} // [key][value]count
		var keyOrder []string
		for _, c := range cs {
			args := decodeArgsMap(c.ArgsMsgpack)
			if len(args) == 0 {
				continue
			}
			for k, v := range args {
				val := fmt.Sprintf("%v", v)
				if s, ok := v.(string); ok {
					val = prettyArgValue(s)
				}
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
	var r Result
	if err := proto.UnmarshalData(rsp, &r); err != nil {
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
	fmt.Fprintf(&b, "§report: %s \xe2\x80\x94 %d calls, %s exec\n",
		sessionLabel, r.Totals.Calls, fmtUs(r.Totals.ExecSumUs))

	// Totals line
	okPct := pct(int(r.Totals.OK), int(r.Totals.Calls))
	fmt.Fprintf(&b, "totals: ok=%d/%d (%.0f%%), tokens_in=%d, tokens_out=%d\n",
		r.Totals.OK, r.Totals.Calls, okPct, r.Totals.TokensIn, r.Totals.TokensOut)

	// ASH-71: path-prefix tax. Shown only when material — pre-migration
	// rows back-filled to 0 would otherwise report a misleading 100% tax.
	if r.Totals.TokensOutNoPrefix > 0 && r.Totals.TokensOutNoPrefix < r.Totals.TokensOut {
		tax := r.Totals.TokensOut - r.Totals.TokensOutNoPrefix
		taxPct := float64(tax) / float64(r.Totals.TokensOut) * 100
		fmt.Fprintf(&b, "path-prefix tax: %d tokens (%.0f%% of out)\n", tax, taxPct)
	}

	// ASH-123: MCP-transport emit accounting. tokens_out above counts
	// the daemon-pretty-rendered text — what a CLI client sees. For
	// rows where the request arrived via `ashmcp`, the harness instead
	// consumes a JSON envelope; tokens_out_emit is the cost of that.
	// Shown only when at least one call had MCP transport, so CLI-only
	// sessions are unchanged.
	if r.Totals.MCPCalls > 0 && r.Totals.TokensOutEmit > 0 {
		fmt.Fprintf(&b, "mcp emit: %d calls, tokens_out_emit=%d, bytes_out_emit=%d\n",
			r.Totals.MCPCalls, r.Totals.TokensOutEmit, r.Totals.BytesOutEmit)
	}

	// ASH-108: Anthropic prompt-cache accounting (harness-reported).
	// Shown only when at least one call carried non-zero cache numbers,
	// so windows without telemetry are byte-identical to today's output.
	// The cache-hit ratio is the headline number: cached input tokens
	// charge ~10x less than uncached, so a high ratio is the win.
	if r.Totals.CacheCalls > 0 && (r.Totals.TokensCacheHit > 0 || r.Totals.TokensCacheMiss > 0) {
		total := r.Totals.TokensCacheHit + r.Totals.TokensCacheMiss
		hitPct := float64(0)
		if total > 0 {
			hitPct = float64(r.Totals.TokensCacheHit) / float64(total) * 100
		}
		fmt.Fprintf(&b, "cache: %d calls, hit=%d miss=%d (%.0f%% hit)\n",
			r.Totals.CacheCalls, r.Totals.TokensCacheHit, r.Totals.TokensCacheMiss, hitPct)
	}

	if len(r.ByVerb) == 0 {
		b.WriteString("(no calls)\n")
		return strings.TrimRight(b.String(), "\n")
	}

	// Per-verb table — sorted by (tier, -n) in aggregate(). Tier column
	// surfaces the optimization-tier classification from the help
	// registry so a glance answers "is this Tier A inner-loop work or
	// Tier D meta?" (ASH-131, see docs/optimization-tiers.md).
	fmt.Fprintf(&b, "\n%-10s  %4s  %4s  %4s  %9s  %9s  %7s  %7s  %6s\n",
		"verb", "tier", "n", "ok%", "p50_exec", "p95_exec", "p50_out", "p95_out", "trunc%")
	fmt.Fprintf(&b, "%s\n", strings.Repeat("-", 78))
	for _, vs := range r.ByVerb {
		tierCell := vs.Tier
		if tierCell == "" {
			tierCell = "-"
		}
		fmt.Fprintf(&b, "%-10s  %4s  %4d  %3.0f%%  %9s  %9s  %7d  %7d  %5.0f%%\n",
			vs.Verb, tierCell, vs.N, vs.OKPct,
			fmtUs(vs.P50ExecUs), fmtUs(vs.P95ExecUs),
			vs.P50TokensOut, vs.P95TokensOut,
			vs.TruncatedPct)
	}

	// Tier subtotals — one line per tier that has at least one verb in
	// the window. Lets ASH-131 readers spot at a glance whether Tier D
	// is bloating the budget or Tier A is dominating as expected.
	tierN := map[string]int{}
	tierTok := map[string]int64{}
	tierOrder := []string{}
	for _, vs := range r.ByVerb {
		t := vs.Tier
		if t == "" {
			t = "-"
		}
		if _, seen := tierN[t]; !seen {
			tierOrder = append(tierOrder, t)
		}
		tierN[t] += vs.N
		tierTok[t] += vs.TokensOutSum
	}
	if len(tierOrder) > 0 {
		b.WriteString("\nby tier (n / tokens_out):\n")
		for _, t := range tierOrder {
			fmt.Fprintf(&b, "  %s: n=%d, tokens_out=%d\n", t, tierN[t], tierTok[t])
		}
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

	// Hook denials by rule — ASH-162. Replays hook.Decide() against
	// each hook-call row in the window so the friction-prompt ritual
	// (ASH-168) can read deny patterns at a glance without scanning
	// raw `command` args.
	if len(r.HookDenials) > 0 {
		total := 0
		for _, h := range r.HookDenials {
			total += h.Count
		}
		fmt.Fprintf(&b, "\nhook denials by rule (%d):\n", total)
		for _, h := range r.HookDenials {
			if h.TopSuggestedVerb != "" {
				fmt.Fprintf(&b, "  %s \xc3\x97 %d  \xe2\x86\x92 ash %s\n", h.Rule, h.Count, h.TopSuggestedVerb)
			} else {
				fmt.Fprintf(&b, "  %s \xc3\x97 %d\n", h.Rule, h.Count)
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

// decodeArgsMap decodes a plain msgpack args map from the ledger.
// Returns nil when the blob is empty or undecodable.
func decodeArgsMap(blob []byte) map[string]any {
	if len(blob) == 0 {
		return nil
	}
	dec := msgpack.NewDecoder(bytes.NewReader(blob))
	dec.UseLooseInterfaceDecoding(true)
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil
	}
	return m
}

// decodeArgsSummary decodes an args blob and returns a compact key=value
// summary (e.g. "glob=**/*.go path=. limit=256"). Returns "" on any error
// or if no args are present.
//
// ASH-71: string values are run through jail.PrettyPath so the
// truncation-hotspot line does not repeat the project-root prefix on
// every entry.
func decodeArgsSummary(blob []byte) string {
	args := decodeArgsMap(blob)
	if len(args) == 0 {
		return ""
	}
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		v := fmt.Sprintf("%v", args[k])
		if s, ok := args[k].(string); ok {
			v = prettyArgValue(s)
		}
		if v != "" {
			parts = append(parts, k+"="+v)
		}
	}
	return strings.Join(parts, " ")
}

// computeHookDenials replays hook.Decide() against each hook-call row in
// cs and returns a per-rule histogram of denies, sorted by count desc
// (ties broken alphabetically by rule). Returns nil when cs has no rows
// that recompute to a deny.
//
// Why recompute: MatchedRule and Suggested live in the hook verb's
// Result, which the ledger does not persist. Args are persisted, and
// hook.Decide is deterministic over them; the client only writes
// exclude_verbs into the wire args when an exclusion actually fired
// (cmd/ash/hook.go), so recompute mirrors the original decision for
// every row.
func computeHookDenials(cs []ledger.Call) []HookDenialEntry {
	if len(cs) == 0 {
		return nil
	}
	type bucket struct {
		count     int
		verbCount map[string]int
	}
	byRule := map[string]*bucket{}
	for _, c := range cs {
		args := decodeArgsMap(c.ArgsMsgpack)
		if len(args) == 0 {
			continue
		}
		ha, perr := hook.ParseArgs(args)
		if perr != nil {
			continue
		}
		r := hook.Decide(ha)
		if r.Decision != "deny" {
			continue
		}
		b, ok := byRule[r.MatchedRule]
		if !ok {
			b = &bucket{verbCount: map[string]int{}}
			byRule[r.MatchedRule] = b
		}
		b.count++
		if v := suggestedAshVerb(r.Suggested); v != "" {
			b.verbCount[v]++
		}
	}
	if len(byRule) == 0 {
		return nil
	}
	entries := make([]HookDenialEntry, 0, len(byRule))
	for rule, b := range byRule {
		verbs := make([]string, 0, len(b.verbCount))
		for v := range b.verbCount {
			verbs = append(verbs, v)
		}
		// Alphabetical tie-break for determinism when two verbs share
		// the top count.
		sort.Strings(verbs)
		top := ""
		topN := 0
		for _, v := range verbs {
			if b.verbCount[v] > topN {
				top = v
				topN = b.verbCount[v]
			}
		}
		entries = append(entries, HookDenialEntry{
			Rule: rule, Count: b.count, TopSuggestedVerb: top,
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Count != entries[j].Count {
			return entries[i].Count > entries[j].Count
		}
		return entries[i].Rule < entries[j].Rule
	})
	return entries
}

// suggestedAshVerb extracts the verb name from a hook.Suggested string
// like "ash grep --path X --pattern Y" → "grep". Returns "" when the
// suggestion does not contain an "ash <verb>" pair.
func suggestedAshVerb(suggested string) string {
	parts := strings.Fields(suggested)
	for i, p := range parts {
		if p == "ash" && i+1 < len(parts) {
			return parts[i+1]
		}
	}
	return ""
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

func CompactResponse(rsp *proto.Response) (any, error) {
	if !rsp.OK {
		return nil, nil
	}
	// Decode twice: once typed (for by_verb rows), once as map[string]any via
	// msgpack round-trip so metadata fields use msgpack tag names (lowercase),
	// not Go field names.
	var r Result
	if err := proto.UnmarshalData(rsp, &r); err != nil {
		return nil, err
	}
	dec := msgpack.NewDecoder(bytes.NewReader([]byte(rsp.Data)))
	dec.UseLooseInterfaceDecoding(true)
	var raw map[string]any
	if err := dec.Decode(&raw); err != nil {
		return nil, err
	}
	cd := proto.CompactData{
		K: []string{"verb", "n", "ok_n", "ok_pct", "p50", "p95", "p50t", "p95t", "trunc_n", "trunc_pct"},
		R: make([][]any, len(r.ByVerb)),
	}
	for i, vs := range r.ByVerb {
		cd.R[i] = []any{
			vs.Verb, vs.N, vs.OKCount, vs.OKPct,
			vs.P50ExecUs, vs.P95ExecUs,
			vs.P50TokensOut, vs.P95TokensOut,
			vs.TruncatedN, vs.TruncatedPct,
		}
	}
	return map[string]any{
		"scope":          raw["scope"],
		"totals":         raw["totals"],
		"by_verb":        cd,
		"err_histogram":  raw["err_histogram"],
		"trunc_hotspots": raw["trunc_hotspots"],
		"hook_denials":   raw["hook_denials"],
	}, nil
}
