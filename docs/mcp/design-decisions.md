# MCP adapter — design decisions

Companion to:

- [docs/mcp/tools.json](tools.json) — generated tool schemas (the wire contract).
- [docs/mcp/wire-cost.md](wire-cost.md) — per-fixture CLI vs MCP cost table.
- [docs/cache-shape.md](../cache-shape.md) — envelope cache-prefix contract.

This doc captures *why* `ashmcp` looks the way it does — the load-bearing
design choices and the operational findings that shaped them.

## v1 surface scope

The eight read-side verbs: `ash_read`, `ash_find`, `ash_grep`, `ash_stat`,
`ash_git`, `ash_report`, `ash_metrics`, `ash_help`. Writes (`write`,
`edit`, `diff`) deferred to phase 2 per the ASH-104 ticket.

**`ash_test` is *not* in v1 despite being read-only-ish.** Three reasons
to wait for phase 2:

1. `test` invokes `go test` — mutates the build cache, can write coverage
   files, runs arbitrary test code. "Presumed safe" is a different
   category from "cannot touch state." Mixing categories in v1 muddies
   the contract that made `readSideVerbs` easy to reason about.
2. `test` has the most verbose, structurally-repetitive output we ship
   (per-package pass/fail/skip + `file:line`). Without measuring it
   under `cmd/wirecmp` first, exposing it over MCP risks landing the
   worst-case wire cost in a heavily-used surface.
3. Streaming for `test` is gated on the MCP client supplying a
   `progressToken` (ASH-106). Harnesses that don't take the cumulative
   path — one huge frame — which is exactly the failure mode ASH-123
   warned about.

Phase-2 revisit checklist: add `test` to `readSideVerbs` (or rename — it
becomes a misnomer once writers join); add a `wirecmp` fixture; confirm
Claude Code's MCP client sends a `progressToken` for `tools/call ash_test`;
update [adoption/claude-code.md](../adoption/claude-code.md) and
[adoption/claude-desktop.md](../adoption/claude-desktop.md).

## Architecture

**`cmd/ashmcp/` is a thin adapter, not a re-implementation.** Three files:

- `main.go` — entry, server setup, schema bootstrap.
- `dispatch.go` — per-call UDS roundtrip, args decoding, response shaping.
- `daemon.go` — dial-or-start (copy of the same logic in `cmd/ash/main.go`).

It dispatches to the same `ashd` over the same per-project UDS and lands
in the same ledger. Ledger rows from MCP calls are indistinguishable
from CLI rows by row shape — a load-bearing property for the recursive-
development thesis (MCP adoption immediately produces real-session
ledger data without a schema change).

**`daemon.go` duplicates `cmd/ash/main.go` dial-or-start logic** (~120 LOC
including `killStaleIfNeeded`, `findAshd`, `tailLog`, `isConnRefused`,
`isENOENT`). Kept on purpose per CLAUDE.md "no abstractions beyond what
the task requires." Extract to `internal/daemonclient/` when a third
caller appears — not before. Worth knowing if you touch either copy:
they must stay in sync.

**`//go:embed tools.json` cannot reach outside the package directory.**
Resolution: the canonical artifact lives at `docs/mcp/tools.json`, with
a byte-identical copy at `cmd/ashmcp/tools.json`. `make schema` writes
both; `make schema-check` verifies both. Alternative (gitignore the embed
copy + generate at build time) was rejected because it would break
`go test ./...` from a fresh clone before `make schema` runs.

**`proto.MCPEnvelope` is the single source of envelope truth.** Both
ashmcp (for emit) and ashd (for ledger accounting via `tokens_out_emit`)
call it. Changing the wire shape is one edit, one place. The first design
draft had ashmcp compute tokens after emit and write back via a follow-up
RPC; rejected because it would have introduced two callers of envelope-
building code that had to stay in sync forever.

## Wire shape (post-ASH-156)

**Success (json mode, default).** `CallToolResult` carries:

- `StructuredContent` — `rsp.Data` decoded into a generic object. Every
  verb's `Result` marshals to a top-level object, which is what the MCP
  spec wants here.
