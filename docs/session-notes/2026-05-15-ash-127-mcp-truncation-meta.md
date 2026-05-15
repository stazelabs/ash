# ASH-127 — ashmcp surfaces truncation_hint via `_meta` so harnesses can narrow

**Task.** Make the structured truncation signal harness-readable over MCP. Before this ticket, `ash grep`/`ash read`/etc. truncations carried `TruncInfo {trunc, limit, max}` inside the response body. ashmcp dumped the whole body into a JSON `TextContent` block, so a harness couldn't programmatically detect "this response is partial" without parsing the envelope and inspecting a nested field it had no schema for. Effect: agents over MCP narrow truncated calls less reliably than agents over CLI, costing Claude tokens (more redundant follow-ups) and correctness.

**Verbs used.** `ash read` (a lot), `ash grep`, `ash edit`, `ash write`, `ash help`, `ash find`, `ash test`, `ash metrics`, `ash stop`.

## Design

Two-channel surfacing, both wired through one shared helper.

**Channel A — `_meta.ash.truncated`.** Whenever a successful response carries a non-zero `TruncInfo`, ashmcp adds the structured hint under `_meta.ash.truncated` on the `CallToolResult`. `_meta` is protocol-reserved metadata that doesn't count against the model — the agent gets a clean machine-readable signal for free.

**Channel B — sentinel TextContent.** ashmcp also prepends a short prose hint as the first `TextContent` block so harnesses that ignore `_meta` still see the signal. Two phrasings: `truncated: hit limit=L (max=M) — narrow the call or raise the verb's limit flag` when `Limit < Max`, and `truncated: hit hard cap (max=M) — narrow the call; raising the limit will not help` when `Limit == Max`. The hard-cap wording matters: a harness reading just the sentinel must not retry with `--max=higher` on a call that already saturated the cap.

**`IsError` stays false.** Truncation is a partial success, not a failure — the data the agent received is still useful, just incomplete.

## Implementation

- **`proto.MCPTruncationHint(rsp)` and `proto.MCPTruncationSentinel(rsp)`** ([internal/proto/mcpenv.go](../../internal/proto/mcpenv.go)). Verb-agnostic partial decode that picks up the `truncation_hint` msgpack field from any Result type. Lives in `proto` so ashmcp (emit) and ashd (tokens_out_emit accounting) cannot drift.
- **`proto.TruncInfo` gained `json` tags** ([internal/proto/proto.go](../../internal/proto/proto.go)). The type had only msgpack tags; without json tags `_meta.ash.truncated` would marshal to `{"Trunc":1,"Limit":256,"Max":4096}` (Go field names) instead of the lowercase keys the [outputSchema](../mcp/tools.json) advertises for the `proto.TruncInfo` shape. Adding the tags brought wire emission into agreement with the schema.
- **`toolResult` in [cmd/ashmcp/dispatch.go](../../cmd/ashmcp/dispatch.go)** decodes the hint once via `proto.MCPTruncationHint`, sets `_meta.ash.truncated`, and prepends the sentinel block. `_meta.ash.metrics` rides alongside as before. The structured-content emission path (ASH-124) is untouched — the truncation_hint field naturally rides inside it for harnesses that consume StructuredContent.
- **`ashd` MCP-emit accounting** ([cmd/ashd/main.go](../../cmd/ashd/main.go)) — when `req.Transport == TransportMCP`, the daemon already computed `emitBytes = len(MCPEnvelope(rsp))` and `emitTokens = Count(env)`. I added `+ len(sentinel)` and `+ Count(sentinel)` when the sentinel is non-empty, so `tokens_out_emit` in the ledger continues to equal exactly what the harness consumes. This is the same fidelity contract ASH-123 set up; the truncation-surface change extends it.
- **`wirecmp`** ([cmd/wirecmp/main.go](../../cmd/wirecmp/main.go)) — mirrors the same sentinel logic so the wirecmp tables stay byte-identical to the live ledger's `tokens_out_emit`. (The bundled fixtures don't truncate, so totals are unchanged for the existing report.)

## Verification

- **Unit tests.** [internal/proto/mcpenv_test.go](../../internal/proto/mcpenv_test.go) covers `MCPTruncationHint` (truncated, untruncated, nil/err/empty-data paths) and `MCPTruncationSentinel` (both phrasings, empty when not truncated). [cmd/ashmcp/dispatch_test.go](../../cmd/ashmcp/dispatch_test.go) covers `toolResult` end-to-end: structured `_meta.ash.truncated` shape (and JSON-roundtripped lowercase keys matching the outputSchema), sentinel prepended for both `Limit<Max` and `Limit==Max`, no sentinel/no `_meta.truncated` key for non-truncated responses, and ashmcp's sentinel matches `proto.MCPTruncationSentinel` exactly (fidelity invariant — drift here breaks `tokens_out_emit` accounting).
- **Full suite.** `bin/ash test` — 40/40 packages pass.
- **Drift gates.** `make vocab-check` / `make schema-check` / `make validate-check` all pass.
- **wirecmp smoke.** `go run ./cmd/wirecmp -repeat 1` against the bundled non-truncating fixtures produces a byte-identical table to before this change (sentinel returns "" so nothing is added).

## Friction

- **The PreToolUse hook fires before the harness Edit tool tracks its "I've read this file" state.** Calling harness `Read` is denied; harness `Edit` then refuses because it doesn't know the file. The CLAUDE.md note for Write says "do not fall back to harness Write — go straight to ash write"; the same applies to edits (use `ash edit`). I hit this once before remembering.
- **MCP SDK source lives outside the project jail.** Bash `grep` / `find` are redirected by the hook even when targeting `$GOMODCACHE`, which is the right default but means inspecting third-party Go types needs `mcp__ash__ash_read` against an absolute path (works fine — jail is project-rooted, not transport-rooted).

## Suggestions

- **`proto.MCPTruncationHint` is now the third or fourth verb-agnostic probe in this codebase.** If another lands (something like "extract the `count` or `match_count` for a quick summary"), it might be worth a tiny `proto/probes.go` with shared partial-decode patterns rather than letting them spread.
- **Consider exposing `_meta.ash.truncated` in the ledger's MCP-emit row.** Today `tokens_out_emit` counts the sentinel bytes; a `bool` column for "this MCP response was truncated" would let `ash report` answer "what % of MCP grep calls truncated vs CLI grep calls" — closer alignment with the `trunc%` already shown for CLI.

## Instrumentation

`bin/ash test` after the change: `40 pkgs (40 pass, 0 fail) — 3.15s`. New tests:
- `TestMCPTruncationHint` (internal/proto)
- `TestMCPTruncationSentinel` (internal/proto)
- `TestToolResultTruncationMeta` (cmd/ashmcp)
- `TestToolResultHardCapSentinel` (cmd/ashmcp)
- `TestToolResultNoTruncation` (cmd/ashmcp)
- `TestTruncationSentinelMatchesProto` (cmd/ashmcp)

wirecmp non-truncating totals before == after: CLI 6569B / 1564 cl100k, MCP 21708B / 5081 cl100k.
