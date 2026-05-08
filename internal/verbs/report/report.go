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
	"sort"
	"strings"
	"time"

	"github.com/stazelabs/ash/internal/ledger"
	"github.com/stazelabs/ash/internal/proto"
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
}

// Scope describes the filters that were applied, for display and JSON.
type Scope struct {
	Session string `msgpack:"session"`
	Since   string `msgpack:"since,omitempty"`
	Last    int    `msgpack:"last,omitempty"`
	Verb    string `msgpack:"verb,omitempty"`
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
	if a.Last, perr = argutil.OptionalPosInt(in, "last", 0, MaxLast); perr != nil {
		// last is optional with no implicit default — but OptionalPosInt
		// rejects 0 when the value is set. The default-0 path produces 0
		// from "absent" cleanly, so a 0 from the helper means "absent."
		return nil, perr
	}
	if a.Verb, perr = argutil.OptionalString(in, "verb", ""); perr != nil {
		return nil, perr
	}
	if a.TopN, perr = argutil.OptionalPosInt(in, "top", defaultTopN, MaxTopN); perr != nil {
		return nil, perr
	}
	return a, nil
}

func RunWithLedger(led *ledger.Ledger, a *Args) (*Result, *proto.Error) {
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

	// Cap hotspot sections to TopN entries (already sorted by count desc).
	if a.TopN > 0 {
		if len(result.TruncHotspots) > a.TopN {
			result.TruncHotspots = result.TruncHotspots[:a.TopN]
		}
		if len(result.ErrHistogram) > a.TopN {
			result.ErrHistogram = result.ErrHistogram[:a.TopN]
		}
		// Cap arg distribution values per key.
		for i := range result.ArgDistributions {
			for j := range result.ArgDistributions[i].Args {
				if len(result.ArgDistributions[i].Args[j].Values) > a.TopN {
					result.ArgDistributions[i].Args[j].Values = result.ArgDistributions[i].Args[j].Values[:a.TopN]
				}
			}
		}
	}
	return result, nil
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
		var sumExec, sumWalk, sumIO, sumRegex int64
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
		if sumExec > 0 && (sumWalk > 0 || sumIO > 0 || sumRegex > 0) {
			walkExcl := sumWalk - sumIO - sumRegex
			if walkExcl < 0 {
				walkExcl = 0
			}
			walkPct := pctOf(walkExcl, sumExec)
			ioPct := pctOf(sumIO, sumExec)
			regexPct := pctOf(sumRegex, sumExec)
			otherPct := 100.0 - walkPct - ioPct - regexPct
			if otherPct < 0 {
				otherPct = 0
			}
			vs.SubPhases = []VerbSubPhase{
				{Name: "walk", Pct: walkPct},
				{Name: "io", Pct: ioPct},
				{Name: "regex", Pct: regexPct},
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
	if r.Scope.Since != "" {
		sessionLabel += ", since=" + r.Scope.Since
	}
	if r.Scope.Verb != "" {
		sessionLabel += ", verb=" + r.Scope.Verb
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