- `Content` — empty array. The TextContent JSON fallback shipped by
  ASH-124 was retired in ASH-156 after ASH-130's smoke-test settled
  that no shipping harness benefits from dual-emit (Claude Code drops
  TextContent when StructuredContent is present; Claude Desktop
  consumes both and pays double).
- `Meta = {"ash": {"metrics": ...}}` — metrics ride as MCP-reserved
  metadata. Cooperating harnesses don't count it against the model.

**Success (pretty mode, ASH-146 opt-in).** Same as before — single
TextContent block with the daemon-pretty render, no StructuredContent.

**Errors.** `IsError=true` + `TextContent("<code>: <msg>")`. No
StructuredContent on the error path.

**Truncation** (ASH-127). Two-channel surfacing because some harnesses
ignore `_meta`:

- **Channel A — `_meta.ash.truncated`**: structured `{trunc, limit, max}`
  hint. Machine-readable; doesn't count against the model.
- **Channel B — sentinel `TextContent`** prepended to the response. Two
  phrasings:
  - `truncated: hit limit=L (max=M) — narrow the call or raise the verb's
    limit flag` when `Limit < Max`.
  - `truncated: hit hard cap (max=M) — narrow the call; raising the
    limit will not help` when `Limit == Max`.

The hard-cap phrasing matters: a harness reading just the sentinel must
not retry with `--max=higher` on a call that already saturated the cap.
`IsError` stays `false` — truncation is partial success, not failure.

`proto.MCPTruncationHint` and `proto.MCPTruncationSentinel` are the
shared verb-agnostic probes; ashmcp uses them for emission, ashd uses
them so `tokens_out_emit` accounting stays byte-equal to what the
harness consumes. Drift here breaks the fidelity invariant.

**outputSchema** is generated by `internal/mcpschema/output.go`: AST-walk
each `internal/verbs/<pkg>/`, collect every `type X struct`, recursively
turn the `Result` struct into JSON Schema draft 2020-12. `omitempty` on
a msgpack tag drops the field from `required[]`; `msgpack:"-"` skips it
entirely. Field descriptions come from the trailing `// comment`
(preferred) or the doc comment above. Same pattern as `cmd/ashvocab`.

One hand-baked external reference: `proto.TruncInfo`. When a verb
references another external type, expand `externalSchemas` or add a
cross-package resolver. Current state is intentional — the universe is
small enough that a resolver costs more than it saves.

## Ledger fidelity — the two-column model (ASH-123)

The original `tokens_out` counts the daemon's pretty render — load-bearing
for replay determinism (per ASH-112), but undercounts what an MCP harness
actually consumes because the harness never sees the pretty form.

The two-column resolution:

- `tokens_out` — pretty-render view, unchanged. Replay-deterministic.
- `tokens_out_emit` — what the harness consumes as TextContent. On the
  CLI it equals `tokens_out` (same pretty render). On MCP it depends on
  shape: pretty mode tokenizes the daemon-pretty render (`prettyRsp`);
  errors tokenize the `"<code>: <msg>"` envelope; json-mode success
  collapses to just the ASH-127 truncation sentinel (and reads `0` when
  the verb did not truncate) because the JSON body now rides as
  StructuredContent only (ASH-156). The accounting follows ashmcp's
  actual emit shape by construction.

Same shape: `bytes_out` (daemon-side bytes) + `bytes_out_emit`
(harness-side bytes). `ash report` adds a `mcp emit:` line and `ash
metrics` widens with an `oE` column **only when at least one row in the
window came over MCP** — CLI-only sessions are byte-identical to today's
output.

`proto.Request.Transport` is the discriminator (`omitempty`; absent =
CLI, `"mcp"` = ashmcp). The daemon mutates `rsp.Metrics.BytesOut =
len(frame)` post-encode when `Transport=mcp`, then computes
`proto.MCPEnvelope(rsp)` so the embedded `bo` matches the envelope by
construction. `LatencySerializeUs` is *not* mirrored — ashmcp can't see
it, and keeping both at zero is the only way to make the two envelopes
byte-identical.

