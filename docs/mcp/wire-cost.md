# wirecmp: CLI vs MCP wire cost

Same intent, two transports. CLI = daemon-pretty render; MCP = JSON envelope ashmcp emits as TextContent. Both renders are computed from a single daemon roundtrip per fixture; latency is the median of `-repeat` trials per transport.

## Latest snapshot (post-ASH-147)

`bin/wirecmp -claude -repeat 5` against this repo, daemon at HEAD post-ASH-147 (which trimmed `Long` from the default `help` MCP envelope).

| fixture | CLI bytes | CLI cl100k | CLI claude | MCP bytes | MCP cl100k | MCP claude | Δ bytes | Δ cl100k | Δ claude | CLI p50 | MCP p50 |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| read README:1-60 | 4829 | 1101 | 1221 | 5005 | 1183 | 1317 | +176 (+4%) | +82 (+7%) | +96 (+8%) | 2.1ms | 3.8ms |
| find **/*.go (20) | 41 | 9 | 16 | 38 | 8 | 15 | -3 (-7%) | -1 (-11%) | -1 (-6%) | 826µs | 703µs |
| grep ^func Run | 39 | 9 | 16 | 36 | 8 | 15 | -3 (-8%) | -1 (-11%) | -1 (-6%) | 591µs | 401µs |
| stat README.md | 35 | 14 | 26 | 113 | 37 | 48 | +78 (+223%) | +23 (+164%) | +22 (+85%) | 1.7ms | 2.9ms |
| git status† | 140 | 42 | 60 | 228 | 61 | 82 | +88 (+63%) | +19 (+45%) | +22 (+37%) | 10.0ms | 8.4ms |
| help | 1614 | 391 | 463 | 17505 | 4076 | 4696 | +15891 (+985%) | +3685 (+942%) | +4233 (+914%) | 1.4ms | 4.8ms |

**Totals** — CLI 6698B / 1566 cl100k, MCP 22925B / 5373 cl100k. Δ +16227B (+242.3%) / +3807 cl100k tokens (+243.1%).
Claude: CLI 1802, MCP 6173, Δ +4371 (+242.6%).

Excluding the `help` outlier: CLI 1339 claude, MCP 1477 claude, Δ +138 (+10.3%) — within rounding of the post-ASH-124 +9.6%. ASH-147 did not touch the non-`help` rows; the small drift is the `git status` workload change documented under † below.

† This run's `git status` captured a dirty tree (ASH-147's own in-flight edits to `internal/verbs/help/help.go` + `internal/verbs/help/help_test.go`). The `Δ vs CLI` *ratio* (+37%) is the load-bearing comparison — both sides see the same workload, so envelope overhead is comparable.

## Pre-vs-post ASH-147 deltas (`help`)

ASH-147 gated `ArgSchema.Long` on `Args.Verbose` at `help.Run`, so the default (non-verbose) `help` call no longer ships per-arg long descriptions over the wire. Every other fixture is unaffected; only the `help` row changes.

| metric | post-ASH-124 | post-ASH-147 | Δ |
|---|---:|---:|---:|
| `help` MCP bytes | 29680 | 17505 | -12175 (-41%) |
| `help` MCP cl100k | 6939 | 4076 | -2863 (-41%) |
| `help` MCP claude | 7933 | 4696 | -3237 (-41%) |
| `help` Δ vs CLI (claude) | +1613% | +914% | from 17× to 10× |
| aggregate MCP claude | 9366 | 6173 | -3193 (-34%) |
| aggregate Δ vs CLI (claude) | +429% | +243% | |

The `--verbose true` path is preserved: a caller that explicitly asks for it (CLI `ash help --verb edit --verbose true`, or an MCP harness setting `verbose: true`) still gets every `Long` description on the wire and the CLI renderer surfaces them. ASH-144's regression test (`TestVerboseSurfacesLong`) was strengthened to assert the wire-shape gate, not just the pretty-render gate.

## Pre-vs-post ASH-124 deltas

Pre numbers are the post-ASH-123 snapshot previously committed in this file (commit `f37eb6d`). Same fixtures, same `wirecmp -claude` invocation, daemon before ASH-124 landed.

| fixture | pre MCP claude | post MCP claude | Δ (post − pre) | pre Δ vs CLI | post Δ vs CLI |
|---|---:|---:|---:|---:|---:|
| read README:1-60 | 1363 | 1317 | -46 (-3%) | +12% | +8% |
| find **/*.go (20) | 63 | 15 | -48 (-76%) | +294% | -6% |
| grep ^func Run | 63 | 15 | -48 (-76%) | +294% | -6% |
| stat README.md | 90 | 48 | -42 (-47%) | +246% | +85% |
| git status‡ | 229 | 38 | -191 (-83%) | +70% | +36% |
| help§ | 4339 | 7933 | +3594 (+83%) | +987% | +1613% |

