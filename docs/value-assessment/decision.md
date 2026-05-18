# ash / ashmcp — value assessment decision

**Date:** 2026-05-18 · **Commit:** f35cf2c5 · **Scope:** 1-day data-mining sweep of existing infrastructure (no new measurement built).

## Headline finding

**Continue, with focused scope refinements.** The bash-baseline evidence is decisive: ash delivers a **−63.8% token reduction** on the canonical bench corpus, which translates to **~5–16 M tokens/year** at observed call volumes (5,000 calls in the last 30 days), plus a **5–10× wall-clock latency win** that is unambiguous at any call volume. Cross-validation against the real Claude tokenizer shows **0 disagreements out of 96 rows**, so the cl100k savings claims are real.

The case for continued investment is qualitatively strong on three of four dimensions and clear-eyed on the fourth:

| dimension | finding | go/no-go signal |
|---|---|---|
| Token win vs bash | −63.8% corpus, ~5–16 M tokens/year aggregate | **Strong go** |
| Latency win vs bash | 5–10× total wall-clock; >100× on heavy grep | **Strong go** |
| Friction (cost of redirects) | ~12% hook-deny rate; 13 of 17 rules redirect cleanly | **Manageable; small fixable list** |
| ashmcp-vs-harness-native | **Not measured** today; deferred to follow-on | **Decision-blocking for adoption push** |

## Evidence summary

1. **[01-bench.md](01-bench.md)** — Bench corpus replay: 21 cases, −63.8% tokens, ash beats bash on every case for latency. Win concentrated in agent-hot Tier A verbs (grep/find/git/stat). Read is breakeven. `read_tiny_range` is the lone token loss (+47%, but only 8 absolute tokens).
2. **[02-aggregate.md](02-aggregate.md)** — Real-session aggregate: 5,000 calls / 30 d, 1.93 M tokens_out, 519 s exec. Fermi: ~5 M tokens/year conservative, ~16 M/year realistic. Tier A is 88% of calls and 90% of tokens — bench cases are representative of where the action is.
3. **[03-friction.md](03-friction.md)** — Friction inventory: 583 hook denies in 30 d sample. Biggest fixable: find args validation (124 errors with bad `--type` value, ~1-hour fix). Largest unmeasured friction: chained-bash deny rejecting whole commands (ASH-170 in progress). Lang verb is the only candidate for scope reduction (30% error rate at low volume).
4. **[04-cache.md](04-cache.md)** — Cache-shape validation: 190/434 (44%) consecutive call pairs are arg-stable. Avg shared prefix 424 bytes (vs 8 pre-ASH-135). Help responses dominate per-pair savings (+14,298 bytes); hook denies dominate volume (172 stable pairs). Structural win confirmed; actual Anthropic prompt-cache hit-rate is unmeasurable from our side.
5. **make validate** — 96/96 rows clean (zero `✗` disagreements between cl100k and Claude tokenizer). The savings claims hold under the real tokenizer.

## Non-technical cost ledger

