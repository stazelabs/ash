// Package report implements the `report` verb.
//
// Args:
//
//	session  string  (optional) - "current" (default), "all", or an explicit session ID
//	since    string  (optional) - duration window, e.g. "15m", "1h", "24h", "7d"
//	last     int     (optional) - row cap after session/since filters, max 5000
//	verb     string  (optional) - restrict to one verb
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

type Args struct {
	Session string // "current", "all", or explicit session ID
	Since   time.Duration
	Last    int // 0 = no cap beyond DefaultWindowLimit
	Verb    string
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
}

type Result struct {
	Scope  Scope       `msgpack:"scope"`
	Totals Totals      `msgpack:"totals"`
	ByVerb []VerbStats `msgpack:"by_verb"`
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

	return aggregate(calls, scope), nil
}

func aggregate(calls []ledger.Call, scope Scope) *Result {
	totals := Totals{Calls: len(calls)}
	byVerb := map[string][]ledger.Call{}
	order := []string{}

	for _, c := range calls {
		totals.TokensIn += int64(c.TokensIn)
		totals.TokensOut += int64(c.TokensOut)
		totals.ExecSumUs += c.LatencyExecUs
		if c.OK {
			totals.OK++
		} else {
			totals.Errors++
		}
		if _, seen := byVerb[c.Verb]; !seen {
			order = append(order, c.Verb)
		}
		byVerb[c.Verb] = append(byVerb[c.Verb], c)
	}

	stats := make([]VerbStats, 0, len(order))
	for _, verb := range order {
		cs := byVerb[verb]
		vs := VerbStats{Verb: verb, N: len(cs)}
		execUs := make([]int64, len(cs))
		tokOut := make([]int64, len(cs))
		var sumExec, sumWalk, sumIO, sumRegex int64
		for i, c := range cs {
			if c.OK {
				vs.OKCount++
			}
			if c.Truncated {
				vs.TruncatedN++
			}
			execUs[i] = c.LatencyExecUs
			tokOut[i] = int64(c.TokensOut)
			sumExec += c.LatencyExecUs
			sumWalk += c.WalkUs
			sumIO += c.IOUs
			sumRegex += c.RegexUs
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

		stats = append(stats, vs)
	}

	return &Result{Scope: scope, Totals: totals, ByVerb: stats}
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