‡ `git status` workload changed between runs: the pre-ASH-124 run captured a dirty working tree (Phase 2 work in flight); the post-ASH-124 run was clean. Most of the row's drop reflects fewer status entries, not envelope shrink. The `pre Δ vs CLI` / `post Δ vs CLI` columns are the load-bearing ones — both sides shrank, so the *ratio* still tells the protocol story.

§ `help` workload grew across ASH-123 → ASH-124: 14 verbs → 20 verbs, and ASH-143 (PH placeholder surfaced in `writeArg`) + ASH-144 (`Long` descriptions wired through the msgpack tag) both expanded the per-verb schema record. ASH-147 above is the targeted fix that took the inflated `help` row back down.

## Where ASH-124 won

Tax 1 (the `{"ok":...,"data":...,"metrics":...}` envelope wrapper, ~30–50 tokens per call) and tax 3 (metrics counted against tokens) closed cleanly on every fixture they touched:

- **Terse-response fixtures (`find`, `grep`)** flipped from costing ~4× the CLI to being *slightly cheaper*. `{"records":[],"count":0}` is one Claude token shorter than the CLI's `§grep: 0 match(es) (0 file(s) scanned)` pretty header. The MCP transport now has a small structural advantage on empty/near-empty results.
- **`stat README.md`** dropped from +246% to +85% Δ claude. The named-field cost (`{"path":"…","size":…,"mtime":…,...}`) is still there — but the wrapper tax it used to sit inside is gone.
- **`read README:1-60`** dropped from +12% to +8% Δ claude. Small because the wrapper was always a fixed cost dwarfed by the file body; the residual gap is the `{"path":"…","content":"…","encoding":"utf-8",...}` framing around the bytes.
- **`help` (post-ASH-147)** dropped from +1613% to +914% Δ claude — a targeted projection rather than transport-level work. The structural cost of dumping the full `help.Registry()` over JSON remains, but the `Long`-description tax is now gated on opt-in.
- **Aggregate excluding `help`**: +27.9% → +9.6% → +10.3% (the last drift is the git status workload, not protocol).

## Where tax 2 still bites

Tax 2 — JSON-vs-pretty, with field names spelled out for every record — is untouched by ASH-124 or ASH-147. Two shapes pay it:

1. **Schema-dump verbs (`help`).** Even after ASH-147 stripped `Long`, every verb still ships every arg's structured form (`name`, `type`, `default`, `description`, `PH`, `mode`, `op`, `values`). The CLI render collapses this to `--name:type[!|=default] — description`. Result: still ~10× the Claude tokens over MCP. ASH-147 was a 41% bite out of the worst offender; the underlying linear-per-field cost is what's left.

2. **List-of-records verbs (`stat`, `find --meta true`, future `lang outline` / `lang refs`, etc.).** The named-field tax scales linearly with record count. `stat` is the canonical small case (+85% on a one-record response); a 100-record `find --meta` would land much worse — measured in [ASH-148](https://linear.app/stazelabs/issue/ASH-148).

Closing this is its own design problem — tracked in [ASH-146](https://linear.app/stazelabs/issue/ASH-146). Two candidate shapes:

- A `--format pretty|json` knob on the ashmcp path that ships the daemon-pretty text inside the JSON envelope for harnesses that prefer it. Loses structured access but matches CLI cost.
- A structured-pretty hybrid: compact tuple form (e.g. `{"cols":["path","size","mtime"],"rows":[[…],[…]]}`) for repeated records. Keeps structured access; pays the field-name cost once per call instead of once per record.

## Method

`bin/wirecmp -claude -repeat 5` against the local daemon. Per fixture: one canonical roundtrip whose `Response` feeds *both* renderings (CLI = `verbs.PrettyHandlers()[verb](req, rsp)`; MCP = `proto.MCPEnvelope(rsp)`) so the comparison isolates transport overhead from verb behavior; five additional roundtrips per transport for the latency median. Tokenizers: `cl100k_base` for the local proxy, Anthropic `count_tokens` against `claude-sonnet-4-5` for ground truth — they agree within 0.5% at aggregate (post-ASH-147: +243.1% cl100k vs +242.6% Claude).

Reproduce:

```sh
make all && go build -o bin/wirecmp ./cmd/wirecmp
set -a; . ./.env.local; set +a
bin/ash help --verb help >/dev/null  # auto-start the daemon
bin/wirecmp -claude -out docs/mcp/wire-cost.snapshot.md
```

The snapshot table is hand-merged into this doc rather than overwritten in place, because the doc also carries the pre/post deltas and the narrative — which `wirecmp` itself doesn't emit.
