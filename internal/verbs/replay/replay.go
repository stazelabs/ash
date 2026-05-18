// Package replay implements the `ash replay` verb. It re-runs prior
// ledger calls against the current build and reports per-verb token
// deltas vs the originals. The intent is to turn the ledger itself
// into a regression test suite: token-shape changes, envelope tweaks,
// and pretty-form edits get an empirical scoreboard without writing
// synthetic benchmarks for each.
//
// Args:
//
//	session         string  "current" (default), "all", or an explicit session ID
//	since           string  duration window (e.g. 1h, 7d) — additional time filter
//	verb            string  restrict to one verb
//	limit           int     cap on calls replayed; 0 = no cap (hard ceiling 5000)
//	regress_tokens  int     Δtokens% threshold for tagging regressions (default 10)
//	top             int     max rows in top-regressors section (default 10)
//	cache_prefix    bool    when true, also compute the ASH-108 envelope-reorder A/B
//	                        (see cache_prefix.go) — average matching byte-prefix
//	                        between consecutive same-verb stable-data pairs, encoded
//	                        once with today's envelope and once with a legacy
//	                        struct mirroring the pre-ASH-108 ordering.
//
// Mutating and heavyweight verbs are skipped unconditionally (write,
// edit, init, uninit, stop, test, bench, replay). Calls whose long
// string args were sanitized to "<truncated:N>" sentinels in the
// ledger are also skipped — replaying with a placeholder would not be
// honest. Each skip is counted by reason in the result.
//
// Limitations (deliberate, this is the first cut):
//   - No file-state snapshot. Replay runs against the current working
//     tree; results diverge if files have changed since the recorded
//     call. Git-ref pinning is a planned follow-up.
//   - The argsBlob sanitizer in ashd caps string args at 1024 bytes,
//     so write/edit/diff calls with large content would not replay
//     faithfully even if mutating verbs were not skipped. That trade
//     is deliberate — keeping argsMsgpack bounded matters more than
//     perfect replay fidelity for content-heavy verbs.
package replay

import (
	"bytes"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/stazelabs/ash/internal/ledger"
	"github.com/stazelabs/ash/internal/proto"
	"github.com/stazelabs/ash/internal/verbs/argutil"
	"github.com/vmihailenco/msgpack/v5"
)

const (
	DefaultRegressTokPct = 10
	DefaultTop           = 10
	MaxLimit             = 5000
	MaxTop               = 200
)

var mutatingVerbs = map[string]bool{
	"write":  true,
	"edit":   true,
	"init":   true,
	"uninit": true,
	"stop":   true,
}

var heavyVerbs = map[string]bool{
	"test":  true,
	"bench": true,
}

var recursiveVerbs = map[string]bool{
	"replay": true,
}

type Args struct {
	Session       string
	Since         time.Duration
	Verb          string
	Limit         int
	RegressTokPct int
	Top           int
	CachePrefix   bool
}

type Scope struct {
	Session       string `msgpack:"session" json:"session"`
	Since         string `msgpack:"since,omitempty" json:"since,omitempty"`
	Verb          string `msgpack:"verb,omitempty" json:"verb,omitempty"`
	Limit         int    `msgpack:"limit,omitempty" json:"limit,omitempty"`
	RegressTokPct int    `msgpack:"regress_tokens" json:"regress_tokens"`
	CachePrefix   bool   `msgpack:"cache_prefix,omitempty" json:"cache_prefix,omitempty"`
}

// CallReplay is one row of the comparison: the original ledger call
// vs the same call dispatched against the current build.
type CallReplay struct {
	Verb           string  `msgpack:"verb" json:"verb"`
	Args           string  `msgpack:"args" json:"args"`
	OriginalOK     bool    `msgpack:"orig_ok" json:"orig_ok"`
	ReplayOK       bool    `msgpack:"new_ok" json:"new_ok"`
	OriginalErr    string  `msgpack:"orig_err,omitempty" json:"orig_err,omitempty"`
	ReplayErr      string  `msgpack:"new_err,omitempty" json:"new_err,omitempty"`
	OriginalTokens int     `msgpack:"orig_tok" json:"orig_tok"`
	ReplayTokens   int     `msgpack:"new_tok" json:"new_tok"`
	DeltaTokens    int     `msgpack:"d_tok" json:"d_tok"`
	DeltaPct       float64 `msgpack:"d_pct,omitempty" json:"d_pct,omitempty"`
	Regress        bool    `msgpack:"regress,omitempty" json:"regress,omitempty"`
}