| dimension | count |
|---|---:|
| Production Go LOC | ~29,700 |
| Test Go LOC | ~19,100 |
| Docs markdown | ~1,450 lines |
| Vocab inventory (auto-generated) | 344 lines |
| Documented gotchas in [CLAUDE.md §Gotchas](../../CLAUDE.md#gotchas) | 10 |
| Live verbs | 23 (per `ash help`) |
| Binaries | 4 (`ash`, `ashd`, `ashmcp`, `ashd-clean`) |

**Reading:** ~50 k LOC total with strong test coverage (64% test:production ratio). 10 gotchas is comparable to mature tooling surfaces like `git` or `docker` — not a runaway accidental-complexity signal. The maintenance burden is **non-trivial but not extraordinary** for a system in this category.

## Recommendation: continue with focus

Three explicit calls:

### KEEP investing in:
- **Tier A verbs and the daemon/wire infrastructure they ride on.** This is where the measured wins live.
- **ashmcp.** The 148 harness-tool denies in 30 days (Read 109, Write 28, Edit 4, Grep 7) show that even Claude Code's native users default to harness tools. If ashmcp owns that namespace via MCP, the friction disappears and the wins land transparently. This is the highest-leverage adoption move available.
- **`ash replay --cache_prefix`** as a regression-detection gate. 451 calls replayed with 0 result-mismatches across recent commits is strong evidence the verb surface is stable; this verb is independently valuable as a CI guard.

### FIX before any adoption push (each <1 day):
- **find args validation** (124 errors / 30 d): accept common variants of `--type` or expand the error to show the fix.
- **find/grep truncation visibility** (37% / 18% truncation rates): either raise defaults or improve the truncated-signal prominence so agents don't act on partial data unknowingly.
- **chained-bash deny experience** (ASH-170 in progress): finish naming the matched segment and consider allowing the un-denied prefix to run.

### NARROW or RETIRE:
- **lang verb.** 30% error rate at 23 calls / 30 d. Either commit to better LSP heuristics or formally narrow the claim to "exact-symbol-name lookups only." 30% error rate at low volume is the strongest "this isn't pulling its weight" signal in the inventory.
- **`ash usage`** verb design needs revisiting: it requires manual `--hit`/`--miss` inputs because the ledger has no automatic way to capture cache hit-rate. Either (a) wire harness callbacks that feed the ledger, or (b) drop the manual-input form and rely on `ash replay --cache_prefix` as the structural proxy.

### DO NOT invest further in:
- **Per-verb micro-optimizations for `read`** purely for token shape. Read is breakeven; the case for `ash read` rests on jail enforcement, binary base64 handling, ledger instrumentation, and atomic semantics — those are the value props, not bytes saved.
- **More cache-shape work** beyond the current ASH-108/135 design. The structural win is real but is concentrated in help+hook responses; further investment hits diminishing returns without harness-side cache-hit telemetry to measure against.

## Open questions deferred from today

Today's sweep used existing data only. Three questions remain that strictly require new measurement to answer, with rough build estimates so you can decide whether/when to invest:

### Q1 — Does ash beat harness-native tools (Read/Grep/Glob/Edit/Write) inside Claude Code? ✅ **ANSWERED**
- **Result:** yes, by **−64% on the comparable subset** (read + grep + find/glob, 13 cases). For read specifically, ash flips from "breakeven vs bash" to **−18% to −26% vs harness** because the harness Read format includes cat-n line-number prefixes that bash `cat` doesn't. Methodology + per-case table in [05-harness.md](05-harness.md).
- **Caveat:** the harness-tool envelope tax (~10–30 tokens per call) is not modeled; back-of-envelope analysis suggests this slightly under-counts ash's win. Modeled by simulation rather than direct measurement because the in-repo hook denies real harness invocations; the simulation is grounded in documented response formats.
- **What changes:** the adoption push for Claude Code users is no longer gated on this question. The decision now hinges on Q2 (ashmcp envelope tax).

### Q2 — What's the ashmcp envelope tax on a uniform workload? ✅ **ANSWERED**
- **Result:** ashmcp **beats harness-native MCP at every emit mode** (−48% in json default, −57% in compact, −67% in pretty). Envelope tax vs direct CLI: **+66% (json default) → +36% (compact) → +6% (pretty)**. Find is the worst-case verb (+262% to +782% in json mode) because the per-record `{path,type,size,mtime}` payload dominates small results; pretty mode crushes this. Methodology + three-mode table in [06-mcp.md](06-mcp.md).
- **What changes:** ashmcp has standalone adoption value confirmed. The most concrete leverage moves surfaced by this measurement: **shift the ashmcp default emit mode from json to compact** (preserves structured access, ~half the envelope tax) and/or **fix `ash find` to emit path-only StructuredContent when `--meta=false`** (closes the find loss). Both are small follow-on tickets, not adoption blockers.

### Both adoption-gate questions are now answered.

Q1 (harness-native baseline) and Q2 (ashmcp envelope) both came back in ash's favor. The remaining gates on the adoption push are organizational (Homebrew packaging — ASH-118; docs/onboarding; outreach), not measurement.

### Q3 — What % of structural cache prefixes actually land as Anthropic prompt-cache hits? (build: blocked)
- ASH-108/135 produced a 424-byte shared prefix for 44% of consecutive call pairs. We don't know how often that prefix is still in cache when re-used.
- **Need:** harness-side cache-hit telemetry fed back to the daemon (or read by `ash usage`).
- **Status:** likely blocked on Anthropic exposing this data. Today the structural prefix is a defensible proxy; we should stop chasing the real-hit number until/unless the harness can report it.

## What this assessment did NOT measure

In the interest of transparency about what we know vs what we believe:

- **Real-user adoption signal.** This is a single-user (you) ledger. The friction inventory reflects one workflow; another developer's hot paths might surface different friction.
- **Bug rate / stability.** The replay sweep showed 0 result-mismatches, but bench corpus stability is not the same as multi-user load stability.
- **Onboarding cost for a new contributor.** Estimated only from gotcha count + docs LOC; not measured by actually onboarding someone.
- **Cost of an adoption push** (writing setup docs, MCP-server installation guides, etc.) — separate from the technical work and not estimated here.

---

*Provenance: bench (sweep 1) at f35cf2c5 via `make bench-baseline`; report (sweeps 2-3) at f35cf2c5 via `ash report --since 30d`; replay (sweep 4) at f35cf2c5 via `ash replay --cache_prefix true --since 24h`; validate (sweep 5) at f35cf2c5 via `make validate`. All evidence sourced from `.ash/ledger.db`, [`bench/`](../../bench/), and [`testdata/validate_results.md`](../../testdata/validate_results.md).*
