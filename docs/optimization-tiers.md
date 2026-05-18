# Optimization tiers — how to decide what to optimize

This doc is the policy layer for future token (and latency) work.
[encoding-results.md](encoding-results.md) records *what* we measured
and decided; this doc records *how* we'll decide next time.

Originating ticket: [ASH-131](https://linear.app/stazelabs/issue/ASH-131).

## The problem

Token optimization isn't uniform across the verb surface. `read` runs
hundreds of times in a single coding session and every shaved token
compounds; `report` runs once or twice and over-compaction there costs
human readability without meaningful aggregate savings. Treating every
verb the same — applying the same envelope/header/footer rules to all 23
verbs — wastes engineering on calls that don't matter and risks
under-investing in the ones that do.

We need an explicit categorization so optimization decisions are
deliberate rather than reflexive.

## The four tiers

Each verb is assigned a single tier based on **who reads the output**
and **how often per session**. The tier travels with the schema (added
to the help registry under `Tier`), so `ash help --verb <name>` and
`ash report` can surface and group by it.

| Tier | Name | Reader | Per-session calls | Verbs |
|---|---|---|---|---|
| **A** | Inner-loop agent | Agent | 10s–100s+ | `read`, `grep`, `find`, `edit`, `write`, `diff`, `git`, `hook` |
| **B** | Episodic agent | Agent | 1–10 | `test`, `build`, `lang`, `stat`, `workspace`, `recap` |
| **C** | Bootstrap | Agent (1–2 per session at most) | 0–2 | `help`, `init`, `uninit` |
| **D** | Instrumentation / meta | Often human-in-terminal; sometimes agent on user's request | 0–5 | `report`, `metrics`, `replay`, `usage`, `bench`, `stop` |

Tiers are deliberately coarse. Anything more granular invites
bikeshedding and a per-verb policy file. Four buckets is enough to
distinguish "every token matters" from "humans read this."

## Per-tier policy

### Tier A — inner-loop agent

*Every token compounds. Latency budget is tightest here.*

- **Optimize envelope, header, footer, truncation hints, error strings aggressively.** This is where header-compaction (ASH-100), truncation glyph (ASH-120), and structured truncation body (ASH-121) earned their place.
- **Keep flag names short.** snake_case splits into multiple tokens under cl100k/o200k; prefer kebab-case or single-word. The flag-rename pass in [cli-tokens.md](cli-tokens.md) §2.1 is Tier-A territory.
- **Cache-shape stability is load-bearing.** Tier A verbs ride the [cache-shape.md](cache-shape.md) contract. Reordering `proto.Response` or `jsonResponse` fields can break Anthropic-prompt-cache prefix sharing — be deliberate, and let the pinning tests in `internal/proto/proto_test.go` guide you.
- **Token-validate before merging.** Run `make validate` for any change that touches a Tier A verb's pretty/error/header strings. Treat a `tokens_out` regression as a bug.
- **Daemon p50 budget is tightest here.** Latency wins matter — these calls happen in the agent's inner loop and add up just like tokens do.

### Tier B — episodic agent

*Moderate compounding. Body is data, not envelope.*

- **Envelope hygiene is the same as Tier A** since they share the proto envelope anyway. The pretty header, stderr footer, and metrics envelope are uniform infrastructure (see *Cross-cutting* below).
- **Body content is data, not envelope.** `test` per-test failures, `lang` outline nodes, `build` error rows carry information the agent needs to act. Don't compress at the cost of agent parseability — saving 5 tokens on a `test` failure row that the agent then misreads costs orders of magnitude more than it saves.
- **Token-validate for envelope changes; body changes need only correctness review.**

### Tier C — bootstrap

*One-shot but heavy. Single-call wins beat hundreds of inner-loop micro-wins.*

- **Optimize aggressively *because* the call is large.** `ash help` was 3,703 tokens pre-ASH-147; ASH-147 cut that to ~150. A single shipped editorial sweep on Tier C dwarfs months of inner-loop micro-optimization.
- **The body dominates, not the envelope.** Don't over-think per-call envelope wins on Tier C; reach for editorial rewrites (description rewrites, schema-field renames) instead.
- **One-shot caching matters.** Tier C output is often what the agent loads once and refers back to from context. A stable shape across sessions helps prompt-cache reuse.

### Tier D — instrumentation / meta

*Humans read this. Aggregate savings are a rounding error.*

- **Do not over-optimize at the cost of readability.** Column alignment, full metric names, prose hints, units, section dividers — keep them. A human running `ash report --since 1h` in their terminal needs to scan it visually, and compaction hurts that.
- **Aggregate savings here are negligible against the agent budget.** Five calls per session × 50 tokens per call = 250 tokens. Not worth a readability regression.
- **Exception — the envelope still uses Tier A rules.** Shared infrastructure (`proto.Response` framing, stderr footer, pretty header) rides on every call regardless of tier. Tier D applies to the **body** of these verbs, not the envelope around them.
- **Flag names can be longer and more descriptive without guilt.** `--since`, `--verb`, `--top` are fine as-is; `--all-roots` doesn't need to be `--all`. Tier D is allowed to be ergonomic.

## Cross-cutting — what stays uniform

Some optimizations apply to all tiers because they're paid by every
call, regardless of who reads the output:

- **Pretty header** — ASH-100's `§<verb>: …` form. Saves 3 cl100k tokens per call universally.
- **Stderr footer** — ASH-114 compaction. Paid per-call by the harness whether the verb is `read` or `report`.
- **Metrics envelope** — `omitempty` hygiene, short keys via proto v2 (if/when shipped).
- **Truncation glyph** — `…` (U+2026), single-token.
- **Cache-shape contract** — `proto.go` field ordering invariant.

These are infrastructure, not per-verb decisions. Don't relitigate them
when working on a single verb.

## Decision template for new optimization work

Before shipping a token-shape change, answer in one line each:

1. **Which tier does the affected verb sit in?** (Check `ash help --verb <name>`.)
2. **Does the change touch the envelope or the body?** (Envelope changes are cross-cutting; body changes are per-tier.)
3. **What's the measured per-call saving** (run `make validate` for cl100k+Claude deltas) **and what's the projected session aggregate** (per-call × Tier-typical frequency)?
4. **Does the change hurt readability** (Tier D) **or agent parseability** (Tier B body)? If yes, the saving must clearly outweigh it.

If (4) is "no" and (3) is positive on a Tier A verb, ship it. If (4) is
"yes" on a Tier D verb, don't.

## Tier-aware tooling (today and planned)

**Today** (this ticket):
- `ash help --verb <name>` shows `tier: <X>`.
- `ash report` per-verb table groups rows by tier with subtotal lines.
- JSON/msgpack `report` carries a `tier` field per row.

**Deferred** (separate tickets):
- A `make validate-tier-a` CI gate that fails on `tokens_out`
  regressions for Tier A verbs while tolerating drift on Tier D.
- A `ash report --tier A` filter flag.
- Tier annotations in [docs/vocab/inventory.md](vocab/inventory.md) so
  the output-surface inventory is groupable by tier.

## Retro — shipped work re-scored

A walk through token-shape tickets that landed before this framework
existed, judged against the four-tier policy. Each entry: ticket,
affected verbs/tiers, verdict.

| Ticket | Touched | Verdict |
|---|---|---|
| ASH-100 (pretty header `§<verb>: …`) | All tiers — envelope | **Kept.** Uniform infrastructure; correctly applies to every tier including D. Saves 3 cl100k tokens per call universally. |
| ASH-114 (stderr footer compaction) | All tiers — envelope | **Kept.** Same reasoning as ASH-100. |
| ASH-114 (read header compaction) | Tier A (`read`) | **Kept.** Inner-loop verb; envelope shave on the hottest verb. Textbook Tier A win. |
| ASH-120 (`…` truncation glyph) | Tier A dominant (`find`/`grep`) | **Kept.** Truncation fires almost exclusively on Tier A verbs. Single-token glyph beats prose. |
| ASH-121 (structured truncation body) | Tier A dominant | **Kept.** Same reasoning as ASH-120; the structured form keeps the raise-flag hint that prevents agent regressions. |
| ASH-127 (`_meta` error shape) | Cross-cutting | **Kept.** Error-shape standardization; uniform across tiers. |
| ASH-143 (compact per-arg help format) | Tier C (`help`) | **Kept.** Body-side compaction on a single-shot heavy call. Tier C textbook. |
| ASH-147 (no-arg `ash help` one-liner) | Tier C (`help`) | **Kept.** Single-shipment ~3,500-token saving on the biggest single-call cost. Dwarfs any inner-loop micro-win. |
| ASH-153 (MCP `format=compact`) | Tier-agnostic, opt-in | **Kept.** Opt-in `--format compact` for row-shaped verbs; doesn't change defaults for any tier. |

No rollbacks identified at the time of writing. Tier D verbs (`report`,
`metrics`, `bench`) have not been over-compacted — their pretty output
still carries column headers, units, section dividers, and prose hints.
If future audits surface candidates, add rows here marked
`rolled back in <ticket>`.