// VerbSummary aggregates CallReplay rows by verb. DeltaTotal is the
// signed sum of per-call deltas — negative means the new build is
// cheaper in tokens (the win); positive means a regression.
type VerbSummary struct {
	Verb          string `msgpack:"verb" json:"verb"`
	N             int    `msgpack:"n" json:"n"`
	OriginalTotal int    `msgpack:"orig_tok_sum" json:"orig_tok_sum"`
	ReplayTotal   int    `msgpack:"new_tok_sum" json:"new_tok_sum"`
	DeltaTotal    int    `msgpack:"d_tok_sum" json:"d_tok_sum"`
	Regressions   int    `msgpack:"regressions" json:"regressions"`
	OKMismatch    int    `msgpack:"ok_mismatch" json:"ok_mismatch"`
}

type Result struct {
	Scope         Scope          `msgpack:"scope" json:"scope"`
	Replayed      int            `msgpack:"replayed" json:"replayed"`
	Skipped       int            `msgpack:"skipped" json:"skipped"`
	SkipByReason  map[string]int `msgpack:"skip_by_reason,omitempty" json:"skip_by_reason,omitempty"`
	ByVerb        []VerbSummary      `msgpack:"by_verb" json:"by_verb"`
	Overall       VerbSummary        `msgpack:"overall" json:"overall"`
	TopRegressors []CallReplay       `msgpack:"top_regressors,omitempty" json:"top_regressors,omitempty"`
	OKMismatches  []CallReplay       `msgpack:"ok_mismatches,omitempty" json:"ok_mismatches,omitempty"`
	CachePrefix   *CachePrefixResult `msgpack:"cache_prefix,omitempty" json:"cache_prefix,omitempty"`
}

// Deps mirrors the bench Deps pattern. The daemon supplies closures
// over the live runner registry, pretty handlers, counter, and ledger
// so replay can dispatch in-process and tokenize new responses with
// exactly the same code path as a fresh CLI call.
type Deps struct {
	Counter *ledger.Counter
	Ledger  *ledger.Ledger
	Run     func(verb string, args map[string]any) (any, *proto.Error)
	Pretty  func(verb string, req *proto.Request, rsp *proto.Response) string
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
		d, err := argutil.ParseDuration(since)
		if err != nil {
			return nil, &proto.Error{Code: "args", Msg: "since: " + err.Error()}
		}
		a.Since = d
	}
	if a.Verb, perr = argutil.OptionalString(in, "verb", ""); perr != nil {
		return nil, perr
	}
	if a.Limit, perr = argutil.OptionalNonNegInt(in, "limit", 0, MaxLimit); perr != nil {
		return nil, perr
	}
	if a.RegressTokPct, perr = argutil.OptionalNonNegInt(in, "regress_tokens", DefaultRegressTokPct, 1000); perr != nil {
		return nil, perr
	}
	if a.Top, perr = argutil.OptionalNonNegInt(in, "top", DefaultTop, MaxTop); perr != nil {
		return nil, perr
	}
	if a.Top == 0 {
		a.Top = DefaultTop
	}
	if a.CachePrefix, perr = argutil.OptionalBool(in, "cache_prefix", false); perr != nil {
		return nil, perr
	}
	return a, nil
}

