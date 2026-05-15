# ASH-123 — MCP wire cost & ledger fidelity

**Task.** Two gaps surfaced shipping ASH-104: (1) the `tokens_out` ledger column for MCP-transport rows undercounts what the harness consumes (daemon tokenizes pretty-rendered text the MCP client never sees), and (2) the wire-savings hypothesis from ASH-104 is unverified. Fix the ledger fidelity, then measure CLI vs MCP cost for canonical read-side verbs.

**Verbs used.** `ash read`, `ash edit`, `ash grep`, `ash find`, `ash test`, `ash git`, `ash report`. Two bash escapes: `go build` (until `ash build` ships) and the `make` invocations.

## What shipped

**Ledger fidelity (two-column model).** Added `tokens_out_emit` and `bytes_out_emit` to the `calls` schema with an idempotent `ALTER TABLE` migration mirroring the ASH-71 / ASH-106 pattern. The two-column choice was the ticket's safer option — it preserves the daemon's pretty-render view (load-bearing for replay determinism per ASH-112) while also recording the per-harness reality.

**Transport-aware daemon.** Added `proto.Request.Transport` (`omitempty`; absent = CLI, `"mcp"` = ashmcp). When transport is `mcp`, the daemon — after encoding the response frame — mirrors the single `rsp.Metrics.BytesOut = len(frame)` mutation that ashmcp performs on its side, then calls a new shared helper `proto.MCPEnvelope(rsp)` to compute the exact JSON envelope ashmcp will emit as `TextContent.text`, and writes `len(envelope)` + cl100k(envelope) to the new columns via `UpdateMCPEmit(rowID, …)`. By construction the daemon's envelope is byte-identical to ashmcp's emit, because the two callers go through the same helper.

**Single source of envelope truth.** `cmd/ashmcp/dispatch.go`'s old inline JSON wrap is gone; `toolResult` is now a thin shim over `proto.MCPEnvelope`. Changing the wire shape (ASH-124 StructuredContent, future `_meta` additions) is one edit, one place.

**Surfaced in report + metrics.** `Totals` gains `TokensOutEmit`, `BytesOutEmit`, `MCPCalls`. `ash report` prints a `mcp emit:` line only when at least one call in the window came over MCP — CLI-only sessions are byte-identical to today's output. `ash metrics` adds an `oE` column to the per-row table, gated the same way (only widens when at least one row has emit data).

**Measurement harness.** New `cmd/wirecmp` opens a UDS connection, dispatches each fixture once with `Transport=mcp` (so the daemon also fills the emit columns), then derives both renderings in-process: CLI = `verbs.PrettyHandlers()[verb](req, rsp)`; MCP = `proto.MCPEnvelope(rsp)`. Both come from the same `*proto.Response` so the comparison isolates transport overhead from verb behavior. `-claude` flag also calls Anthropic `count_tokens` for sign-checked numbers. Median latency over `-repeat` trials per transport.

## Measurement results

[`docs/mcp/wire-cost.md`](../mcp/wire-cost.md) (full table). Headlines:

| fixture | CLI claude | MCP claude | Δ |
|---|---:|---:|---:|
| read README:1-60 | 1221 | 1363 | +11.6% |
| find **/\*.go (20) | 16 | 63 | +294% |
| grep ^func Run | 16 | 63 | +294% |
| stat README.md | 26 | 90 | +246% |
| git status | 135 | 229 | +69.6% |
| help | 399 | 4339 | +987% |
| **totals** | **1813** | **6147** | **+239%** |

**The ASH-104 wire-savings hypothesis ("typed JSON args + no pretty-format overhead beats CLI parsing") is empirically wrong.** Across six canonical read-side intents, MCP costs **≈3.4× more Claude tokens** than the CLI pretty render. The cl100k_base ratio agrees within a tenth of a percent (+240% vs +239%), so this is not a cross-tokenizer artifact. Latency is at parity within noise — the cost is purely in payload shape.

## Why this happens

Three additive taxes:

1. **Envelope wrapper.** `{"ok":true,"data":…,"metrics":…}` is ~30-50 tokens of pure scaffolding per call. Bad for terse responses (find/grep/stat: <50 tokens of payload, the wrapper is the majority of cost).
2. **JSON vs the pretty render.** Pretty output is purpose-built: dense, headerless beyond `§verb:`, no field names. JSON spells out `"col":17,"line":306,"path":"README.md","text":"…"` for every match.
3. **Help is the worst case** — pretty output is a compact list of verbs; the structured `Data` field carries every arg description for every verb. The compact view collapses naturally; the structured view doesn't.

## Implications for follow-ups

- **ASH-124 (StructuredContent + outputSchema) is now the headline mitigation.** It lets ashmcp drop the envelope wrapper for MCP harnesses that support it — `data` flows into `structuredContent`, `ok`/`err`/`metrics` into `_meta`. Estimated savings: the 30-50 token-per-call wrapper plus the field-name JSON tax. Should bring the small-response cases (find/grep/stat) close to parity. The help case is fundamentally an information-density problem — even StructuredContent won't help if the harness wants the schema bodies.
- **The "tokens_out_emit > tokens_out" finding strongly motivates ASH-108 (cache-aware envelope).** With MCP costing 3.4× more, prompt-cache placement matters more there than in CLI; the structure of the envelope deserves cache-stable ordering.
- **Help over MCP needs a separate compact form.** Maybe `ash_help` over MCP returns just verb names + one-line descriptions; the harness pulls full schemas from `tools/list` (which it has already, statically embedded). Worth its own ticket.

## Friction

- **One-place envelope shape was the unlock.** The first design draft had ashmcp computing tokens after emit and writing back to the daemon via a follow-up RPC. That solved the timing problem but introduced two callers of the envelope-building code (the actual emitter and the accountant) that would have to stay in sync forever. Moving the shape into `proto.MCPEnvelope` and having both callers go through it eliminates the drift class entirely.
- **`rsp.Metrics.BytesOut` mirror.** ashmcp mutates `rsp.Metrics.BytesOut = len(buf)` after reading the response frame. The daemon now does the exact same mutation right before computing the envelope, so the embedded `bo` field matches by construction. `LatencySerializeUs` is deliberately *not* mirrored because ashmcp can't see it — keeping both at 0 is the only way to make the two envelopes byte-identical.
- **Drift for streaming responses.** Streaming MCP rows have a small `bo` divergence: ashmcp computes `totalBytes` (sum of all chunk+final frame payloads) while the daemon's `len(final)` is just the final frame. ~1-2 tokens of drift for typical workloads. Not worth fixing for v1 measurement — `wirecmp` doesn't supply a `progressToken`, so all measurement rows are non-streaming. Documenting the gap rather than papering over it.
- **`ash edit` Makefile diff with tabs.** Trying to insert a `wirecmp` target into `Makefile` via `ash edit` failed to match because my heredoc's tab indentation didn't match the file's tabs exactly. Skipped the Makefile target — `go build -o bin/wirecmp ./cmd/wirecmp` works directly. Worth a session-note flag: `ash edit` is brittle against Makefile-shaped content (literal tabs in heredocs are easy to get wrong via shell-quoting). Possible future work: an `ash edit --ignore-whitespace-runs` mode for whitespace-fragile files.

## Instrumentation

The new columns make MCP cost queryable for the first time:

```
ash report --since 1h
```
…now prints a `mcp emit: N calls, tokens_out_emit=…, bytes_out_emit=…` line when any call in the window came over MCP. CLI-only windows leave the line out (no behavior change for today's users). For per-row inspection, `ash metrics --last 20` widens with an `oE` column when MCP rows are present.

Verified the wirecmp run inserted matching emit rows. Run-time `tokens_out_emit` from the ledger matched `wirecmp`'s in-process count exactly for every fixture (both go through `proto.MCPEnvelope`, so this is the single-source-of-truth check we want).

## Out of scope (sibling tickets it leaves open)

- ASH-124 (StructuredContent emit). The data here gives it numbers to beat.
- ASH-108 (cache-aware envelope). Now has empirical motivation: MCP costs 3.4×, cache placement matters proportionally more.
- ASH-127 (truncation_hint via `_meta`). Same envelope refactor, will benefit from the shared helper landing.
- A separate `ash_help` compact MCP form. Worth a ticket — `help` is 10× more expensive than CLI today.
