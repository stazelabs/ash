# 2026-05-15 — ASH-108 cache-aware response envelope

## Task

Restructure `ash` responses so the Anthropic prompt cache can latch onto
consecutive identical-input calls. The hypothesis from
[docs/revolutionary-directions.md](../revolutionary-directions.md):
prompt-cache hits charge ~10× less than uncached input, so positioning
volatile per-call fields at the **tail** of every encoded envelope (and
keeping stable verb data in a long matching prefix) is potentially the
biggest single token-cost lever in the project. Wire-level companion:
proto v2 already lives in code (ASH-106 bumped it); ASH-108 is the seam
where the v2 *shape* gets pinned. Ledger companion: reserve
`tokens_cache_hit` / `tokens_cache_miss` columns so a future harness-
side telemetry path can land without a migration.

## Verbs used

- `ash edit` for every in-repo file change (with multi-line `--old/--new`
  string args — held up fine even on the 19-column `metrics` SELECT).
- `ash read`, `ash grep`, `ash find` for navigation.
- `ash write` for `docs/cache-shape.md` and this session note.
- `ash diff` (real call, not the verb code) for the manual cache-prefix
  sanity check after build — two consecutive `ash help --verb read
  --format json` produced byte-identical output through `data`, with the
  only diffs in `id` and `metrics` as designed.
- `make all`, `make vocab schema`, `make vocab-check schema-check`,
  `make validate-check`, `ash test` for the gate sweep.

## What landed

**Envelope reorder for cache-stable prefix.** Reordered
[`proto.Response`](../../internal/proto/proto.go) so stable fields (`V`,
`OK`, `Data`, `Err`) precede the volatile suffix (`ID`, `Metrics`). The
`msgpack/v5` encoder honors struct field order, so the wire shape now
puts everything cacheable at the head. Same reorder applied to
[`jsonResponse`](../../cmd/ash/main.go) — `--format json` output mirrors
the wire shape. CLI pretty was already cache-stable (timing on stderr,
verb body deterministic), so it needed no change.

**Cache-telemetry columns in the ledger.** Added `tokens_cache_hit` and
`tokens_cache_miss` to the `calls` schema, the `Call` struct, both
`QueryWindow`/`QueryRecent` SELECTs and the `Record` INSERT. Idempotent
`ALTER TABLE` migration mirroring the ASH-71 / ASH-106 / ASH-123 pattern.
No populator yet — daemon-originated rows leave both at zero. The
columns ride the schema so a future harness-side feedback path (via MCP
`_meta` passthrough, or an `ash usage` annotation verb) can land
without another migration.

**Surfaced in report and metrics.** `ash report` adds a `cache:` line
with the per-window hit ratio when any row carried non-zero cache
numbers — hidden when all-zero so CLI-only sessions are byte-identical
to today's pretty output. `ash metrics` widens with `ch` / `cm` columns
under the same gate. Compact mode picks up the two extra row positions.

**`proto.Metrics.CacheReadTokens` / `CacheCreationTokens` on the wire.**
Two `omitempty` fields so a producer can ship harness-reported cache
accounting through the existing Response envelope without bumping
protocol versions. No current code populates them; the wire path is in
place for when it does.

**Contract doc.** [docs/cache-shape.md](../cache-shape.md) classifies
every field of `proto.Response`, `jsonResponse`, and the MCP
`CallToolResult` into stable prefix / bounded-stable middle / volatile
suffix; spells out the three rules (no volatile field in the middle,
metrics ride a separate channel, verb data is deterministic for fixed
inputs); documents the ledger-column reservation and the test invariant.
CLAUDE.md "Memory hygiene" gained a pointer.

**Tests.**
- `TestResponse_CacheStableWirePrefix` ([proto_test.go](../../internal/proto/proto_test.go))
  encodes two `Response` values that differ only in `ID` and `Metrics`,
  asserts the encoded byte sequences share a long common prefix
  (≥ 40 bytes on the sample payload, and at least half the envelope),
  with the diverging bytes strictly in the tail.
- `TestResponse_VolatileSuffixOrdering` walks `proto.Response` via
  reflection and asserts `V`/`OK`/`Data`/`Err` all precede `ID`/`Metrics`
  in struct declaration order. Pins the contract at the type level so a
  future reorder fails on the type, not on the encoder.
- `TestRecord_CacheTokens_RoundTrip` ([ledger_test.go](../../internal/ledger/ledger_test.go))
  inserts a row with non-zero cache hit/miss and asserts both query
  paths return them — same schema-INSERT-SELECT round-trip we use for
  every column.

## Friction

- The hook denied harness `Edit`/`Read`/`Write` (per
  `.claude/settings.json`), so every change funneled through `ash edit`.
  Worked smoothly even for 19-column SQL block edits — the `--old @PATH
  --new @PATH` form would have helped for a couple of the longest
  blocks, but inline multi-line `--old`/`--new` strings carried it. No
  ash-bug findings.
- `proto.Metrics` has lots of `omitempty` fields with short msgpack
  tags. Picking `crt` / `cct` for the new cache fields to fit that
  convention; could be revisited if/when a producer arrives and we
  want longer keys.

## Manual sanity check

After rebuilding and restarting the daemon:

```sh
bin/ash help --verb read --format json > /tmp/a.json
bin/ash help --verb read --format json > /tmp/b.json
bin/ash diff --path /tmp/a.json --other /tmp/b.json
```

Produces a clean diff with changes confined to lines 51–62 of a
~65-line JSON envelope: only `id` and the `metrics` block differ. The
preceding 50 lines (everything through the `data` array) are byte-
identical across the two calls — exactly the cache-stable prefix the
contract promises.

## Suggestions / follow-ups (out of scope for ASH-108)

- **Wire the populator.** The columns are reserved; nobody writes them.
  Plausible homes: (a) `ashmcp` reads `cache_read_input_tokens` /
  `cache_creation_input_tokens` out of `_meta` on the next tool call
  and annotates the previous row; (b) an `ash usage` verb the agent
  calls retroactively with numbers it observed in a chat-completion
  response. Worth its own ticket once the harness side stabilizes.
- **Empirical scoreboard.** With [ASH-112](https://linear.app/stazelabs/issue/ASH-112)
  (`ash replay`) shipped, the proper measurement is a replay run before
  and after this ship against real prior sessions, asserting matching
  prefix length is monotonic in the *new* envelope shape. Today
  `TestResponse_CacheStableWirePrefix` is the proxy.
- **Prefix-cache placement in pretty.** The pretty stdout body is
  already cache-stable, but the `§<verb>` header could in principle
  be tightened further. Defer; the entire `metrics_no_equals` /
  `headers_compact` line from
  [cli-tokens.md](../cli-tokens.md) lives in that zone and is its
  own ticket trail.
- **`bench` cache-hit ratio.** Ticket mentions surfacing cache-hit
  ratio per verb in the bench harness. Bench today doesn't see real
  Anthropic responses; until the populator lands, the bench column
  would always be zero. Deferred.
