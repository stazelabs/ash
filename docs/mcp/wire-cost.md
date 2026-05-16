# wirecmp: CLI vs MCP wire cost

Same intent, two transports. CLI = daemon-pretty render; MCP = JSON envelope ashmcp emits as TextContent. Both renders are computed from a single daemon roundtrip per fixture; latency is the median of `-repeat` trials per transport.

> **Correction (ASH-148):** All `find` and `grep` rows in the post-ASH-123, post-ASH-124, and post-ASH-147 snapshots below were measuring the `args: limit must be a positive integer` error envelope, not real verb output. wirecmp's fixtures passed numeric args as Go `int`, which msgpack-encodes as positive fixints; the daemon decoded those to `uint8`, which `argutil.ToInt` does not currently accept (it handles `int`/`int64`/`uint64`/`float64`/`string`). Result: `--limit 20` and `--max 20` were rejected, and both CLI and MCP rendered the error. The "find and grep are cheaper over MCP than CLI" claim in earlier wire-cost narrative was an artifact of that — the bug rendered short on both sides. The post-ASH-148 snapshot below uses string-typed args (matching what the CLI's `parseFlags` produces, and what ashmcp's `decodeArgs` produces via `json.Unmarshal`), so it's the first real comparison for those rows.
>
> Daemon paths in production (CLI → ashd, ashmcp → ashd) are unaffected: both send strings or JSON-decoded `float64`s, never raw Go `int`s. The hardening of `argutil.ToInt` to accept the full set of msgpack integer types is tracked separately in [ASH-149](https://linear.app/stazelabs/issue/ASH-149).

## Latest snapshot (post-ASH-148)

`bin/wirecmp -claude -repeat 5` against this repo, daemon at HEAD post-ASH-147 with fixtures fixed to pass numeric args as strings.

| fixture | CLI bytes | CLI cl100k | CLI claude | MCP bytes | MCP cl100k | MCP claude | Δ bytes | Δ cl100k | Δ claude | CLI p50 | MCP p50 |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| read README:1-60 | 4829 | 1101 | 1221 | 5005 | 1183 | 1317 | +176 (+4%) | +82 (+7%) | +96 (+8%) | 2.4ms | 4.0ms |
| find **/*.go (20) | 548 | 195 | 250 | 1920 | 638 | 733 | +1372 (+250%) | +443 (+227%) | +483 (+193%) | 2.0ms | 1.9ms |
| find **/*.go --meta (20) | 911 | 415 | 490 | 1920 | 638 | 733 | +1009 (+111%) | +223 (+54%) | +243 (+50%) | 2.4ms | 2.5ms |
| grep ^func Run | 2317 | 757 | 941 | 2861 | 905 | 1072 | +544 (+23%) | +148 (+20%) | +131 (+14%) | 8.7ms | 8.6ms |
| stat README.md | 35 | 14 | 26 | 113 | 37 | 48 | +78 (+223%) | +23 (+164%) | +22 (+85%) | 1.3ms | 1.7ms |
| git status† | 137 | 46 | 65 | 204 | 58 | 80 | +67 (+49%) | +12 (+26%) | +15 (+23%) | 9.3ms | 8.4ms |
| help | 1614 | 391 | 463 | 17505 | 4076 | 4696 | +15891 (+985%) | +3685 (+942%) | +4233 (+914%) | 2.1ms | 4.9ms |

**Totals** — CLI 10391B / 2919 cl100k, MCP 29528B / 7535 cl100k. Δ +19137B (+184.2%) / +4616 cl100k tokens (+158.1%).
Claude: CLI 3456, MCP 8679, Δ +5223 (+151.1%).

Excluding the `help` outlier: CLI 2993 claude, MCP 3983 claude, Δ +990 (+33%) — the read-side surface (find, grep, stat, git, read) costs ~1/3 more over MCP than CLI. Earlier snapshots reported ~+10% here, but those numbers were dominated by find+grep returning empty error envelopes; the real number is +33%.

† `git status` workload is sensitive to tree state. This run captured the post-ASH-147 dirty tree (in-flight ASH-148 edits to `cmd/wirecmp/main.go`). The `Δ vs CLI` ratio is the load-bearing comparison.

## Linear-scaling check on list-of-records verbs (ASH-148)

`find **/*.go (20)` and `find **/*.go --meta (20)` walk the same 20 records over MCP. The MCP envelopes are byte-identical (1920B / 733 claude) because the structured Record always carries `path`, `type`, `size`, `mtime` regardless of `meta`. Only the CLI render differs.

| metric | stat (1 record) | find (20 records) | per-record cost |
|---|---:|---:|---:|
| MCP claude − CLI claude | +22 | +243 (--meta on) | ~12 claude/record |
| MCP claude − CLI claude | +22 | +483 (--meta off) | ~24 claude/record |

The linear-scaling prediction from post-ASH-129 holds. The 12–24 Claude tokens per record is the tax 2 cost ([ASH-146](https://linear.app/stazelabs/issue/ASH-146)): structural framing (`{"path":"…","type":"file","size":…,"mtime":…},`) per record, paid every time. CLI sidesteps it with a single-line-per-record pretty form; MCP pays the named-field overhead linearly.

When `meta=true` matches between CLI and MCP, the gap shrinks substantially (+50% vs +193%) because both transports ship the same density of data — but MCP still pays for the quote-marks-and-commas framing.

## Pre-vs-post ASH-148 corrections (`find` and `grep` rows)

The numbers in this column are the values *previously claimed* in this doc through the post-ASH-147 snapshot. They were measuring error envelopes, not real verb output. Anything outside `find` / `grep` is unaffected.

| fixture | pre-correction MCP claude | post-correction MCP claude | shape of pre-number |
|---|---:|---:|---|
| find **/*.go (20) | 15 | 733 | error envelope JSON |
| find **/*.go --meta (20) | (not measured) | 733 | new fixture |
| grep ^func Run | 15 | 1072 | error envelope JSON |

The pre-ASH-148 numbers should not be quoted in future analyses of MCP/CLI parity. The post-ASH-147 wins for find/grep ("MCP is cheaper than CLI for terse responses!") were illusory — both transports rendered the same error short.

## Pre-vs-post ASH-147 deltas (`help`)

ASH-147 gated `ArgSchema.Long` on `Args.Verbose` at `help.Run`, so the default (non-verbose) `help` call no longer ships per-arg long descriptions over the wire. The `help` row is the only one ASH-147 touched; this measurement is unaffected by the ASH-148 fixture-bug correction.

| metric | post-ASH-124 | post-ASH-147 | Δ |
|---|---:|---:|---:|
| `help` MCP bytes | 29680 | 17505 | -12175 (-41%) |
| `help` MCP cl100k | 6939 | 4076 | -2863 (-41%) |
| `help` MCP claude | 7933 | 4696 | -3237 (-41%) |
| `help` Δ vs CLI (claude) | +1613% | +914% | from 17× to 10× |

The `--verbose true` path is preserved: a caller that explicitly asks for it (CLI `ash help --verb edit --verbose true`, or an MCP harness setting `verbose: true`) still gets every `Long` description on the wire and the CLI renderer surfaces them. The strengthened `TestVerboseSurfacesLong` pins the wire shape against future regressions.

## Where ASH-124 won

Tax 1 (the `{"ok":...,"data":...,"metrics":...}` envelope wrapper, ~30–50 tokens per call) and tax 3 (metrics counted against tokens) closed cleanly on every fixture they touched:

- **`stat README.md`** dropped from +246% to +85% Δ claude. The named-field cost (`{"path":"…","size":…,"mtime":…,...}`) is still there — but the wrapper tax it used to sit inside is gone.
- **`read README:1-60`** dropped from +12% to +8% Δ claude. Small because the wrapper was always a fixed cost dwarfed by the file body; the residual gap is the `{"path":"…","content":"…","encoding":"utf-8",...}` framing around the bytes.
- **`help` (post-ASH-147)** dropped from +1613% to +914% Δ claude — a targeted projection rather than transport-level work.
- **`git status`** dropped from +70% to +23% Δ claude. (Workload-dependent; numbers compare like-for-like on small status outputs.)
- **(find, grep)** post-ASH-148: the pre-ASH-124 numbers for these rows were measuring error envelopes (same wirecmp fixture bug as today). The closed-tax-1 win on terse responses claimed in earlier text was not real — those rows weren't measuring real verb output until ASH-148 fixed the fixtures.

## Where tax 2 still bites

Tax 2 — JSON-vs-pretty, with field names spelled out for every record — is untouched by ASH-124, ASH-147, or ASH-148. ASH-148 confirmed the prediction with measurement:

1. **Schema-dump verbs (`help`).** Even after ASH-147 stripped `Long`, every verb still ships every arg's structured form (`name`, `type`, `default`, `description`, `PH`, `mode`, `op`, `values`). Result: ~10× the Claude tokens over MCP.
2. **List-of-records verbs.** Confirmed at ~12–24 Claude tokens per record (above). `find --meta` over the full Go tree (~200 files) would cost ~2400 extra Claude tokens via MCP vs CLI's pretty form. `lang outline` / `lang refs` over a workspace would land in the same shape.

Closing this is its own design problem — tracked in [ASH-146](https://linear.app/stazelabs/issue/ASH-146). Two candidate shapes:

- A `--format pretty|json` knob on the ashmcp path that ships the daemon-pretty text inside the JSON envelope for harnesses that prefer it. Loses structured access but matches CLI cost.
- A structured-pretty hybrid: compact tuple form (e.g. `{"cols":["path","size","mtime"],"rows":[[…],[…]]}`) for repeated records. Keeps structured access; pays the field-name cost once per call instead of once per record. The post-ASH-148 per-record cost (~12 claude/record) is the design target this would attack.

## Method

`bin/wirecmp -claude -repeat 5` against the local daemon. Per fixture: one canonical roundtrip whose `Response` feeds *both* renderings (CLI = `verbs.PrettyHandlers()[verb](req, rsp)`; MCP = `proto.MCPEnvelope(rsp)`) so the comparison isolates transport overhead from verb behavior; five additional roundtrips per transport for the latency median. Tokenizers: `cl100k_base` for the local proxy, Anthropic `count_tokens` against `claude-sonnet-4-5` for ground truth — they agree within 7% at aggregate post-ASH-148 (+158.1% cl100k vs +151.1% Claude; the spread comes from JSON-formatting tokens like quotes and commas, which cl100k counts more aggressively than Claude's tokenizer).

Reproduce:

```sh
make all && go build -o bin/wirecmp ./cmd/wirecmp
set -a; . ./.env.local; set +a
bin/ash help --verb help >/dev/null  # auto-start the daemon
bin/wirecmp -claude -out docs/mcp/wire-cost.snapshot.md
```

The snapshot table is hand-merged into this doc rather than overwritten in place, because the doc also carries the pre/post deltas and the narrative — which `wirecmp` itself doesn't emit.

Numeric args in `cmd/wirecmp/main.go` fixtures are passed as strings (`"limit": "20"`, not `20`) to match how the CLI's `parseFlags` and ashmcp's `decodeArgs` produce them in production. See the correction note at the top of this doc.
