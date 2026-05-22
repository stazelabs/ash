// Package usage implements the `ash usage` verb.
//
// Original purpose (ASH-134): populate the Anthropic prompt-cache hit/miss
// columns by accepting a harness-supplied --hit/--miss pair to back-fill
// the prior ledger row. In 30 days of production traffic, no harness
// wired up that callback — the verb saw 4 lifetime invocations, all
// synthetic test calls (docs/value-assessment/04-cache.md).
//
// ASH-185 redesign: drop the manual-input form and replace with a
// ledger-side aggregate that estimates cache-friendliness from
// argument-repetition counts. The proxy is structural — consecutive
// same-verb calls with byte-identical Args within Anthropic's 5-minute
// prompt-cache TTL would land on a warm prefix. We can compute that
// from the ledger alone, no harness callback required.
//
// ASH-188 augmentation: when the harness's Stop hook is wired up
// (see cmd/ash/hook_stop.go + the `turn` verb), real Anthropic cache
// hit/miss numbers land in the ledger `turns` table. This verb
// summarizes them on a one-line cache header above the proxy table,
// turning the structural proxy into a sanity check against the real
// signal. See docs/cache-telemetry.md for design context.
//
// Args:
//
//	since   string  (optional) - time window; Go duration + d/w/mo, default 24h.
//	session string  (optional) - "current" (default), "all", or explicit session id.
//
// Result reports per-verb call count, distinct arg-set count, and
// cache-eligible pair count (consecutive same-args within 5 min).
// Tier D (human-warm); see docs/optimization-tiers.md.
package usage

import (
	"bytes"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/stazelabs/ash/internal/ledger"
	"github.com/stazelabs/ash/internal/proto"
	"github.com/stazelabs/ash/internal/verbs/argutil"
)

// CacheTTL is the Anthropic prompt-cache time-to-live we use to define
// "cache-eligible" consecutive pairs. Same-verb same-args calls within
// this window of each other would land on a warm cache prefix.
const CacheTTL = 5 * time.Minute

// DefaultSince is the default --since window.
const DefaultSince = 24 * time.Hour

type Args struct {
	Since   time.Duration
	Session string
}

// VerbStats is one row of the per-verb breakdown.
type VerbStats struct {
	Verb       string `msgpack:"verb"`
	Calls      int    `msgpack:"calls"`
	UniqueArgs int    `msgpack:"unique_args"`
	CachePairs int    `msgpack:"cache_pairs"` // consecutive (same verb, same args, within CacheTTL)
	CacheRatio int    `msgpack:"cache_ratio"` // percent — CachePairs / max(Calls-1, 1) * 100
}

// TurnsSummary is the harness-reported Anthropic cache accounting for
// the report window. Populated by the Stop hook → `turn` verb path
// (ASH-188 / ASH-185 Option A). When no rows in the window have turn
// data, the field is nil and pretty output is byte-identical to the
// pre-ASH-188 surface — the arg-repetition proxy.
type TurnsSummary struct {
	Turns               int `msgpack:"turns"`
	InputTokens         int `msgpack:"input_tokens"`
	OutputTokens        int `msgpack:"output_tokens"`
	CacheReadTokens     int `msgpack:"cache_read_tokens"`
	CacheCreationTokens int `msgpack:"cache_creation_tokens"`
}

// Result is the structured response of `ash usage`. Turns rides at the
// end so adding it doesn't shift the cache-stable prefix of the
// pre-ASH-188 envelope (docs/cache-shape.md).
type Result struct {
	Since   string        `msgpack:"since"`
	Session string        `msgpack:"session"`
	Calls   int           `msgpack:"calls"`
	PerVerb []VerbStats   `msgpack:"per_verb"`
	Turns   *TurnsSummary `msgpack:"turns,omitempty"`
}

func ParseArgs(in map[string]any) (*Args, *proto.Error) {
	a := &Args{Since: DefaultSince, Session: "current"}
	if v, ok := in["since"]; ok && v != nil {
		s, ok := argutil.ToString(v)
		if !ok {
			return nil, &proto.Error{Code: "args", Msg: "since must be a duration string"}
		}
		d, err := argutil.ParseDuration(s)
		if err != nil {
			return nil, &proto.Error{Code: "args", Msg: "since: " + err.Error()}
		}
		a.Since = d
	}
	if v, ok := in["session"]; ok && v != nil {
		s, ok := argutil.ToString(v)
		if !ok || s == "" {
			return nil, &proto.Error{Code: "args", Msg: "session must be a string"}
		}
		a.Session = s
	}
	return a, nil
}

