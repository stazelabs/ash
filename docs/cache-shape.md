# Cache-aware response envelope (ASH-108)

This document is the contract for how `ash` shapes its responses so an
agent's conversation transcript can hit the Anthropic prompt cache. It
is the cache-shape companion to [vocab/inventory.md](vocab/inventory.md):
inventory pins per-token cost on every output literal; this pins where
those literals are *positioned* in the encoded bytes so the cache
prefix matches across consecutive identical calls.

## Why this matters

The Anthropic prompt cache charges **roughly 10× less** on cached input
tokens (5-minute TTL) than on uncached. The 2026-05-13 encoding
measurement also found Claude tokenizes ash output ~19% heavier than
the cl100k_base proxy in the ledger
(see [encoding-results.md](encoding-results.md))
and the 2026-05-15 wire-cost measurement found MCP-emitted envelopes
are ~3.4× the CLI cost
(see [mcp/wire-cost.md](mcp/wire-cost.md)).
Restructuring the response envelope so volatile fields (random request
IDs, per-call timings) ride at the **tail** of the encoded bytes lets
two consecutive identical calls share a long matching prefix that the
cache can latch onto. The win compounds across a conversation:
every grep+read+stat sequence the agent runs becomes mostly-cached
input after the first run.

## The three zones

Every ash response — over every surface — has three zones, in order:

1. **Stable prefix.** Bytes that are identical for two calls with
   identical inputs. The protocol version, the OK flag, and the verb-
   specific result data live here. This is the part the cache latches
   onto.
2. **Bounded-stable middle.** Verb data that depends on the *world*
   (file contents, file lists, git output) but not on the call's
   instrumentation. Stable across calls when the world doesn't change.
3. **Volatile suffix.** Per-call telemetry that *cannot* match across
   calls by construction: the random request ID, latency
   microseconds, tokens-in/-out counts, sub-phase breakdowns. This
   goes at the **end** of the encoded bytes, never in the middle.

If a field is volatile, it must come after every stable field.
A volatile field in the middle of the envelope wrecks the cache
contract — the matching prefix ends at the first volatile byte.

## Field-level audit

The table below classifies every field across the three encoded
surfaces an agent might consume. Fields are listed in the order
they appear in encoded output.

### `proto.Response` (msgpack wire — daemon → client) — internal

| Field     | Zone           | Why                                                       |
|-----------|----------------|-----------------------------------------------------------|
| `V`       | stable prefix  | protocol version constant                                  |
| `OK`      | stable prefix  | per-call outcome, stable for matching inputs               |
| `Data`    | bounded-stable | verb result; deterministic for fixed file state            |
| `Err`     | stable prefix  | error code + msg; stable for matching inputs               |
| `ID`      | volatile suffix| random uint64 per request                                  |
| `Metrics` | volatile suffix| latencies, token counts, ledger errors — vary per call     |

This is the internal UDS frame and not directly agent-visible, but
its struct-field order is the source of truth for the JSON and MCP
surfaces below.

### `jsonResponse` (CLI `--format json` — agent-visible)

| Field     | Zone           | Notes                                                |
|-----------|----------------|------------------------------------------------------|
| `v`       | stable prefix  | mirrors `proto.Response.V`                            |
| `ok`      | stable prefix  |                                                       |
| `data`    | bounded-stable | decoded verb result, not raw bytes                   |
| `err`     | stable prefix  |                                                       |
| `id`      | volatile suffix| at the tail so the JSON prefix matches cross-call     |
| `metrics` | volatile suffix| at the very end                                      |

### MCP `CallToolResult` (ashmcp → harness — agent-visible, primary path)

| Field                          | Zone           | Notes                                                                  |
|--------------------------------|----------------|------------------------------------------------------------------------|
| `Content[].TextContent.Text`   | bounded-stable | `proto.MCPEnvelope(rsp)` — JSON of `rsp.Data` only (no envelope wrap)  |
| `StructuredContent`            | bounded-stable | decoded `rsp.Data` for harnesses that validate against `outputSchema`  |
| `IsError`                      | stable prefix  | mirrors `!rsp.OK`                                                      |
| `Meta["ash"]["metrics"]`       | volatile, OUT  | metrics ride in MCP `_meta`, **not** in model-visible content          |
| `Meta["ash"]["truncated"]`     | volatile, OUT  | truncation hint, also out of model context                             |

