# wirecmp: CLI vs MCP wire cost

Same intent, two transports. CLI = daemon-pretty render; MCP = JSON envelope ashmcp emits as TextContent. Both renders are computed from a single daemon roundtrip per fixture; latency is the median of `-repeat` trials per transport.

## Post-ASH-124 snapshot

`bin/wirecmp -claude -repeat 5` against this repo on a clean working tree, daemon at HEAD post-ASH-124.

| fixture | CLI bytes | CLI cl100k | CLI claude | MCP bytes | MCP cl100k | MCP claude | Δ bytes | Δ cl100k | Δ claude | CLI p50 | MCP p50 |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| read README:1-60 | 4829 | 1101 | 1221 | 5005 | 1183 | 1317 | +176 (+4%) | +82 (+7%) | +96 (+8%) | 2.6ms | 4.5ms |
| find **/*.go (20) | 41 | 9 | 16 | 38 | 8 | 15 | -3 (-7%) | -1 (-11%) | -1 (-6%) | 1.9ms | 937µs |
| grep ^func Run | 39 | 9 | 16 | 36 | 8 | 15 | -3 (-8%) | -1 (-11%) | -1 (-6%) | 943µs | 719µs |
| stat README.md | 35 | 14 | 26 | 113 | 37 | 48 | +78 (+223%) | +23 (+164%) | +22 (+85%) | 2.5ms | 3.2ms |
| git status | 67 | 20 | 28 | 105 | 29 | 38 | +38 (+57%) | +9 (+45%) | +10 (+36%) | 10.1ms | 7.9ms |
| help | 1614 | 391 | 463 | 29680 | 6939 | 7933 | +28066 (+1739%) | +6548 (+1675%) | +7470 (+1613%) | 1.3ms | 5.8ms |

**Totals** — CLI 6625B / 1544 cl100k, MCP 34977B / 8204 cl100k. Δ +28352B (+428.0%) / +6660 cl100k tokens (+431.3%).
Claude: CLI 1770, MCP 9366, Δ +7596 (+429.2%).

Excluding the `help` outlier: CLI 1307 claude, MCP 1433 claude, Δ +126 (+9.6%) — down from +27.9% pre-ASH-124. The aggregate ratio is hostage to `help`; the rest of the read-side surface is near parity now.

## Pre-vs-post ASH-124 deltas

Pre numbers are the post-ASH-123 snapshot previously committed in this file (commit `f37eb6d`). Same fixtures, same `wirecmp -claude` invocation, daemon before ASH-124 landed.

| fixture | pre MCP claude | post MCP claude | Δ (post − pre) | pre Δ vs CLI | post Δ vs CLI |
|---|---:|---:|---:|---:|---:|
| read README:1-60 | 1363 | 1317 | -46 (-3%) | +12% | +8% |
| find **/*.go (20) | 63 | 15 | -48 (-76%) | +294% | -6% |
| grep ^func Run | 63 | 15 | -48 (-76%) | +294% | -6% |
| stat README.md | 90 | 48 | -42 (-47%) | +246% | +85% |
| git status† | 229 | 38 | -191 (-83%) | +70% | +36% |
| help‡ | 4339 | 7933 | +3594 (+83%) | +987% | +1613% |

† `git status` workload changed between runs: the pre-ASH-124 run captured a dirty working tree (Phase 2 work in flight); the post-ASH-124 run was clean. Most of the row's drop reflects fewer status entries, not envelope shrink. The `pre Δ vs CLI` / `post Δ vs CLI` columns are the load-bearing ones — both sides shrank, so the *ratio* still tells the protocol story.

‡ `help` workload grew: 14 verbs in ASH-123 → 20 verbs now, and ASH-143 (PH placeholder surfaced in `writeArg`) + ASH-144 (`Long` descriptions wired through the msgpack tag) both expanded the per-verb schema record. The CLI render also grew (336 → 391 cl100k), but the JSON dump ~doubled because every verb now ships every arg's full `Long` description in the structured payload. This is "scope creep, not regression" — but it's also the clearest single signal that tax 2 (JSON-vs-pretty) is the load-bearing one for any verb that emits structured records.

## Where ASH-124 won

Tax 1 (the `{"ok":...,"data":...,"metrics":...}` envelope wrapper, ~30–50 tokens per call) and tax 3 (metrics counted against tokens) closed cleanly on every fixture they touched:

- **Terse-response fixtures (`find`, `grep`)** flipped from costing ~4× the CLI to being *slightly cheaper*. `{"records":[],"count":0}` is one Claude token shorter than the CLI's `§grep: 0 match(es) (0 file(s) scanned)` pretty header. The MCP transport now has a small structural advantage on empty/near-empty results.
- **`stat README.md`** dropped from +246% to +85% Δ claude. The named-field cost (`{"path":"…","size":…,"mtime":…,...}`) is still there — but the wrapper tax it used to sit inside is gone.
- **`read README:1-60`** dropped from +12% to +8% Δ claude. Small because the wrapper was always a fixed cost dwarfed by the file body; the residual gap is the `{"path":"…","content":"…","encoding":"utf-8",...}` framing around the bytes.
- **Aggregate excluding `help`**: +27.9% → +9.6% Δ claude.

## Where tax 2 still bites

Tax 2 — JSON-vs-pretty, with field names spelled out for every record — is untouched by ASH-124. Two shapes pay it:

1. **Schema-dump verbs (`help`).** Every verb ships every arg's full structured form (`name`, `type`, `default`, `description`, `Long`, `PH`, `mode`, `op`, `repeats`). The CLI render collapses this to `--name:type[!|=default] — description`. Result: 17× the Claude tokens over MCP. This is the worst-case row in the table.

2. **List-of-records verbs (`stat`, `find --meta true`, future `lang outline` / `lang refs`, etc.).** The named-field tax scales linearly with record count. `stat` is the canonical small case (+85% on a one-record response); a 100-record `find --meta` would land much worse.

Closing this is its own design problem — out of scope for ASH-129. Two candidate shapes:

- A `--format pretty|json` knob on the ashmcp path that ships the daemon-pretty text inside the JSON envelope for harnesses that prefer it. Loses structured access but matches CLI cost.
- A structured-pretty hybrid: compact tuple form (e.g. `{"cols":["path","size","mtime"],"rows":[[…],[…]]}`) for repeated records. Keeps structured access; pays the field-name cost once per call instead of once per record.

## Method

`bin/wirecmp -claude -repeat 5` against the local daemon. Per fixture: one canonical roundtrip whose `Response` feeds *both* renderings (CLI = `verbs.PrettyHandlers()[verb](req, rsp)`; MCP = `proto.MCPEnvelope(rsp)`) so the comparison isolates transport overhead from verb behavior; five additional roundtrips per transport for the latency median. Tokenizers: `cl100k_base` for the local proxy, Anthropic `count_tokens` against `claude-sonnet-4-5` for ground truth — they agree within 0.5% at aggregate (+431% vs +429%).

Reproduce:

```sh
make all && go build -o bin/wirecmp ./cmd/wirecmp
set -a; . ./.env.local; set +a
bin/wirecmp -claude -out docs/mcp/wire-cost.snapshot.md
```

The snapshot table is hand-merged into this doc rather than overwritten in place, because the doc also carries the pre/post deltas and the narrative — which `wirecmp` itself doesn't emit.