// RunWithLedger queries the ledger and computes per-verb arg-repetition
// stats. The verb itself is excluded from the output (a request for
// "cache stats since 24h" shouldn't include the call making that
// request — circular and uninteresting).
func RunWithLedger(led *ledger.Ledger, a *Args) (*Result, *proto.Error) {
	// "all" is the ledger's no-filter sentinel: empty string. The
	// daemon's WHERE clause would otherwise match no rows on the
	// literal "all". Mirrors report.querySessionID.
	sid := a.Session
	if sid == "all" {
		sid = ""
	}
	opts := ledger.QueryOpts{
		SessionID: sid,
		Limit:     5000,
	}
	if a.Since > 0 {
		opts.Since = time.Now().Add(-a.Since)
	}
	calls, err := led.QueryWindow(opts)
	if err != nil {
		return nil, &proto.Error{Code: "ledger", Msg: err.Error()}
	}

	// QueryWindow returns DESC; reverse to chronological so pair
	// detection mirrors the order the harness made the calls.
	for i, j := 0, len(calls)-1; i < j; i, j = i+1, j-1 {
		calls[i], calls[j] = calls[j], calls[i]
	}

	byVerb := map[string][]ledger.Call{}
	for _, c := range calls {
		if c.Verb == "usage" {
			continue
		}
		byVerb[c.Verb] = append(byVerb[c.Verb], c)
	}

	stats := make([]VerbStats, 0, len(byVerb))
	total := 0
	for verb, cs := range byVerb {
		vs := VerbStats{Verb: verb, Calls: len(cs)}
		uniq := map[string]struct{}{}
		for _, c := range cs {
			uniq[string(c.ArgsMsgpack)] = struct{}{}
		}
		vs.UniqueArgs = len(uniq)
		for i := 1; i < len(cs); i++ {
			if !bytes.Equal(cs[i].ArgsMsgpack, cs[i-1].ArgsMsgpack) {
				continue
			}
			if cs[i].Timestamp.Sub(cs[i-1].Timestamp) > CacheTTL {
				continue
			}
			vs.CachePairs++
		}
		if vs.Calls > 1 {
			vs.CacheRatio = vs.CachePairs * 100 / (vs.Calls - 1)
		}
		stats = append(stats, vs)
		total += vs.Calls
	}
	sort.Slice(stats, func(i, j int) bool {
		if stats[i].Calls != stats[j].Calls {
			return stats[i].Calls > stats[j].Calls
		}
		return stats[i].Verb < stats[j].Verb
	})

	since := ""
	if a.Since > 0 {
		since = a.Since.String()
	}

	// Pull turn rows for the same window. QueryTurns reuses QueryOpts
	// shape but ignores VerbFilter (turns have no verb dimension). Soft-
	// fail: if the turns table is unreadable we just omit the summary.
	var turnsSummary *TurnsSummary
	if turns, err := led.QueryTurns(opts); err == nil && len(turns) > 0 {
		s := &TurnsSummary{Turns: len(turns)}
		for _, t := range turns {
			s.InputTokens += t.InputTokens
			s.OutputTokens += t.OutputTokens
			s.CacheReadTokens += t.CacheReadTokens
			s.CacheCreationTokens += t.CacheCreationTokens
		}
		turnsSummary = s
	}

	return &Result{
		Since:   since,
		Session: a.Session,
		Calls:   total,
		PerVerb: stats,
		Turns:   turnsSummary,
	}, nil
}

// PrettyResponse renders the cache-friendliness summary as a header line
// plus a per-verb table. Tier D so cost matters less than legibility.
func PrettyResponse(_ *proto.Request, rsp *proto.Response) string {
	if !rsp.OK {
		return proto.PrettyResponseHeader(rsp)
	}
	var r Result
	if err := proto.UnmarshalData(rsp, &r); err != nil {
		return "ok\n<unrecognized usage result>"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "§usage: %s, since=%s — %d calls across %d verbs\n",
		r.Session, r.Since, r.Calls, len(r.PerVerb))
	if r.Turns != nil {
		writeTurnsLine(&b, r.Turns)
	}
	if len(r.PerVerb) == 0 {
		b.WriteString("\nno calls in window.\n")
		return b.String()
	}
	fmt.Fprintf(&b, "\n%-10s  %5s  %6s  %5s  %5s\n",
		"verb", "calls", "unique", "pairs", "ratio")
	b.WriteString(strings.Repeat("-", 42))
	b.WriteByte('\n')
	for _, vs := range r.PerVerb {
		fmt.Fprintf(&b, "%-10s  %5d  %6d  %5d  %4d%%\n",
			vs.Verb, vs.Calls, vs.UniqueArgs, vs.CachePairs, vs.CacheRatio)
	}
	b.WriteString("\npairs = consecutive same-args calls within 5 min " +
		"(Anthropic prompt-cache TTL). For structural prefix-overlap stats " +
		"see `ash replay --cache_prefix true --since <window>`.\n")
	if r.Turns != nil {
		b.WriteString("turns = harness-reported Anthropic API turns " +
			"(ASH-188; populated by the Stop hook).\n")
	}
	return b.String()
}

// writeTurnsLine renders the one-line real-cache summary above the
// per-verb table. Hit rate is cache_read / total-input, matching
// Anthropic's own usage accounting (cache_read + cache_creation +
// input together make up the full prompt).
func writeTurnsLine(b *strings.Builder, t *TurnsSummary) {
	totalInput := t.CacheReadTokens + t.CacheCreationTokens + t.InputTokens
	hitPct := 0.0
	if totalInput > 0 {
		hitPct = float64(t.CacheReadTokens) * 100 / float64(totalInput)
	}
	turnsWord := "turns"
	if t.Turns == 1 {
		turnsWord = "turn"
	}
	fmt.Fprintf(b, "cache: %.1f%% hit (%s read / %s created / %s fresh in / %s out across %d %s)\n",
		hitPct,
		humanizeTokens(t.CacheReadTokens),
		humanizeTokens(t.CacheCreationTokens),
		humanizeTokens(t.InputTokens),
		humanizeTokens(t.OutputTokens),
		t.Turns, turnsWord,
	)
}

// humanizeTokens renders large token counts in K/M form. Cutoffs are
// the conventional 1k / 1M boundaries; below 1k we keep the raw int
// so small workloads stay legible.
func humanizeTokens(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fK", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}
