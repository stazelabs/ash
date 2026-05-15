# ASH-125 — `ash_test` over MCP: wait for phase 2

**Task.** Decide whether to expose `ash_test` as an MCP tool now (v1.5,
one-line `readSideVerbs` change) or hold until phase 2 alongside the
writers (`write`, `edit`, `diff`).

**Decision.** Wait for phase 2.

## Why

The arguments for shipping now (real friction for MCP-only sessions,
low risk, one-line code change) are real but outweighed by three things
the ticket framing didn't fully account for:

1. **`test` is not read-only in the way the v1 set is.** The v1 eight
   touch nothing on disk. `test` invokes `go test`, which mutates the
   build cache, can write coverage files, and runs arbitrary test code
   in this repo. "Presumed safe" is a different category from "cannot
   touch state." Mixing those categories in v1 muddies the contract
   that made `readSideVerbs` easy to reason about.

2. **ASH-123 reframed the MCP-vs-CLI cost question.** MCP costs ≈3.4×
   more Claude tokens than the CLI pretty render across canonical
   read-side intents. `test` is the verb with the most verbose,
   structurally-repetitive output we ship — per-package pass/fail/skip
   with `file:line` extraction. Without measuring it under the
   `cmd/wirecmp` methodology first, exposing it over MCP risks landing
   the worst-case wire cost in the surface agents call most often.
   Phase 2 should expose `test` *after* ASH-124 StructuredContent has
   had a session or two to prove out the envelope-drop and we have a
   `wirecmp` row for `test` specifically.

3. **Streaming for `test` is gated on `progressToken` (ASH-106).** The
   per-package chunk emission only fires when the MCP client opts in.
   Harnesses that don't send a `progressToken` take the cumulative
   path, which for a large package set is exactly the failure mode
   ASH-123 warned about — a single huge frame. Better to land `test`
   over MCP after the progress-token story is settled across the
   harnesses we care about (Claude Code, Claude Desktop) rather than
   before.

## Cost of waiting

MCP-only sessions can't run tests via `ash_test` and fall back to bash
`go test` (or `ash test` if they have a shell). For the agents this
project actually exercises (Claude Code with a shell available), the
gap is small — `ash test` from bash is one keystroke away. The real
MCP-only future is harnesses like Claude Desktop, where sessions are
far less likely to be running tests in the first place.

## What to revisit at phase 2

When `write`/`edit`/`diff` ship over MCP:

- Add `test` to `readSideVerbs` (or rename the map — `readSideVerbs`
  is becoming a misnomer once writers join).
- Add a `wirecmp` fixture for `test` so the cost is in
  [`docs/mcp/wire-cost.md`](../mcp/wire-cost.md) before claiming
  parity.
- Re-check the streaming path: confirm Claude Code's MCP client sends
  a `progressToken` for `tools/call ash_test`, and document the
  cumulative fallback shape if it doesn't.
- Update the file header comment in
  [cmd/ashmcp/main.go](../../cmd/ashmcp/main.go) and adoption docs
  ([claude-code.md](../adoption/claude-code.md),
  [claude-desktop.md](../adoption/claude-desktop.md)) — both currently
  say "eight read-side verbs" and "long-running verbs (`ash_test`,
  `ash_bench`) follow as the Phase 2 ship list closes," which is the
  shape the wait preserves.

## Verbs used

`ash read`, `ash grep`, `ash find` for the codebase audit; Linear
`get_issue` / `save_issue` for the ticket itself. No code changed.

## Instrumentation

N/A — decision-only ticket.
