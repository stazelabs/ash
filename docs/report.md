# `ash report` — ledger synthesis verb (design)

## Context

The ash ledger captures a rich per-call record at [internal/ledger/ledger.go:25-47](../internal/ledger/ledger.go#L25-L47) — 20 columns covering session, args (msgpack), three latency phases, three sub-phases (walk/io/regex), tokens in/out, bytes in/out, truncation, error code/msg. But the only query surface today is `ash metrics`, which returns a flat "last N rows" table — no aggregation, percentiles, time windows, error grouping, or arg decoding.

Session notes (ship 1, 2, 4) repeatedly point at synthesis the ledger *could* answer but `ash metrics` *can't*: per-verb p95 latency, truncation rates, sub-phase attribution (the "blame tiktoken cold-start" misattribution from ship 4), token-efficiency baselines, error hotspots. Ship 1's note: "ledger is the substrate for the recursive-development experiment. If a session feels heavy or surprising, query the ledger first."

We're adding a sibling verb `ash report` that does in-daemon aggregation across calls. Per-verb summary first; richer dimensions deferred to follow-up Linear tickets so the first cut stays small and dogfoods cleanly.

This stays inside the ash surface (recursive dogfooding) rather than a standalone Go binary — same precedent as `ash metrics` (ASH-1).

## Approach

New verb `ash report` that mirrors the `metrics` package layout and dispatch wiring. Daemon-side aggregation (Go in-memory percentile/sort over rows pulled from SQLite) — the dataset is small enough that we don't need windowed SQL.

### Args (first cut)

| flag | type | default | meaning |
|---|---|---|---|
| `--session` | string | `current` | `current` (daemon's session ID), `all`, or an explicit session ID |
| `--since` | duration | unset | window filter on `ts`, e.g. `15m`, `1h`, `24h`, `7d` |
| `--last` | int | unset | row-count cap (applied after session/since filters); max 5000 |
| `--verb` | string | unset | restrict aggregation to one verb (rarely used at first; included for symmetry) |

Defaults: `--session=current` with no other filters → "everything in the live daemon session". Sane mid-session glance.

### Output (pretty)

```
§report: session abc12345 — 28 calls, 26.4s exec
totals: ok=27/28 (96%), tokens_in=312, tokens_out=8412

verb     n   ok%   p50_exec  p95_exec  p50_out  p95_out  trunc%
read     8   100   142us     1812us    412      1024     0
find     12  100   2401us    8120us    284      1812     17
grep     6    83   4012us    12104us   1402     4980     0
metrics  2   100   38us      52us      210      210      0
```

JSON form: `Result{ Scope: {...filters...}, Totals: {...}, ByVerb: []VerbStats{...} }` with the verb stats struct carrying `Verb, N, OK, Errors, P50ExecUs, P95ExecUs, P50TokensOut, P95TokensOut, TruncatedCount, TruncatedPct, ...`.

### Aggregation math

- p50 / p95: sort the int64 slice, pick `slice[idx]` with `idx = floor(p * (n-1))`. Trivial in Go; unit-testable.
- truncation pct: `truncated_count / n` (rounded).
- ok pct: `ok_count / n`.
- `totals.exec_wall` is sum of `latency_exec_us` across all rows (not real wall, but a useful aggregate).

## Critical files

**New:**

- [internal/verbs/report/report.go](../internal/verbs/report/report.go) — verb body. Mirror [internal/verbs/metrics/metrics.go](../internal/verbs/metrics/metrics.go): `Args`, `ParseArgs`, `RunWithLedger(led, a)`, `Result`, `PrettyResponse`, `decodeResult` (for client-side rendering when data arrives as `map[string]any`). Reuse `metrics.toInt` / `toInt64` helpers — extract them into a small shared pkg only if duplication starts to hurt; otherwise just copy (3 lines).
- [internal/verbs/report/report_test.go](../internal/verbs/report/report_test.go) — table tests for percentile math, scope-filter resolution (current/all/explicit session, --since parse), pretty rendering shape.

**Edit:**

- [internal/ledger/ledger.go](../internal/ledger/ledger.go) — add `QueryWindow(opts QueryOpts) ([]Call, error)` alongside `QueryRecent`. Single SQL with `WHERE` clauses composed from non-zero opts (`session_id = ?`, `ts >= ?`, `verb = ?`) and `ORDER BY id DESC LIMIT ?` (default 5000). Keep `QueryRecent` untouched — `metrics` still uses it.
- [internal/verbs/verbs.go:39-48](../internal/verbs/verbs.go#L39-L48) and [internal/verbs/verbs.go:53-128](../internal/verbs/verbs.go#L53-L128) — register `report` in `PrettyHandlers()` and `Runners()`. Mirror the `metrics` entry exactly (no `Truncated` field; no tracer use).
- [internal/verbs/help/help.go:99-105](../internal/verbs/help/help.go#L99-L105) — add the `report` schema block right after `metrics` so `ash help --verb report` works.
- [CLAUDE.md](../CLAUDE.md) — add `ash report` to the "Live verbs" list under Phase 1; one-line summary plus a "use this instead of `ash metrics --last 200 | …` when you want aggregates" hint.
- [README.md](../README.md) — update the verb roster if it enumerates verbs (one-line entry).

**Untouched:** `proto.Metrics`, ledger schema, all existing verbs. No migration.

## Deferred — Linear tickets to file (team `ash`)

After plan approval and merge of the first cut, file three follow-up issues so the deferred dimensions don't get lost:

1. **`ash report`: sub-phase attribution per verb.** Show `walk_us / io_us / regex_us / serialize_us` as % of `exec_us` per verb. Directly addresses misattribution (the cold-start-blame story from ship 4 — see ASH-4).
2. **`ash report`: truncation & error hotspots.** Top-N truncated calls (decoded args summary), error-code histogram with sample messages, surfaces friction the agent may not have noticed.
3. **`ash report`: token efficiency & arg distributions.** `tokens_out` per result-unit (per match for grep, per file for find), and decoded-args views (most-used globs/paths/patterns). Requires per-verb `args_msgpack` decoders — highest impl cost; do last.

Each ticket should reference this design doc and the relevant session-note section.

## Verification

1. `make` (or the two `go build` calls) — both binaries clean.
2. `go test ./internal/verbs/report/... ./internal/ledger/...` — unit tests green.
3. End-to-end smoke from a clean `.ash/`:
   - `rm -rf .ash bin && make`
   - Run a varied workload: `bin/ash find --path . --glob '**/*.go'`, `bin/ash grep --pattern TODO --path .`, `bin/ash read --path README.md`, repeat to seed ~10 calls.
   - `bin/ash report` — confirm per-verb table, percentiles, ok%/trunc% all populated and plausible.
   - `bin/ash report --since 1h --verb find` — confirm filters compose.
   - `bin/ash report --session all` — confirms cross-session view (only one session here, but exercises the path).
   - `bin/ash report --format json | jq .` — confirms wire shape decodes cleanly.
   - `bin/ash help --verb report` — schema entry visible.
4. Self-recursion check: `bin/ash report` itself appears as a row in subsequent reports (sanity that it goes through the ledger).
5. Promote any first-use friction into the [CLAUDE.md Gotchas section](../CLAUDE.md) or this doc, per the [session-feedback ritual](../CLAUDE.md).