The MCP path is the strictest realization of the contract: everything
the model sees is in the bounded-stable middle. Per-call telemetry
goes through `_meta`, which MCP-aware harnesses keep out of the
agent's conversation context. This is the surface most adoption sits
on (ASH-104) and the one prompt caching helps most.

### Pretty CLI (`--format pretty`, the default)

| Stream | Zone           | Notes                                                              |
|--------|----------------|--------------------------------------------------------------------|
| stdout | stable prefix + bounded-stable | verb pretty body — header (`§<verb>: …`) then result rendering   |
| stdout | stable prefix  | error responses are `err <code>\n<msg>` only                       |
| stderr | volatile       | `[ash bi=… bo=… ti=… to=… us=…]` metrics line on the trailing line |

Pretty already keeps timing on stderr; the stdout body is cache-stable
for identical inputs.

## The cache contract

Three rules. Read them as a checklist before touching response shape.

1. **No volatile field in the middle of the envelope.** If a new field
   varies per call (random IDs, wall-clock timestamps, latency
   microseconds), it must come after every stable field in struct
   declaration order. Both `msgpack/v5` and `encoding/json` preserve
   field order, so struct ordering is the wire ordering.
2. **Metrics ride in a separate channel.** For pretty, stderr. For
   MCP, `_meta` (protocol-reserved metadata, out of model context).
   For JSON-formatted CLI output, last in the envelope. The agent
   should never have to read the metrics dict to reach the next
   verb's response.
3. **Verb result data is deterministic for fixed inputs.** If a verb
   reaches into the world it must reach deterministically: sorted
   results, stable ordering, no timestamps inside the data payload.
   Pretty headers (`§<verb>: …`) and ASCII-only literals do not
   change across calls. Anything that *would* change (path-prefix
   tax, truncation hint) is structured (`proto.TruncInfo`) so it can
   move to `_meta` or be omitted.

## Ledger telemetry

The ledger has two columns reserved for prompt-cache accounting:

- `tokens_cache_hit` — Anthropic's `usage.cache_read_input_tokens`
  for the message that included this call.
- `tokens_cache_miss` — `usage.cache_creation_input_tokens` for the
  same message.

Both default to zero. Daemon-originated rows leave them at zero
because the daemon does not see Anthropic API responses. The
columns ride the schema today so a future feedback path (a harness
side annotation via MCP `_meta`, or an `ash usage` verb that the
agent calls retroactively with the numbers it observed) can land
without a schema migration.

When at least one row in the report window has non-zero cache
numbers, `ash report` adds a `cache:` line with the per-window
hit ratio. `ash metrics` widens the per-row table with `ch` / `cm`
columns under the same gate. CLI-only sessions today produce
byte-identical pretty output to the pre-ASH-108 surface.

## Test invariant

`internal/proto/proto_test.go` carries `TestResponse_CacheStableWirePrefix`,
which encodes two `Response` values that differ only in `ID` and
`Metrics` and asserts that the encoded byte sequences share a long
common prefix. Any field reordering that pushes a volatile field
earlier shortens the matching prefix and the test fails — surfacing
the cache-contract break at PR review time.

## Out of scope for ASH-108

- Wire path for harness-reported cache stats. The columns exist; the
  populator does not. Plausible homes: `ashmcp` reading `_meta` from
  the harness's next call, or a dedicated `ash usage` verb the agent
  calls retroactively.
- Measuring the actual cache-hit % on real Claude Code sessions.
  That needs MCP-side hookup plus replay
  ([ASH-112](https://linear.app/stazelabs/issue/ASH-112)) to A/B the
  same task with and without cache-aware envelope. The structural
  shape change is the prerequisite; the empirical scoreboard is the
  next ticket.
- `proto.Metrics.CacheReadTokens` / `CacheCreationTokens` exist on
  the wire so a producer can ship telemetry through `_meta` without
  a protocol revision, but no current code populates them.
