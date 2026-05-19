# Cache-hit telemetry — real Anthropic data via harness callback

> Status: **Mechanism A shipped under [ASH-188](https://linear.app/stazelabs/issue/ASH-188).** The Stop hook is registered in [.claude/settings.json](../.claude/settings.json), the `turn` verb upserts into a new ledger `turns` table, and `ash usage` prepends a `cache: X% hit …` line when turn rows exist. The §Decision gate below was explicitly stepped past — see §Why we built before the gate fired. Mechanism B (OTel) remains future work.

## Why this doc exists

The structural cache-shape work ([ASH-108](https://linear.app/stazelabs/issue/ASH-108), [ASH-135](https://linear.app/stazelabs/issue/ASH-135)) put `ash` response envelopes in a shape that *should* hit the Anthropic prompt cache when an agent makes two same-args calls within the 5-minute TTL. [cache-shape.md](cache-shape.md) is the contract; [cache-prefix-measurement.md](cache-prefix-measurement.md) verifies the structural win (+767 bytes shared prefix per stable pair, measured against a real session).

The thing we cannot measure from inside `ash`: whether those prefixes actually get *cached* in production. The Anthropic API tells the *caller* (the harness) about cache hits via `usage.cache_read_input_tokens` / `usage.cache_creation_input_tokens` on every assistant message. The daemon never sees these numbers; the ledger columns `tokens_cache_hit` / `tokens_cache_miss` ([internal/ledger/ledger.go:64-65](../internal/ledger/ledger.go#L64-L65)) have stayed at zero since they shipped.

[ASH-185](https://linear.app/stazelabs/issue/ASH-185) replaced the original manual-input `ash usage --hit/--miss` form (4 lifetime calls, all synthetic) with a structural ledger-side aggregate: it counts consecutive same-verb same-args pairs within the 5-min TTL as a *proxy* for cache eligibility. First live run produced **zero pairs across 157 calls in 6 hours** ([ASH-188](https://linear.app/stazelabs/issue/ASH-188)). That finding raises a hypothesis the proxy can't answer: maybe the cache *is* doing useful work via partial overlap or longer-window hits that the strict per-call-pair proxy misses. To answer it we need the real numbers.

This doc explores whether the real numbers are recoverable, at what attribution granularity, and what implementation would honestly serve the underlying question — without building it.

## What we can measure

Anthropic returns two relevant fields on every assistant message's `usage` object:

- `cache_read_input_tokens` — tokens served from a cache hit on this turn.
- `cache_creation_input_tokens` — tokens written into the cache on this turn (cache miss that populated a new entry).

Both apply to the **entire input prompt** for the API call, not to any one tool result inside the prompt. Two surfaces expose this to a collector running on the user's machine:

1. **Transcript JSONL.** Claude Code persists every turn to `~/.claude/projects/<encoded-project-path>/<session-id>.jsonl`. Each assistant entry carries the raw API response including `message.usage`. Schema is stable enough to scrape but is not an Anthropic-supported contract.
2. **OpenTelemetry.** Claude Code emits OTel spans when `OTEL_EXPORTER_OTLP_ENDPOINT` is set; the spans include the same usage data with proper attributes. Harness-agnostic in principle (any harness with OTel hooks could feed the same collector), but requires the user to configure OTel.

## What we cannot measure honestly

**Per-verb attribution.** A single turn typically contains many tool calls — some `ash`, some not (`mcp__linear-server__*`, harness-built-ins, etc.) — and the cache hit/miss numbers apply to the *full* prompt. There is no per-tool-result accounting in the API.

You can divide turn-level stats across the verb calls in that turn (e.g. proportional by response bytes), but the result is synthetic: it tells you what *would* be true if cache savings were distributed proportionally, not what *is* true. Two equally honest framings:

- **Turn-level truth.** "Turn T was 73% cached input." Coarse, but accurate.
- **Per-verb estimate.** "ash grep contributed roughly N tokens to this turn's cached prefix." Useful as a directional signal, dishonest as a precise per-verb metric.

The first framing answers the question that motivated [ASH-188](https://linear.app/stazelabs/issue/ASH-188) — *is the cache actually doing useful work?* — without overclaiming. Prefer it.

## Mechanism A — Stop hook scrapes transcript JSONL — **shipped**

Near-term, Claude-Code-specific. Mirrors the existing [PreToolUse.md](PreToolUse.md) pattern: a hook command registered in [.claude/settings.json](../.claude/settings.json) under the `Stop` event, implemented as a client-side `ash hook --event stop` fast path that posts to the daemon.

Registered in [.claude/settings.json](../.claude/settings.json):

```
"hooks": {
  "Stop": [{ "hooks": [{ "type": "command",
    "command": "\"$CLAUDE_PROJECT_DIR/bin/ash\" hook --event stop" }] }]
}
```

The hook command ([cmd/ash/hook_stop.go](../cmd/ash/hook_stop.go)):

1. Reads the Claude `Stop` payload from stdin. Payload fields consumed: `session_id`, `transcript_path`, `cwd`.
2. Tails up to 256 KiB from the end of the transcript JSONL, scans newline-delimited lines back-to-front for the last entry with `"type":"assistant"`, and extracts `message.id`, `message.usage.{input,output,cache_read_input,cache_creation_input}_tokens`, `sessionId`, and `timestamp`.
3. Resolves the project root from the payload's `cwd` (falls back to `os.Getwd`) and fires a fire-and-forget `turn` request over the per-project UDS. Soft-fail discipline matches the PreToolUse hook — any failure exits 0.

Pros:
- Reuses the existing hook plumbing end-to-end (verb registration, decision-in-client, async ledger fire, soft-fail discipline).
- No new dependency; the transcript is on disk regardless.
- Works on the developer machine where `ashd` runs.

Cons:
- Transcript JSONL schema is not a stable Anthropic contract; field renames would break the scraper. Mitigated by the soft-fail path: if the schema drifts, the verb stops recording and the proxy continues to work; nothing breaks.
- Claude-Code-specific. A different harness wouldn't benefit.
- Race window: `Stop` fires when the turn ends, but the transcript write is asynchronous. The 256 KiB tail window covers the common case; if the file isn't flushed yet we miss this turn and pick up the next one.

## Mechanism B — OTel collector ingests harness traces

Future, harness-agnostic. Stand up a tiny OTel collector (or inline OTLP receiver in `ashd`) that ingests spans matching `gen_ai.*` semantic conventions. Cache fields land on `gen_ai.usage.cache_read_input_tokens` / `gen_ai.usage.cache_creation_input_tokens` attributes.

Pros:
- Schema is governed by the OTel GenAI conventions, not a specific harness.
- Any OTel-emitting harness participates without code.
- Cleaner separation: the harness doesn't need to know about `ash`.

Cons:
- User must configure OTel (`OTEL_EXPORTER_OTLP_ENDPOINT` env var) for Claude Code.
- Adds a network surface to `ashd` (or a sidecar process), with the lifecycle/ownership concerns that brings.
- Heavier than the hook for a single-user dev loop; only earns its keep at multi-harness or multi-user scope.

## Daemon protocol — as shipped

The `turn` verb ([internal/verbs/turn/turn.go](../internal/verbs/turn/turn.go)) upserts into a new ledger `turns` table:

```
turns (
  id INTEGER PRIMARY KEY, session_id TEXT, turn_id TEXT UNIQUE,
  harness_session_id TEXT, model TEXT, ts INTEGER,
  input_tokens, output_tokens,
  cache_read_tokens, cache_creation_tokens
)
```

`UNIQUE(turn_id)` + `INSERT OR IGNORE` makes the insert idempotent on the Anthropic `message.id`, so a re-fire (manual retry, hook rerunning on session resume) is a no-op rather than a double-count. The verb returns a tiny `{turn_id, inserted}` result body; the hook never reads it.

We chose this **Shape 1** over back-filling the per-row `tokens_cache_hit`/`tokens_cache_miss` columns on `calls` because the cache numbers describe a *turn* (the whole prompt's overlap with the prior prompt), not any one verb invocation; writing them on a single representative call row would be misleading. The row-level columns stay reserved for a future producer that has per-call numbers at request time, as originally noted in [cache-shape.md](cache-shape.md) §Out of scope.

`ash usage` ([internal/verbs/usage/usage.go](../internal/verbs/usage/usage.go)) queries the turns table with the same `since`/`session` filter and, when rows exist, prepends a one-line summary above the per-verb table:

```
§usage: all, since=24h0m0s — 1415 calls across 17 verbs
cache: 97.9% hit (279.2K read / 6.0K created / 6 fresh in / 238 out across 1 turn)
```

Hit-rate denominator is `cache_read / (cache_read + cache_creation + input)` — Anthropic's own decomposition of total input tokens. When no turn rows are in the window the field is `nil` and the output is byte-identical to the pre-ASH-188 proxy surface.

`turn` is excluded from `ash usage`'s per-verb aggregate the same way `usage` itself is today ([internal/verbs/usage/usage.go](../internal/verbs/usage/usage.go) `byVerb` loop) so the meta-instrumentation doesn't pollute the signal.

## Decision gate — should we ship?

The gate below was the original framing — kept for the historical reasoning even though it's been answered.

The honest question is not *can we build this*; it's *would the data change a decision*. Two pre-conditions:

1. **Re-run `ash usage --since 7d --session all` after a representative week.** If pairs still cluster near zero, the strict-proxy answer is already definitive enough — the cache isn't doing much and no real-traffic measurement will rescue that. Skip building.
2. **If pairs are non-zero, decide what real-cache-hit-rate threshold would change behavior.** [docs/value-assessment/decision.md](value-assessment/decision.md) recommends not investing further in cache-shape work. For this telemetry to be worth its build cost, there has to be a credible action it would unblock — e.g. *if real cache hit rate < X%, rip out the ASH-108/135 envelope reorder*. If there's no action waiting on the data, the data isn't worth collecting.

### Why we built before the gate fired

The proxy ([ASH-185](https://linear.app/stazelabs/issue/ASH-185) `ash usage`) counts **consecutive byte-identical same-verb same-args pairs within 5 min**. That's a very strict definition of "cache-eligible." But the Anthropic prompt cache hits on shared *prompt prefix*, not on identical individual tool results — and a Claude Code prompt prefix grows monotonically across turns (system prompt + all prior tool results). Two consecutive assistant turns can share megabytes of cached prefix without any single ash verb call having a byte-identical predecessor.

Concretely: if the proxy says 0 pairs and the real hit rate is 97%, the gate's premise ("if pairs ≈ 0, no real-traffic measurement will rescue that") is wrong. The first smoke-test turn this instrument recorded had a real hit rate of 97.9% — a single noisy data point, but enough to confirm the proxy was load-bearing for the wrong question. The instrument earns its keep by answering the actual question, not by clearing the gate as written.

## Out of scope here

- **Updating [04-cache.md](value-assessment/04-cache.md) with the TTL caveat.** ASH-188 calls this out separately; do it as a small standalone edit, not as part of this design.
- **Implementing any of the above.** This doc is the scoping artifact; ticket-and-build is gated on §Decision gate.
- **Replay-based A/B for cache prefix savings.** Already shipped under [ASH-135](https://linear.app/stazelabs/issue/ASH-135); `ash replay --cache_prefix true` is the structural scoreboard.