// RunWithDeps is the daemon entry point. The runner registered in
// internal/verbs/verbs.go closes over the live registry and pretty
// map to populate Deps.
func RunWithDeps(d Deps, a *Args) (*Result, *proto.Error) {
	if d.Counter == nil || d.Ledger == nil || d.Run == nil || d.Pretty == nil {
		return nil, &proto.Error{Code: "config", Msg: "replay: deps not wired"}
	}
	opts := ledger.QueryOpts{
		SessionID:  a.Session,
		VerbFilter: a.Verb,
		Limit:      a.Limit,
	}
	// "all" is the verb-level wildcard; the ledger layer treats empty
	// SessionID as no filter.
	if opts.SessionID == "all" {
		opts.SessionID = ""
	}
	if a.Since > 0 {
		opts.Since = time.Now().Add(-a.Since)
	}
	calls, err := d.Ledger.QueryWindow(opts)
	if err != nil {
		return nil, &proto.Error{Code: "ledger", Msg: err.Error()}
	}

	scope := Scope{
		Session:       a.Session,
		Verb:          a.Verb,
		Limit:         a.Limit,
		RegressTokPct: a.RegressTokPct,
		CachePrefix:   a.CachePrefix,
	}
	if a.Since > 0 {
		scope.Since = a.Since.String()
	}

	res := &Result{
		Scope:        scope,
		SkipByReason: map[string]int{},
	}
	byVerb := map[string]*VerbSummary{}
	var rows []CallReplay
	// QueryWindow returns rows in reverse chronological order (newest
	// first). Cache-prefix analysis wants the chronological order the
	// agent's conversation transcript would have seen, so collect
	// per-call responses and reverse at the end before handing them to
	// computeCachePrefix.
	var cacheResps []verbResp

	for _, c := range calls {
		if reason := preDispatchSkip(c); reason != "" {
			res.Skipped++
			res.SkipByReason[reason]++
			continue
		}
		vargs := decodeArgsMap(c.ArgsMsgpack)
		if vargs == nil && needsArgs(c.Verb) {
			res.Skipped++
			if len(c.ArgsMsgpack) == 0 {
				res.SkipByReason["no_args"]++
			} else {
				res.SkipByReason["decode_failed"]++
			}
			continue
		}
		if hasTruncatedArg(vargs) {
			res.Skipped++
			res.SkipByReason["args_truncated"]++
			continue
		}
		row, rsp, skip := replayOne(d, c, vargs, a.RegressTokPct)
		if skip != "" {
			res.Skipped++
			res.SkipByReason[skip]++
			continue
		}
		rows = append(rows, row)
		res.Replayed++
		if a.CachePrefix && rsp != nil && rsp.OK {
			cacheResps = append(cacheResps, verbResp{Verb: c.Verb, Rsp: rsp})
		}
		s, ok := byVerb[c.Verb]
		if !ok {
			s = &VerbSummary{Verb: c.Verb}
			byVerb[c.Verb] = s
		}
		s.N++
		s.OriginalTotal += row.OriginalTokens
		s.ReplayTotal += row.ReplayTokens
		s.DeltaTotal += row.DeltaTokens
		if row.Regress {
			s.Regressions++
		}
		if row.OriginalOK != row.ReplayOK {
			s.OKMismatch++
		}
	}

	verbOrder := make([]string, 0, len(byVerb))
	for v := range byVerb {
		verbOrder = append(verbOrder, v)
	}
	sort.Strings(verbOrder)
	for _, v := range verbOrder {
		res.ByVerb = append(res.ByVerb, *byVerb[v])
	}
	res.Overall = aggregateOverall(rows)
	res.TopRegressors = topRegressors(rows, a.Top)
	res.OKMismatches = okMismatches(rows)
	if a.CachePrefix && len(cacheResps) >= 2 {
		// QueryWindow returned DESC; reverse to chronological.
		for i, j := 0, len(cacheResps)-1; i < j; i, j = i+1, j-1 {
			cacheResps[i], cacheResps[j] = cacheResps[j], cacheResps[i]
		}
		res.CachePrefix = computeCachePrefix(cacheResps)
	}
	return res, nil
}

