# ASH-105 — one-source-of-truth schema generation

## Task

Close the drift hazard between three artifacts derived from the verb surface:
`ash help` text, vocab inventory (ASH-102), and the MCP tool schemas blocking
ASH-104 (`ashmcp`). Goal: a single registry (`internal/verbs/help`) feeds all
three, with build-time drift detection.

## Verbs used

`ash read`, `ash find`, `ash grep`, `ash write`, `ash edit`, `ash test`.

## Friction

- **Hook redirected harness Edit on the Makefile.** The `Edit` tool requires a
  prior harness `Read`; the project hook denies harness reads. Routed through
  `ash edit --old/--new` instead. This is documented in CLAUDE.md but easy to
  forget on the first non-trivial config edit of a session.
- **`ash grep --pattern foo --path bar.json`** with a positional-looking first
  arg blew up with `--pattern set both as a flag and as a positional argument`.
  The fix was `--pattern=foo --path=bar.json` (`=` form). The error is clear
  but the heuristic that treats a bare token after `--pattern` as the next
  positional is surprising for an exploratory grep — ASH-69-shaped wart.
  Worth a session-note-tagged follow-up.
- **Persisted-output for large reads.** `ash read` on the 39KB `help.go` wrote
  a 2KB preview and a sidecar file at `~/.claude/.../tool-results/`. The
  sidecar reads work, but they have to go through `ash read` themselves (not
  harness Read). One extra hop. No fix needed — just a thing to know.

## What landed

- `internal/mcpschema/` — pure generator. `Generate(help.Registry()) (*ToolList, error)`
  with `Marshal()` for the canonical bytes. Coalesces edit's duplicate `new`
  (one wire key across two modes), tags mode/op-restricted args with
  `[mode=…]` / `[op=…]` prefixes when the registry didn't already include a
  bracketed tag, declares JSON Schema draft 2020-12 dialect on each
  `inputSchema`, sets `additionalProperties: false` so unknown args fail at
  the schema layer (mirrors ASH-116's verb-level guard).
- `cmd/ashschema/` — sibling binary to `ashvocab`. `gen` writes
  `docs/mcp/tools.json`; `check` diffs against the checked-in artifact and
  exits 1 on drift. Same UX as `ashvocab gen` / `ashvocab check`.
- `docs/mcp/tools.json` — 17 tools, ~27 KB, `$schema` + `additionalProperties:
  false` per input schema. This is the file ASH-104 will `//go:embed`.
- Makefile: `make schema` / `make schema-check`, paired with `make vocab` /
  `make vocab-check` so the regen / gate rhythm is symmetric.
- CLAUDE.md memory-hygiene section gained a paragraph pointing at the new
  artifact and `make schema(-check)` cadence.

## Audit findings

The existing `help.ArgSchema` already carried `Name`, `Type`, `Required`,
`Default`, `Description`, `Long`, `Values`, `Ops`, `Mode`, `PH` — every field
JSON Schema needs. **No new ArgSchema fields required.** Decisions that
ended up flat in v1:

- **`oneOf` / `if-then-else`** for edit's mode discriminator and git's
  `--op` discriminator: deferred. The flat schema declares all args optional
  and uses `[mode=…]` / `[op=…]` description prefixes to communicate the
  conditional shape; the daemon enforces correctness post-validation. This
  matches how the current `ash help` text already documents these verbs.
- **`encoding: utf-8|base64` on `ash_write`** correctly emits as an enum
  with a default. Same for `--unit lines|bytes` on `ash_read` and `--op` on
  `ash_git`.
- **Default coercion** — registry stores stringly-typed `Default` ("true",
  "262144"); the generator parses them to proper JSON types so they
  serialize unquoted.

## Suggestions

- When ASH-104 lands and we have a live MCP session, revisit whether
  `oneOf` on edit's mode / git's op is worth the schema-complexity for the
  agent's autocomplete UX. The flat form is correct but less guided.
- Consider extending the `ash grep` heuristic so `--pattern foo --path bar`
  doesn't think `--path` is the second positional — surprising for an
  agent that has memorized `--key value` form.

## Instrumentation

Nothing exotic in the ledger this session: `ash test` was the only heavy
verb (1.6 s for the new package), `ash edit` round-trips on Makefile and
CLAUDE.md were sub-millisecond.
