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
	"fmt"
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
}

type Args struct {
	Verb  string
	Case  string
	Limit int
}

// CaseResult is one row of the bench output.
type CaseResult struct {
	Name           string `msgpack:"name"`
	Verb           string `msgpack:"verb"`
	Why            string `msgpack:"why,omitempty"`
	AshTokens      int    `msgpack:"ash_tokens"`
	BashTokens     int    `msgpack:"bash_tokens"`
	AshLatencyUs   int64  `msgpack:"ash_latency_us"`
	BashLatencyUs  int64  `msgpack:"bash_latency_us"`
	AshBytes       int    `msgpack:"ash_bytes"`
	BashBytes      int    `msgpack:"bash_bytes"`
	AshOK          bool   `msgpack:"ash_ok"`
	AshErr         string `msgpack:"ash_err,omitempty"`
	BashCmd        string `msgpack:"bash_cmd"`
	BashExit       int    `msgpack:"bash_exit"`
	BashRunErr     string `msgpack:"bash_run_err,omitempty"`
	BashTruncated  bool   `msgpack:"bash_truncated,omitempty"`
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
	return a, nil
}

// RunWithDeps executes the bench. It selects cases per Args, runs ash
// + bash for each, tokenizes both sides via Deps.Counter, and returns
// a Result.
func RunWithDeps(d Deps, a *Args) (*Result, *proto.Error) {
	if d.Counter == nil || d.Run == nil || d.Pretty == nil {
		return nil, &proto.Error{Code: "config", Msg: "bench: deps not wired"}
	}

	cases := selectCases(a)
	res := &Result{
		Cases:     make([]CaseResult, 0, len(cases)),
		NotRunWhy: map[string]string{},
	}

	for _, c := range cases {
		row, skipped, why := runCase(d, c)
		if skipped {
			res.NotRun = append(res.NotRun, c.Name)
			res.NotRunWhy[c.Name] = why
			continue
		}
		res.Cases = append(res.Cases, row)
	}

	res.ByVerb = aggregateByVerb(res.Cases)
	res.Overall = aggregateOverall(res.Cases)
	return res, nil
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
func runCase(d Deps, c bench.Case) (CaseResult, bool, string) {
	row := CaseResult{Name: c.Name, Verb: c.Verb, Why: c.Why}

	// 1. Ash side: dispatch the verb in-process and tokenize the same
	// canonical pretty form the daemon would have produced.
	req := &proto.Request{V: proto.ProtocolVersion, Verb: c.Verb, Args: c.AshArgs}
	ashStart := time.Now()
	data, perr := d.Run(c.Verb, c.AshArgs)
	ashDur := time.Since(ashStart)

	rsp := &proto.Response{V: proto.ProtocolVersion, ID: req.ID}
	if perr != nil {
		rsp.OK = false
		rsp.Err = perr
		row.AshOK = false
		row.AshErr = perr.Code
	} else {
		rsp.OK = true
		rsp.Data = proto.MustData(data)
		row.AshOK = true
	}
	prettyAsh := d.Pretty(c.Verb, req, rsp)
	row.AshTokens = d.Counter.Count(prettyAsh)
	row.AshBytes = len(prettyAsh)
	row.AshLatencyUs = ashDur.Microseconds()

	// 2. Bash side: translate, run sandboxed, tokenize stdout.
	argv, terr := bench.BashFor(c)
	if terr != nil {
		return row, true, terr.Error()
	}
	row.BashCmd = strings.Join(argv, " ")

	ctx, cancel := context.WithTimeout(context.Background(), bench.DefaultBashTimeout)
	defer cancel()
	br := bench.RunBash(ctx, argv)
	row.BashTokens = d.Counter.Count(string(br.Stdout))
	row.BashBytes = len(br.Stdout)
	row.BashLatencyUs = br.Latency.Microseconds()
	row.BashExit = br.ExitCode
	row.BashRunErr = br.RunErr
	row.BashTruncated = br.Truncate

	return row, false, ""
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

// PrettyResponse renders the bench result in a side-by-side table.
func PrettyResponse(req *proto.Request, rsp *proto.Response) string {
	if !rsp.OK {
		return proto.PrettyResponseHeader(rsp)
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