**Known drift for streaming MCP rows:** ashmcp computes
`totalBytes = sum(chunk frames + final frame)` while the daemon's
`len(final)` is just the final frame. ~1-2 tokens of drift for typical
workloads. Not fixed for v1 — `wirecmp` doesn't supply a `progressToken`,
so all measurement rows are non-streaming. Documented rather than
papered over.

## The ASH-123 finding & the ASH-148 correction

**Pre-ASH-124 baseline:** MCP costs ~3.4× more Claude tokens than the
CLI pretty render across canonical read-side intents. Three additive
taxes: envelope wrapper (~30-50 tokens), JSON field-name overhead, and
`help` (structured schema body vs compact verb list).

ASH-124 dropped the envelope wrapper. Then **ASH-148 found a long-standing
wirecmp fixture bug** that invalidated every prior find/grep number in
`docs/mcp/wire-cost.md`:

Wirecmp's fixtures built `map[string]any{"limit": 20}` with a Go-native
`int`. msgpack encodes positive ints 0-127 as a single-byte positive
fixint; the daemon decoded that into `uint8(20)` when target is `any`.
`argutil.ToInt` only matches `int`, `int64`, `uint64`, `float64`, `string`
— so the limit was rejected with `args: limit must be a positive integer`.
Every "find / grep" row in three wire-cost snapshots was measuring the
error envelope, not real verb work.

**Why production was fine:** CLI's `parseFlags` produces string args;
ashmcp's `decodeArgs` produces float64 via `json.Unmarshal`. Only
callers that build requests programmatically in Go and pass to msgpack
were exposed (wirecmp, plus future internal tools or replay sessions
decoding ledger args).

Post-fix numbers:

- Real read-side aggregate Δ vs CLI (excl. help): **+33%** (was reported
  as +10%).
- Per-record cost: **~12 Claude tokens/record for `find --meta`**,
  **~24 tokens/record for `find` no-meta**. (Without meta the CLI is
  leaner and MCP's structured cost shows up more sharply.)
- `help` still ~10× more expensive on MCP — fundamentally an
  information-density problem (compact list vs full per-verb schema).

**ASH-149** is the real fix: widen `argutil.ToInt` to handle `int8`/
`int16`/`int32`/`uint`/`uint8`/`uint16`/`uint32`/`float32`, add round-
trip tests through msgpack, then revert the wirecmp string-arg workaround.

**Lesson worth remembering:** the wirecmp summary line in stderr
(`cli=41B/9t mcp=38B/8t`) *looked* plausible for "small response, small
envelope" for three updates. Nobody (including me three commits ago)
bothered to print the actual rendered text. Trust *content*, not byte
counts, on small payloads.

## Configuration gotcha — `.mcp.json` doesn't expand env vars

A locally-authored `.mcp.json` with:

```json
{"mcpServers": {"ash": {"command": "${CLAUDE_PROJECT_DIR}/bin/ashmcp"}}}
```

fails with `ENOENT: posix_spawn '${CLAUDE_PROJECT_DIR}/bin/ashmcp'`.

**Claude Code does not expand environment variables in the `command`
field of `.mcp.json`.** `${CLAUDE_PROJECT_DIR}` is a hook-context-only
variable injected by the PreToolUse runtime — it is not part of the MCP
server spawn environment.

Fix: use an absolute path. `.mcp.json` then stays per-checkout-machine
(untracked). [docs/adoption/claude-code.md](../adoption/claude-code.md)
should grow a "Dogfooding inside the ash repo" subsection covering
project-scope `.mcp.json` with this trap called out explicitly.

A checked-in `.mcp.json` template would either be wrong for every
machine but its author, or would need Makefile rendering. Worth a design
moment before any "let's ship a `.mcp.json` template" reflex.

## ASH-130: structuredContent smoke-test findings

ASH-124's dual-emit (`StructuredContent` + a `TextContent` carrying the
same JSON bytes) was shipped as defense-in-depth: if either target
harness ignored `StructuredContent`, the fallback was load-bearing.
ASH-130 was the verification step.

