# wirecmp: CLI vs MCP wire cost

Same intent, two transports. CLI = daemon-pretty render; MCP = the bytes ashmcp emits as TextContent. Both renders are computed from a single daemon roundtrip per fixture; latency is the median of `-repeat` trials per transport.

> **Post-ASH-156 wire shape.** json-mode success no longer emits a TextContent JSON fallback — the verb's typed payload rides as StructuredContent only. The MCP column below is *only* TextContent (empty for non-truncated json-mode success, the ASH-127 sentinel for truncated rows, the short prose envelope on errors, the daemon-pretty render in `format=pretty` mode). The bytes the model still sees via StructuredContent are out of frame for this measurement; the column reflects ashmcp's `tokens_out_emit` accounting by construction.

> **Correction (ASH-148):** All `find` and `grep` rows in the post-ASH-123, post-ASH-124, and post-ASH-147 snapshots below were measuring the `args: limit must be a positive integer` error envelope, not real verb output. wirecmp's fixtures passed numeric args as Go `int`, which msgpack-encodes as positive fixints; the daemon decoded those to `uint8`, which `argutil.ToInt` does not currently accept (it handles `int`/`int64`/`uint64`/`float64`/`string`). Result: `--limit 20` and `--max 20` were rejected, and both CLI and MCP rendered the error. The "find and grep are cheaper over MCP than CLI" claim in earlier wire-cost narrative was an artifact of that — the bug rendered short on both sides. The post-ASH-148 snapshot below uses string-typed args (matching what the CLI's `parseFlags` produces, and what ashmcp's `decodeArgs` produces via `json.Unmarshal`), so it's the first real comparison for those rows.
>
> Daemon paths in production (CLI → ashd, ashmcp → ashd) are unaffected: both send strings or JSON-decoded `float64`s, never raw Go `int`s. The hardening of `argutil.ToInt` to accept the full set of msgpack integer types is tracked separately in [ASH-149](https://linear.app/stazelabs/issue/ASH-149).

## Latest snapshot (post-ASH-156)

`bin/wirecmp -claude -repeat 5` against the daemon at HEAD post-ASH-156. The MCP column models the TextContent ashmcp emits — empty for non-truncated json-mode success, the ASH-127 sentinel for truncated rows. Numeric args use string types (ASH-148 fixture correction).

| fixture | CLI bytes | CLI cl100k | CLI claude | MCP bytes | MCP cl100k | MCP claude | Δ bytes | Δ cl100k | Δ claude | CLI p50 | MCP p50 |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| read README:1-60 | 4877 | 1115 | 1231 | 0 | 0 | 0 | -4877 (-100%) | -1115 (-100%) | -1231 (-100%) | 2.0ms | 2.1ms |
| find **/*.go (20)‡ | 557 | 200 | 254 | 85 | 24 | 32 | -472 (-85%) | -176 (-88%) | -222 (-87%) | 1.7ms | 1.5ms |
| find **/*.go --meta (20)‡ | 923 | 420 | 494 | 85 | 24 | 32 | -838 (-91%) | -396 (-94%) | -462 (-94%) | 2.8ms | 1.6ms |
| grep ^func Run‡ | 2317 | 757 | 941 | 85 | 24 | 32 | -2232 (-96%) | -733 (-97%) | -909 (-97%) | 8.1ms | 7.6ms |
| stat README.md | 35 | 14 | 26 | 0 | 0 | 0 | -35 (-100%) | -14 (-100%) | -26 (-100%) | 1.2ms | 1.6ms |
| git status† | 266 | 94 | 116 | 0 | 0 | 0 | -266 (-100%) | -94 (-100%) | -116 (-100%) | 9.1ms | 7.8ms |
| help | 1693 | 410 | 485 | 0 | 0 | 0 | -1693 (-100%) | -410 (-100%) | -485 (-100%) | 1.8ms | 1.5ms |

**Totals** — CLI 10668B / 3010 cl100k, MCP 255B / 72 cl100k. Δ -10413B (-97.6%) / -2938 cl100k tokens (-97.6%).
Claude: CLI 3547, MCP 96, Δ -3451 (-97.3%).

The non-zero MCP rows (find / grep) are entirely the ASH-127 truncation sentinel that fires because the fixtures call `limit=20` against `max=20`. Non-truncated calls go to zero TextContent — the model still sees the verb's typed payload via StructuredContent, but ashmcp pays nothing in the TextContent channel that `tokens_out_emit` counts. Non-zero "Claude" values for sentinel-only rows come from `count_tokens` against the 85-byte sentinel string; empty-content rows record 0 because the Anthropic API rejects an empty user message, and an empty payload's token cost is 0 by construction anyway.

The `format=pretty` opt-in (ASH-146) is unchanged by this work — it already shipped single-emit TextContent. See the historical post-ASH-146 snapshot below for that column.

## Historical snapshot (pre-ASH-156)

Two snapshots, same fixtures: the default JSON-envelope MCP shape (pre-ASH-146 baseline; what a harness sees when it does not pass `format`) and the `format=pretty` opt-in (ASH-146; what a harness sees when it sets the MCP-only `format` knob to "pretty"). Both `bin/wirecmp -claude -repeat 5` against the daemon at HEAD post-ASH-146 with the post-ASH-148 fixture corrections in place.

### Default JSON envelope (pre-ASH-146 baseline; format unset or "json")

| fixture | CLI bytes | CLI cl100k | CLI claude | MCP bytes | MCP cl100k | MCP claude | Δ bytes | Δ cl100k | Δ claude | CLI p50 | MCP p50 |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| read README:1-60 | 4829 | 1101 | 1221 | 5005 | 1183 | 1317 | +176 (+4%) | +82 (+7%) | +96 (+8%) | 2.2ms | 2.9ms |
| find **/*.go (20) | 548 | 195 | 250 | 1922 | 638 | 733 | +1374 (+251%) | +443 (+227%) | +483 (+193%) | 1.9ms | 1.8ms |
| find **/*.go --meta (20) | 913 | 415 | 490 | 1922 | 638 | 733 | +1009 (+111%) | +223 (+54%) | +243 (+50%) | 1.8ms | 1.9ms |
| grep ^func Run | 2317 | 757 | 941 | 2861 | 905 | 1072 | +544 (+23%) | +148 (+20%) | +131 (+14%) | 8.2ms | 8.4ms |
| stat README.md | 35 | 14 | 26 | 113 | 37 | 48 | +78 (+223%) | +23 (+164%) | +22 (+85%) | 1.7ms | 2.2ms |
| git status† | 324 | 108 | 142 | 552 | 155 | 199 | +228 (+70%) | +47 (+44%) | +57 (+40%) | 10.0ms | 8.9ms |
| help | 1614 | 391 | 463 | 17505 | 4076 | 4696 | +15891 (+985%) | +3685 (+942%) | +4233 (+914%) | 1.6ms | 5.4ms |

**Totals** — CLI 10580B / 2981 cl100k, MCP 29880B / 7632 cl100k. Δ +19300B (+182.4%) / +4651 cl100k tokens (+156.0%).
Claude: CLI 3533, MCP 8798, Δ +5265 (+149.0%).

### `format=pretty` opt-in (ASH-146)

`bin/wirecmp -claude -repeat 5 -pretty` — same fixtures, but the MCP column is now the daemon-pretty render ashmcp surfaces inside TextContent when the harness sets `format: "pretty"`. By construction this is byte-identical to the CLI render except for the ASH-127 truncation sentinel, which still rides as a separate TextContent block on truncated calls.

| fixture | CLI bytes | CLI cl100k | CLI claude | MCP bytes | MCP cl100k | MCP claude | Δ bytes | Δ cl100k | Δ claude | CLI p50 | MCP p50 |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| read README:1-60 | 4829 | 1101 | 1221 | 4829 | 1101 | 1221 | +0 (+0%) | +0 (+0%) | +0 (+0%) | 2.4ms | 3.6ms |
| find **/*.go (20)‡ | 548 | 195 | 250 | 633 | 219 | 276 | +85 (+16%) | +24 (+12%) | +26 (+10%) | 1.8ms | 1.9ms |
| find **/*.go --meta (20)‡ | 913 | 415 | 490 | 998 | 439 | 516 | +85 (+9%) | +24 (+6%) | +26 (+5%) | 2.8ms | 1.6ms |
| grep ^func Run‡ | 2317 | 757 | 941 | 2402 | 781 | 967 | +85 (+4%) | +24 (+3%) | +26 (+3%) | 9.7ms | 8.6ms |
| stat README.md | 35 | 14 | 26 | 35 | 14 | 26 | +0 (+0%) | +0 (+0%) | +0 (+0%) | 2.8ms | 3.2ms |
| git status† | 324 | 108 | 142 | 324 | 108 | 142 | +0 (+0%) | +0 (+0%) | +0 (+0%) | 11.8ms | 9.9ms |
| help | 1614 | 391 | 463 | 1614 | 391 | 463 | +0 (+0%) | +0 (+0%) | +0 (+0%) | 1.4ms | 4.7ms |

**Totals** — CLI 10580B / 2981 cl100k, MCP 10835B / 3053 cl100k. Δ +255B (+2.4%) / +72 cl100k tokens (+2.4%).
Claude: CLI 3533, MCP 3611, Δ +78 (+2.2%).

### What the opt-in buys (per-fixture Claude-token deltas)

| fixture | json claude | pretty claude | drop | ratio |
|---|---:|---:|---:|---:|
| help | 4696 | 463 | -4233 | -90% |
| find **/*.go (20) | 733 | 276 | -457 | -62% |
| find **/*.go --meta (20) | 733 | 516 | -217 | -30% |
| grep ^func Run | 1072 | 967 | -105 | -10% |
| stat README.md | 48 | 26 | -22 | -46% |
| git status | 199 | 142 | -57 | -29% |
| read README:1-60 | 1317 | 1221 | -96 | -7% |

Aggregate Claude tokens drop from 8798 (json envelope, +149% vs CLI) to 3611 (pretty, +2.2% vs CLI). The remaining +2.2% is the truncation sentinel that fires on `find`/`grep` because the fixtures pass `limit=20` and `max=20` is the hard cap; non-truncated calls match CLI cost exactly.

† `git status` workload is sensitive to tree state. The two snapshots above were taken from the same in-flight ASH-146 tree, so the row is comparable across them; the absolute byte count is not directly comparable to earlier snapshots in this doc.

‡ The `find`/`grep` Δ over CLI in pretty mode (+85 bytes, +26 Claude tokens) is the ASH-127 truncation sentinel that ashmcp prepends as a separate TextContent block when the verb hit `limit==max`. The CLI does not emit this sentinel because the truncation hint is already inside the pretty body. Closing this gap further is its own design problem — folding the sentinel into the body would change the body shape, which a separate ticket can pick up if the +26 tokens per truncated call matters at scale.

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

## How ASH-146 closed tax 2

Tax 2 — JSON-vs-pretty, with field names spelled out for every record — was the load-bearing remaining cost after ASH-124, ASH-147, and ASH-148. ASH-148 quantified it: ~10× the Claude tokens for schema-dump verbs (`help`), ~12–24 Claude tokens per record for list-of-records verbs (`find --meta` over the Go tree would cost ~2400 extra tokens, `lang outline`/`lang refs` would land in the same shape).

ASH-146 added a transport-level `format` knob to every MCP tool's input schema (Option 1 from the design ticket). Harnesses that pass `format: "pretty"` receive the daemon-pretty render — the exact text the CLI prints — as the sole TextContent. Structured access is dropped in that mode (the JSON/pretty divergence would be a footgun); harnesses that need programmatic field access keep the default `format: "json"` and pay the existing structured-record cost.

The numbers (from the snapshots above):

- **Aggregate Claude tokens drop from 8798 → 3611** (+149% → +2.2% vs CLI; -59% absolute).
- **Help drops 4233 → 463 Claude** (-90%); the ~10× schema-dump cost is fully eliminated for opt-in callers.
- **Stat drops 48 → 26 Claude** (-46%); the named-field framing is gone.
- **Find/grep retain a ~26-token sentinel cost** when truncated (the ASH-127 hint ashmcp prepends as a separate TextContent), so they sit a few percent above CLI rather than at parity. Non-truncated calls match CLI exactly.

What ASH-146 *did not* attack: harnesses that need both structured access *and* CLI-equivalent cost on list-of-records verbs (Option 2 from the original design — the `{"cols":[…],"rows":[[…],[…]]}` compact-tuple hybrid). That tradeoff is captured in [ASH-153](https://linear.app/stazelabs/issue/ASH-153) — keep Option 2 on the shelf for when a harness lands that genuinely wants both, instead of speculatively widening the surface now.

## Method

`bin/wirecmp -claude -repeat 5` against the local daemon. Per fixture: one canonical roundtrip whose `Response` feeds *both* renderings (CLI = `verbs.PrettyHandlers()[verb](req, rsp)`; MCP = `proto.MCPEnvelope(rsp)` by default, `cliText` under `-pretty`) so the comparison isolates transport overhead from verb behavior; five additional roundtrips per transport for the latency median. Tokenizers: `cl100k_base` for the local proxy, Anthropic `count_tokens` against `claude-sonnet-4-5` for ground truth — they agree within 7% at aggregate (post-ASH-146 default snapshot: +156.0% cl100k vs +149.0% Claude; the spread comes from JSON-formatting tokens like quotes and commas, which cl100k counts more aggressively than Claude's tokenizer).

Reproduce both snapshots:

```sh
make all && go build -o bin/wirecmp ./cmd/wirecmp
set -a; . ./.env.local; set +a
bin/ash help --verb help >/dev/null  # auto-start the daemon
bin/wirecmp -claude -out docs/mcp/wire-cost.snapshot.md            # default JSON envelope
bin/wirecmp -claude -pretty -out docs/mcp/wire-cost.snapshot-pretty.md  # format=pretty
```

The snapshot table is hand-merged into this doc rather than overwritten in place, because the doc also carries the pre/post deltas and the narrative — which `wirecmp` itself doesn't emit.

Numeric args in `cmd/wirecmp/main.go` fixtures are passed as strings (`"limit": "20"`, not `20`) to match how the CLI's `parseFlags` and ashmcp's `decodeArgs` produce them in production. See the correction note at the top of this doc.