func replayOne(d Deps, c ledger.Call, vargs map[string]any, regressPct int) (CallReplay, *proto.Response, string) {
	row := CallReplay{
		Verb:           c.Verb,
		Args:           argsSummary(vargs),
		OriginalOK:     c.OK,
		OriginalErr:    c.ErrCode,
		OriginalTokens: c.TokensOut,
	}
	data, perr := d.Run(c.Verb, vargs)
	rsp := &proto.Response{V: proto.ProtocolVersion}
	if perr != nil {
		// unknown_verb means the recorded verb is no longer registered.
		// Surface that as a skip rather than a row — there's no honest
		// comparison to make.
		if perr.Code == "unknown_verb" {
			return CallReplay{}, nil, "unknown_verb"
		}
		rsp.OK = false
		rsp.Err = perr
		row.ReplayOK = false
		row.ReplayErr = perr.Code
	} else {
		rsp.OK = true
		rsp.Data = proto.MustData(data)
		row.ReplayOK = true
	}
	req := &proto.Request{V: proto.ProtocolVersion, Verb: c.Verb, Args: vargs}
	pretty := d.Pretty(c.Verb, req, rsp)
	row.ReplayTokens = d.Counter.Count(pretty)
	row.DeltaTokens = row.ReplayTokens - row.OriginalTokens
	if row.OriginalTokens > 0 {
		row.DeltaPct = float64(row.DeltaTokens) / float64(row.OriginalTokens) * 100
		if absInt(row.DeltaTokens) > 0 && absFloat(row.DeltaPct) > float64(regressPct) {
			row.Regress = true
		}
	} else if row.DeltaTokens > 0 {
		// Original was empty; any new output is a regression by definition.
		row.Regress = true
	}
	return row, rsp, ""
}

func preDispatchSkip(c ledger.Call) string {
	switch {
	case mutatingVerbs[c.Verb]:
		return "mutating"
	case heavyVerbs[c.Verb]:
		return "heavy"
	case recursiveVerbs[c.Verb]:
		return "recursive"
	}
	return ""
}

// needsArgs returns true for verbs that won't dispatch sensibly with
// zero args. help and stop legitimately take no args; report and
// metrics tolerate empty args via defaults.
func needsArgs(verb string) bool {
	switch verb {
	case "help", "stop", "report", "metrics":
		return false
	}
	return true
}

func hasTruncatedArg(vargs map[string]any) bool {
	for _, v := range vargs {
		if s, ok := v.(string); ok {
			if strings.HasPrefix(s, "<truncated:") && strings.HasSuffix(s, ">") {
				return true
			}
		}
	}
	return false
}

func argsSummary(vargs map[string]any) string {
	if len(vargs) == 0 {
		return ""
	}
	keys := make([]string, 0, len(vargs))
	for k := range vargs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		v := fmt.Sprintf("%v", vargs[k])
		if len(v) > 32 {
			v = v[:29] + "..."
		}
		parts = append(parts, k+"="+v)
	}
	return strings.Join(parts, " ")
}

func aggregateOverall(rows []CallReplay) VerbSummary {
	o := VerbSummary{Verb: "overall"}
	for _, r := range rows {
		o.N++
		o.OriginalTotal += r.OriginalTokens
		o.ReplayTotal += r.ReplayTokens
		o.DeltaTotal += r.DeltaTokens
		if r.Regress {
			o.Regressions++
		}
		if r.OriginalOK != r.ReplayOK {
			o.OKMismatch++
		}
	}
	return o
}

func topRegressors(rows []CallReplay, top int) []CallReplay {
	out := make([]CallReplay, 0, len(rows))
	for _, r := range rows {
		if r.Regress {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return absInt(out[i].DeltaTokens) > absInt(out[j].DeltaTokens)
	})
	if top > 0 && len(out) > top {
		out = out[:top]
	}
	return out
}

func okMismatches(rows []CallReplay) []CallReplay {
	var out []CallReplay
	for _, r := range rows {
		if r.OriginalOK != r.ReplayOK {
			out = append(out, r)
		}
	}
	return out
}

func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func absFloat(n float64) float64 {
	if n < 0 {
		return -n
	}
	return n
}

// decodeArgsMap mirrors the same loose-interface decode that
// internal/verbs/report uses on the sanitized ledger blob. Returns
// nil when the blob is empty or undecodable; callers distinguish the
// two via len(blob) before calling.
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

