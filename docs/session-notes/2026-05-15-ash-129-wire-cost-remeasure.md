# ASH-129 — re-measure MCP wire cost post-ASH-124

**Task.** Rerun `cmd/wirecmp -claude` against the post-ASH-124 daemon, update
[docs/mcp/wire-cost.md](../mcp/wire-cost.md) with a pre-vs-post column and a
narrative section, snapshot the methodology here.

**Verbs used.** `ash read`, `ash find`, `ash grep`, `ash git --op log`,
`ash write`, plus `make all` + `go build ./cmd/wirecmp` and one
`bin/wirecmp -claude` run with `ANTHROPIC_API_KEY` sourced from `.env.local`.

## Headline numbers

| view | pre-ASH-124 | post-ASH-124 |
|---|---:|---:|
| MCP / CLI Claude tokens (aggregate, all 6 fixtures) | +239% | +429% |
| MCP / CLI Claude tokens (aggregate, excluding `help`) | +27.9% | +9.6% |
| `find **/*.go (20)` Δ vs CLI | +294% | -6% |
| `grep ^func Run` Δ vs CLI | +294% | -6% |
| `stat README.md` Δ vs CLI | +246% | +85% |
| `read README:1-60` Δ vs CLI | +12% | +8% |
| `help` Δ vs CLI | +987% | +1613% |

The aggregate-with-`help` number got *worse* (+239% → +429%) but the
fixture-by-fixture picture is the opposite: tax 1 (envelope wrapper) and
tax 3 (metrics counted against tokens) closed cleanly, and terse-response
fixtures flipped from +294% to slightly cheaper than CLI. The aggregate
regressed because `help` scope grew between runs:

- 14 → 20 verbs registered.
- ASH-143 surfaced the `PH` placeholder in the structured arg record.
- ASH-144 changed the `Long` msgpack tag from `-` to `long,omitempty`, so
  every arg's long description now ships in the wire JSON.

Each of those was the right call for the CLI/help-text surface in
isolation, but the MCP `help` envelope is the JSON dump of
`help.Registry()` — adding fields to that record scales linearly across
every arg across every verb. It dominates the aggregate.

## What the data actually says

Tax 1 + tax 3 (the two things ASH-124 set out to close) closed
unambiguously:

- For every fixture, the `{"ok":..., "data":..., "metrics":...}` wrapper
  is gone. Metrics now ride in MCP `_meta` and don't count against tokens.
- Terse-response fixtures got *cheaper* over MCP than CLI. The reason is
  small: `{"records":[],"count":0}` is one Claude token shorter than the
  CLI's `§grep: 0 match(es) (0 file(s) scanned)` pretty header. But the
  sign change is the story — pre-ASH-124, every fixture cost more over
  MCP regardless of payload size.

Tax 2 (JSON-vs-pretty, named fields per record) is exactly as expected:
unaffected by ASH-124, dominant for structured-record verbs. `help` is
the worst case (17× CLI), `stat` is the canonical small case (+85% on
one record), and any future list-of-records verb (`lang outline`,
`lang refs`, `find --meta true` over a big tree) will scale linearly.

The "MCP is cheaper than CLI" hypothesis from ASH-104 was empirically
wrong at ASH-123, and post-ASH-124 it's *conditionally* right — true for
read-side fixtures that emit short or scalar payloads, false for
structured-record verbs.

## Methodology

`bin/wirecmp -claude -repeat 5` against the local daemon. Per fixture:

1. One canonical roundtrip (`Transport=mcp`) whose `Response` feeds *both*
   renderings — CLI via `verbs.PrettyHandlers()[verb](req, rsp)`, MCP via
   `proto.MCPEnvelope(rsp)`. Same `Response`, two encoders, so the delta
   is pure transport overhead.
2. Five additional roundtrips per transport for the latency median.
3. Tokens via `cl100k_base` locally and Anthropic `count_tokens`
   (`claude-sonnet-4-5`) for ground truth. They agree within 0.5% at
   aggregate.

Pre-numbers are the post-ASH-123 snapshot previously committed in
[docs/mcp/wire-cost.md](../mcp/wire-cost.md) at commit `f37eb6d`. Same
tool, same fixtures, daemon before ASH-124 landed.

## Confounders documented in the doc

Two rows aren't strict apples-to-apples and the doc flags them with `†` /
`‡` footnotes:

- **`git status`** workload changed: pre-ASH-124 had a dirty working tree
  (Phase 2 work in flight), post is clean. Both CLI and MCP shrank, so
  the absolute MCP claude delta (229 → 38) is misleading. The `Δ vs CLI`
  *ratio* (70% → 36%) is the load-bearing comparison — both transports
  saw the same workload, so the protocol overhead ratio is comparable.
- **`help`** workload grew: more verbs, plus ASH-143 + ASH-144 expanded
  per-verb schema records. The CLI side grew modestly (336 → 391
  cl100k); the MCP side roughly doubled. The "post Δ vs CLI" of +1613%
  is the right number to read — the MCP envelope still pays tax 2 for
  every new field on every arg on every verb.

## Friction

- `wirecmp -out` writes a single table — it doesn't compose with the
  narrative-and-deltas shape the doc now carries. I hand-merged the
  snapshot into the doc. If we re-run this twice a quarter, that's
  fine. If we re-run it every ship, we should teach `wirecmp` to emit
  the just-the-snapshot subsection and the doc can `<!-- BEGIN/END
  SNAPSHOT -->` it. Premature today.
- The `help` regression is the right call locally (ASH-143 + ASH-144
  both improve agent ergonomics) but it bumped the *aggregate* MCP cost
  metric. If we want the aggregate number to mean what people think it
  means, we need to either (a) drop `help` from the aggregate (it's a
  schema dump, not a typical call), or (b) build a real call-mix
  weighting from `tokens_out_emit` rows in the ledger and weight the
  aggregate by frequency. Both are future work, not ASH-129 scope.

## Suggestions for the next layer

ASH-129 was explicitly scoped to *measure*, not *fix*. The fix shape for
tax 2 needs its own ticket. Two candidate shapes are recorded in the doc:

1. A `--format pretty|json` knob on the ashmcp path so harnesses that
   prefer text get the CLI render inside the JSON envelope.
2. A structured-pretty hybrid: tuple form (`{"cols":[…], "rows":[[…]]}`) for
   repeated records, paying the field-name cost once per call instead of
   once per record.

(1) is dead simple and ships a CLI knob. (2) keeps structured access
intact. They're orthogonal — could land both. I'd file (2) first because
it preserves the protocol-correct shape; (1) is a workaround for
harnesses that just want the prose.

## Ledger evidence

The `wirecmp` run itself shows up in `ash report`:

```
ash report --since 1h | head
```

shows the cluster of MCP-transport calls from the tool. `wirecmp` also
prints a one-line per-fixture summary to stderr while running — useful
for ad-hoc inspection without opening the markdown.

## Files changed

- `docs/mcp/wire-cost.md` — full rewrite with snapshot table, pre/post
  deltas table, "where ASH-124 won" / "where tax 2 still bites" narrative,
  and a reproduce-block.
- `docs/session-notes/2026-05-15-ash-129-wire-cost-remeasure.md` — this
  note.

No code changed.
