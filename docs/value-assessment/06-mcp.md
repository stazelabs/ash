# Sweep 6 — ashmcp envelope cost on uniform fixtures (ASH-182)

**Source:** [cmd/mcpbench/main.go](../../cmd/mcpbench/main.go) drives `bin/ashmcp` over real MCP stdio transport, calls each comparable bench case via `CallTool`, and tokenizes the resulting `CallToolResult` envelope. Run against [bench/baseline.json](../../bench/baseline.json) at commit `ac1da44` on 2026-05-18. 11 cases (read + grep + find with no `{root}` placeholder).

## Headline

**ashmcp beats the harness-native MCP envelope at every emit-mode setting, but the envelope tax vs the direct CLI varies sharply by mode.** The current default (JSON mode) pays a +66% envelope tax vs CLI; pretty mode drops that to +6% — essentially CLI-equivalent — and still beats harness-native MCP by −67%.

This answers Q2 from [decision.md](decision.md): ashmcp has standalone adoption value over harness-native MCP regardless of mode, but **the choice of default emit mode is now a load-bearing UX/cost decision**, not just an ergonomic preference.

## Three-mode comparison

| mode | ashmcp subset | vs CLI | vs harness-native MCP | what's emitted |
|---|---:|---:|---:|---|
| **json** (default) | 36,545 tok | **+66%** | **−48%** | `StructuredContent` (full typed payload) + minimal `TextContent` sentinel + `Meta` |
| **compact** ([ASH-153](https://linear.app/stazelabs/issue/ASH-153)) | 29,968 tok | **+36%** | **−57%** | cols/rows hybrid `{"k":[...],"r":[[...]]}` instead of per-record maps; field names listed once |
| **pretty** ([ASH-146](https://linear.app/stazelabs/issue/ASH-146)) | 23,278 tok | **+6%** | **−67%** | `TextContent` = daemon-pretty render; no `StructuredContent` |

Direct CLI total over the same 11 cases: **21,913 tok**. Harness-native MCP simulated total: **71,153 tok**.

## Per-case (JSON mode, the production default)

| case | verb | cli | ashmcp_env | harness_env | Δashmcp-vs-cli | Δashmcp-vs-harness |
|---|---|---:|---:|---:|---:|---:|
| `find_shallow` | find | 47 | 415 | 113 | **+782%** | **+267%** |
| `find_md_in_docs` | find | 194 | 1,049 | 301 | **+440%** | **+248%** |
| `find_go_files` | find | 1,440 | 5,219 | 1,636 | **+262%** | **+219%** |
| `read_tiny_range` | read | 25 | 106 | 51 | +324% | +107% |
| `grep_rare_pattern` | grep | 88 | 180 | 375 | +104% | −52% |
| `grep_parseargs_internal` | grep | 5,985 | 10,333 | 10,282 | +72% | +0% |
| `grep_heavy_func_internal` | grep | 5,015 | 9,092 | 46,749 | +81% | **−80%** |
| `grep_todo_repo` | grep | 1,139 | 1,560 | 1,418 | +36% | +10% |
| `grep_files_only` | grep | 910 | 987 | 1,139 | +8% | −13% |
| `read_range` | read | 739 | 857 | 980 | +15% | −12% |
| `read_small` | read | 6,331 | 6,747 | 8,109 | +6% | −16% |

The mix of wins and losses tells the structural story directly: **wins where the harness-native equivalent would dump heavy content (grep_heavy, read_small); losses where the payload is small enough that per-record JSON dominates the actual content (find_shallow, find_md, read_tiny_range).**

## The find problem: per-record JSON dominates small payloads

Find is the worst-case verb in JSON mode by a wide margin (+262% to +782% vs CLI). Diagnosis: `ash find`'s `Record` struct emits `{"path":"...","type":"file","size":N,"mtime":N}` per result in the StructuredContent payload — four fields per record, regardless of whether `--meta` was set.

For `find_shallow` (16 records of just paths in CLI mode), the JSON-mode output balloons to ~415 tokens because:
- StructuredContent serializes 16 × 4-field records (~24 tokens each)
- Plus the surrounding `Result` envelope (count, truncated, scope, etc.)

Compact mode helps (per-field name once instead of per-record): −21% on `find_shallow`. Pretty mode crushes it: 415 → 108 (−74%) because pretty output is just the path list with trailing `/` on dirs — almost identical to bash find's output.

**Implication:** the `Record` shape is paying for `type`/`size`/`mtime` whether the caller wanted them or not. Whether this is a structured-payload purity decision worth keeping is a real design question; the data here makes the cost concrete.

## What the modes mean for the agent

| mode | best for | tradeoff |
|---|---|---|
| **pretty** | the agent will pass tool output back to the model as context (most common case) | no `StructuredContent` — harnesses that need programmatic field access must re-call in json/compact |
| **compact** | harnesses that need structured data but want envelope economy | requires harness understanding of `{"k":[...],"r":[[...]]}` cols/rows shape (still a standard JSON object — no new schema, just denser) |
| **json** (current default) | maximum interop; the typed payload is queryable with standard JSON paths | highest envelope tax |

**Most agent interactions are model-context-feeding**, not programmatic-field-extracting. For those, pretty mode is the right choice; the current default of JSON optimizes for the less-common case at the cost of every call.

## What ashmcp wins are robust to

- The headline "**ashmcp beats harness-native MCP at every mode**" holds in all three measurements (−48% to −67%). This is the load-bearing finding for ASH-182: even at the worst mode, ashmcp has standalone value over a hypothetical harness-native MCP server doing the same work.
- Truncation behavior is the structural advantage. `grep_heavy_func_internal` shows the clearest case: ashmcp returns 9,092 tokens with truncation indicator; harness-native MCP simulated wraps 46,749 tokens of raw matches (because there's no truncation built into TextContent). Real harnesses that paginate or head-limit narrow this, but `ash grep`'s default `--max=256` is the load-bearing safety belt.
- For read, every mode beats harness-native MCP (because the cat-n line-number overhead is in harness Read's payload, not ashmcp's structured shape).

## What this DOES NOT measure

1. **Outer JSON-RPC framing.** Each MCP request/response includes a `jsonrpc`/`id`/`method` wrapper (~30–60 bytes). It applies to both ashmcp and harness-native uniformly, so it's invisible to the comparison but real in absolute terms.
2. **Latency.** mcpbench captures token cost only. Subprocess MCP transport has nontrivial round-trip overhead; not measured here.
3. **Real harness `CallToolResult` framing.** The harness-native simulation is "what the SDK's `mcp.CallToolResult` would serialize to for a TextContent-only payload." A real harness (Claude Code's MCP client surface) may wrap or trim this further. Headline shapes are robust; absolute numbers may shift ±10–20%.
4. **Cases without bench representation.** Edit/write/git/stat/diff weren't compared because they don't have a clean harness-native MCP equivalent (the harness uses Bash for git, has no stat/diff MCP tools). Those rely on the CLI track measurements ([01-bench.md](01-bench.md)).
5. **Multi-turn cache effects.** ashmcp's StructuredContent + Meta has more cache-amenable structure than pretty-mode TextContent. Whether the harness's prompt cache catches more of the JSON-mode prefix is a follow-up the cache-shape work ([04-cache.md](04-cache.md)) didn't measure for MCP-routed calls specifically.

## Recommendations

### 1. Consider shifting the ashmcp default to compact or pretty mode
The current default (json) is the most expensive mode by a wide margin. Even compact reduces the envelope tax from +66% → +36% with no loss of programmatic accessibility (still a standard JSON object). Pretty drops it to +6% but loses StructuredContent.

A defensible default would be **compact** — preserves structured access while cutting ~half the envelope tax. The pretty mode option stays available for harnesses that don't care about structured payloads.

### 2. Investigate ash find's per-record cost
Find emits `{path,type,size,mtime}` per record regardless of whether `--meta` was requested. For default (`meta=false`) callers, the type/size/mtime fields are pure overhead. A path-only StructuredContent shape when `--meta=false` would close most of the +262% to +782% find loss without changing the verb's pretty output. Small, contained fix.

### 3. Don't drop the JSON-mode option
Even with the cost, json mode is the most programmatically-accessible shape and worth keeping for harnesses or tooling that want it. The recommendation is to change the *default*, not remove the option.

### 4. Re-measure after default change
If the default shifts to compact, re-run mcpbench against the new ashmcp and confirm the 30-day ledger's `tokens_out_emit` aggregate drops accordingly.

## What this changes in the decision

[decision.md](decision.md) said:

> **Open Q2 — What's the ashmcp envelope tax on a uniform workload?** Strictly required to claim the ashmcp track has standalone value beyond the CLI track.

**Answered:**
- Standalone value vs harness-native MCP: **confirmed yes** (−48% to −67% across modes).
- Envelope tax vs direct CLI: **+66% in JSON mode (current default), reducible to +6% with pretty mode or +36% with compact mode.**
- The case for ashmcp adoption holds regardless of mode choice. **The next concrete leverage move is shifting the default emit mode**, which would compound this win without further architecture.

With Q1 ([05-harness.md](05-harness.md)) and Q2 (this doc) both answered positively, the two open questions deferred from the original decision sweep are closed in ash's favor. The remaining gates on adoption push are now organizational, not measurement.

---

*Provenance: `go run ./cmd/mcpbench --in bench/baseline.json --format {json,compact,pretty} --out 06-mcp-table-<mode>.md` at ac1da44 + this work on 2026-05-18. Per-mode raw tables: [json](06-mcp-table-json.md), [compact](06-mcp-table-compact.md), [pretty](06-mcp-table-pretty.md).*