func PrettyResponse(req *proto.Request, rsp *proto.Response) string {
	if !rsp.OK {
		return proto.PrettyResponseHeader(rsp)
	}
	var r Result
	if err := proto.UnmarshalData(rsp, &r); err != nil {
		return "ok\n<unrecognized replay result>"
	}
	var b strings.Builder

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
	fmt.Fprintf(&b, "\xc2\xa7replay: %s \xe2\x80\x94 %d replayed, %d skipped\n",
		sessionLabel, r.Replayed, r.Skipped)

	dpct := float64(0)
	if r.Overall.OriginalTotal > 0 {
		dpct = float64(r.Overall.DeltaTotal) / float64(r.Overall.OriginalTotal) * 100
	}
	fmt.Fprintf(&b, "totals: orig_tok=%d new_tok=%d \xce\x94tok=%+d (%+.1f%%)  ok_mismatches=%d  regressions=%d\n",
		r.Overall.OriginalTotal, r.Overall.ReplayTotal, r.Overall.DeltaTotal,
		dpct, r.Overall.OKMismatch, r.Overall.Regressions)

	if r.Skipped > 0 && len(r.SkipByReason) > 0 {
		keys := make([]string, 0, len(r.SkipByReason))
		for k := range r.SkipByReason {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var parts []string
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("%s=%d", k, r.SkipByReason[k]))
		}
		fmt.Fprintf(&b, "skipped: %s\n", strings.Join(parts, ", "))
	}

	if r.Replayed == 0 {
		return strings.TrimRight(b.String(), "\n")
	}

	b.WriteByte('\n')
	fmt.Fprintf(&b, "%-10s  %4s  %9s  %9s  %8s  %7s  %5s\n",
		"verb", "n", "orig_tok", "new_tok", "\xce\x94tok", "\xce\x94tok%", "regr")
	fmt.Fprintf(&b, "%s\n", strings.Repeat("-", 60))
	for _, vs := range r.ByVerb {
		dp := float64(0)
		if vs.OriginalTotal > 0 {
			dp = float64(vs.DeltaTotal) / float64(vs.OriginalTotal) * 100
		}
		fmt.Fprintf(&b, "%-10s  %4d  %9d  %9d  %+8d  %+6.1f%%  %5d\n",
			vs.Verb, vs.N, vs.OriginalTotal, vs.ReplayTotal,
			vs.DeltaTotal, dp, vs.Regressions)
	}

	if len(r.OKMismatches) > 0 {
		fmt.Fprintf(&b, "\nok mismatches (%d):\n", len(r.OKMismatches))
		for _, m := range r.OKMismatches {
			o := "ok"
			if !m.OriginalOK {
				o = "err(" + m.OriginalErr + ")"
			}
			n := "ok"
			if !m.ReplayOK {
				n = "err(" + m.ReplayErr + ")"
			}
			fmt.Fprintf(&b, "  %s %s \xe2\x80\x94 orig=%s new=%s\n", m.Verb, m.Args, o, n)
		}
	}

	if len(r.TopRegressors) > 0 {
		fmt.Fprintf(&b, "\ntop regressors (|\xce\x94tok| desc):\n")
		for _, t := range r.TopRegressors {
			fmt.Fprintf(&b, "  %s %s \xe2\x80\x94 orig=%d new=%d \xce\x94=%+d (%+.1f%%)\n",
				t.Verb, t.Args, t.OriginalTokens, t.ReplayTokens,
				t.DeltaTokens, t.DeltaPct)
		}
	}

	if r.CachePrefix != nil && r.CachePrefix.Overall.StablePairs > 0 {
		cp := r.CachePrefix
		fmt.Fprintf(&b, "\ncache prefix (ASH-108 A/B, stable pairs):\n")
		fmt.Fprintf(&b, "%-10s  %5s  %6s  %7s  %7s  %7s  %6s\n",
			"verb", "pairs", "stable", "enc_len", "old_pre", "new_pre", "\xce\x94gain")
		fmt.Fprintf(&b, "%s\n", strings.Repeat("-", 60))
		for _, vs := range cp.ByVerb {
			fmt.Fprintf(&b, "%-10s  %5d  %6d  %7d  %7d  %7d  %+6d\n",
				vs.Verb, vs.Pairs, vs.StablePairs, vs.AvgEncodedLen,
				vs.AvgPrefixOld, vs.AvgPrefixNew, vs.AvgPrefixGain)
		}
		fmt.Fprintf(&b, "%-10s  %5d  %6d  %7d  %7d  %7d  %+6d\n",
			"overall", cp.Overall.Pairs, cp.Overall.StablePairs,
			cp.Overall.AvgEncodedLen, cp.Overall.AvgPrefixOld,
			cp.Overall.AvgPrefixNew, cp.Overall.AvgPrefixGain)
	}

	return strings.TrimRight(b.String(), "\n")
}