**Claude Code (verified in-session, v2.x with model claude-opus-4-7).**
Forwards `structuredContent` to the model and **drops `content[].text`**
when both are present. Confirmed directly by running `ash_stat` with
`format=json` (dual-emit) vs `format=pretty` (single TextContent, no
StructuredContent) and observing that the model sees exactly one copy
of the payload in each case — JSON-shaped in json mode, pretty text in
pretty mode. Corroborated by upstream issues
[claude-code#9962](https://github.com/anthropics/claude-code/issues/9962),
[#55677](https://github.com/anthropics/claude-code/issues/55677), and
[#15412](https://github.com/anthropics/claude-code/issues/15412), all
closed *not planned* — the structuredContent-wins behavior is
intentional, not a bug.

**Claude Desktop (community evidence, not directly tested).** Live
Claude Desktop testing was out of reach from this session. Public
reports for claude.ai web — which underlies Claude Desktop — say the
model receives **both** `structuredContent` and `content[].text` when a
tool returns both. If true, dual-emit on Claude Desktop doesn't just
add dead wire bytes — it **doubles the model-visible token cost** of
every json-mode tool result.

**Decision matrix** (from ASH-130's verification block):

| harness | StructuredContent consumed? | TextContent consumed? | dual-emit effect |
|---|---|---|---|
| Claude Code 2.x | yes | no (dropped) | dead wire bytes; tokens_out_emit overcounts |
| Claude Desktop / claude.ai | yes | yes (community) | ~2× model tokens per json-mode call |

Neither harness consumes *only* TextContent — the fallback's original
defense-in-depth purpose does not fire. Per ASH-130's decision rule,
this triggers the single-emit follow-up ([ASH-156](https://linear.app/stazelabs/issue/ASH-156)).

**Caveats the follow-up must handle.**

- MCP spec 2025-06-18 says a tool that returns structured content
  *SHOULD* also return the serialized JSON in a TextContent block for
  backwards compatibility. Dropping it is a `SHOULD` violation; the
  practical question is who, outside Claude Code + Claude Desktop,
  still depends on the fallback. Cursor / Cline / third-party harnesses
  are explicitly out of scope here (ASH-130).
- `CallToolResult.content` is a required field but allows an empty
  array. Single-emit can ship `Content: []mcp.Content{}` in json mode
  without violating the wire format. Truncation sentinels (when
  present) still ride as separate TextContent blocks — the ASH-127
  surfacing is unaffected.
- `tokens_out_emit` accounting (ASH-123) currently models dual-emit
  bytes. The follow-up must update `proto.MCPEnvelope` or the daemon-
  side accounting in lockstep, otherwise the ledger's emit column will
  drift from what the harness actually consumes.
- `format=pretty` mode is already single-emit and unaffected.

## Open follow-ups

- **ASH-146 (tax-2 closure).** With per-record cost pinned at ~12 Claude
  tokens (find --meta), `--format pretty|json` or structured-pretty
  tuple form have a concrete target to beat.
- **ASH-149 (`argutil.ToInt` widening).** Revert the wirecmp string-arg
  workaround once shipped.
- **MCP-truncated row flag in the ledger.** `tokens_out_emit` counts the
  sentinel bytes already, but a `bool` for "this MCP response was
  truncated" would let `ash report` answer "what % of MCP grep calls
  truncated vs CLI grep calls" — closer alignment with the `trunc%`
  already shown for CLI.
- **`ash_help` compact MCP form.** `help` is 10× more expensive over MCP
  than CLI. A compact MCP form (verb names + one-line descriptions only;
  harnesses pull full schemas from statically-embedded `tools/list`)
  would close most of the gap. Worth its own ticket.
- **`proto/probes.go` shared partial-decode helpers.** `MCPTruncationHint`
  is the third or fourth verb-agnostic probe in the codebase. If another
  lands (e.g. extracting `count` or `match_count` for a quick summary),
  the spread justifies consolidation.
