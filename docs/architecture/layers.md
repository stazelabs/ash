# ash layering one-pager (ASH-166)

A vocabulary so the next architectural conversation doesn't re-derive
the tiers from the code. Companion to [docs/mcp/design-decisions.md](../mcp/design-decisions.md)
(MCP-specific), [docs/streaming.md](../streaming.md) (wire-level),
[docs/cache-shape.md](../cache-shape.md) (envelope contract), and the
ASH-160 recalibration plan.

## The four tiers

```
┌─────────────────┐    ┌──────────────────┐
│  cmd/ash  (CLI) │    │ cmd/ashmcp (MCP) │   ← clients
└────────┬────────┘    └────────┬─────────┘     (transports)
         │   msgpack over UDS   │
         └──────────┬───────────┘
                    ▼
          ┌──────────────────────┐
          │     cmd/ashd         │             ← dispatch
          │  (handle, registry)  │
          └──────────┬───────────┘
                     │
                     ▼
        ┌────────────────────────┐
        │  internal/verbs/<v>    │             ← verb library (the asset)
        │  Args / Run / Pretty   │
        └────────────────────────┘
                     │ wire types
                     ▼
        ┌────────────────────────┐
        │     internal/proto     │             ← protocol
        │ Request / Response /   │
        │ framing / streaming    │
        └────────────────────────┘
```

### 1. protocol — [internal/proto](../../internal/proto)

The wire format. Owns:

- `Request`, `Response`, `Metrics`, `Error`, `TruncInfo`, `Tracer` types.
- Length-prefixed framing (`WriteFrame`/`ReadFrame`) and the kind-tagged
  streaming envelope from ASH-106 (`KindChunk`, `KindFinal`, `KindCancel`).
- msgpack encoding with a versioned schema dictionary.
- Pretty-form helpers shared by daemon and clients
  (`PrettyResponseHeader`, `PrettyRequest`, `PrettyRequestArgv`).
- MCP-side envelope helpers (`MCPEnvelope`, `MCPStructuredData`,
  `MCPTruncationSentinel`).
- The [cache-shape contract](../cache-shape.md) — stable fields up front,
  volatile fields at the tail.

What lives here: anything two layers above (clients, dispatch) both need
to agree on byte-for-byte. The package knows nothing about verbs, the
ledger, or jail policy.

### 2. verb library — [internal/verbs](../../internal/verbs)

The asset. All structural work lives here. One Go subpackage per verb,
each exposing:

- `Args` struct — the typed args.
- `ParseArgs(map[string]any) (*Args, *proto.Error)` — wire decoder.
- `Run(*Args, *proto.Tracer) (*Result, *proto.Error)` — pure execution.
- `Result` struct — typed result; msgpack-tagged.
- `PrettyResponse(*proto.Request, *proto.Response) string` — canonical
  human/LLM rendering (drives token counting on both daemon and client).
- Optional `CompactResponse` for row-shaped verbs (ASH-153).

[`internal/verbs/verbs.go`](../../internal/verbs/verbs.go) is the
registration seam — `Runners()`, `PrettyHandlers()`, `CompactHandlers()`.
[`internal/verbs/help`](../../internal/verbs/help) is the
arg-schema/description registry (`help.Registry()`), the source of
truth for `ash help`, `tools.json`, the CLI's bool-flag inference, and
the vocab inventory.

What lives here: verb logic over typed args. Pure functions where
possible; for verbs that need long-lived state (e.g. the ledger), the
runner closes over the dep at registration time.

What does NOT live here: knowledge of the UDS, MCP, the daemon's
lifecycle, or the client's flag parsing.

### 3. dispatch — [cmd/ashd](../../cmd/ashd)

The daemon. Owns:

- Per-project UDS listener + accept loop + concurrency cap (ASH-49).
- `handle()` — per-frame request/response loop, wires verb runners to
  proto frames, records every call to the SQLite ledger
  ([internal/ledger](../../internal/ledger)).
- Lifecycle: graceful shutdown drain, double-bind protection (ASH-154),
  PID file management.
- Subprocess-owning state — the LSP broker
  ([internal/lsp](../../internal/lsp)) and its cache
  ([internal/lsp/cache](../../internal/lsp/cache)).
- Per-request hot-reload check on `ash.toml` for enforcement-layer
  config (ASH-164) via [internal/config](../../internal/config)'s
  `Watcher`.

What lives here: everything that depends on daemon lifecycle (the
listener, the ledger, the LSP subprocess, the jail policy as a
package-global). Verb logic does not — it imports `internal/proto`
and `internal/jail` but not `cmd/ashd`.

### 4. clients

Both clients are transports — they package a request, send it over
UDS, decode the response. Neither owns verb logic.

**[cmd/ash](../../cmd/ash) — the CLI.** Parses `--key value`-style
flags against `help.Registry()`, dials/auto-starts `ashd`, sends the
request, renders the response via `verbs.PrettyHandlers()[verb]`. Two
verbs are client-only: `hook` (PreToolUse decision engine; daemon
records but client decides for low latency) and `stop` (terminates
the daemon).

**[cmd/ashmcp](../../cmd/ashmcp) — the MCP adapter.** Speaks Model
Context Protocol over stdio. Tool schemas come from the embedded
`tools.json` ([docs/mcp/tools.json](../mcp/tools.json), generated from
the same `help.Registry()`). Filters to `exposedVerbs` (read-side +
write-side; orchestration verbs stay CLI-only per ASH-161). Same UDS
roundtrip path as the CLI — `Transport=mcp` on the wire so the
daemon's `tokens_out_emit` accounting tokenizes the right shape.

## Worked examples — what tier does X belong in?

- **A new arg on an existing verb.** → verb library
  ([internal/verbs/<v>](../../internal/verbs)) + a registry entry in
  [internal/verbs/help](../../internal/verbs/help). The CLI inherits
  parsing from `help.Registry()`; the MCP schema regenerates from the
  same source via `make schema`. No protocol or client edits needed.

- **A new wire field on `Response.Metrics`.** → protocol
  ([internal/proto](../../internal/proto)). Add the msgpack tag at the
  *tail* of the struct ([docs/cache-shape.md](../cache-shape.md)
  pinning tests will fail otherwise). Then update verbs that emit it
  and update report's aggregator to consume.

- **A new CLI flag like `--format msgpack`.** → CLI client
  ([cmd/ash](../../cmd/ash)). Global flags are stripped before the
  request hits `ashd`; the daemon never knows.

- **A new MCP tool surface (e.g. expose a verb that was CLI-only).** →
  add to `exposedVerbs` in [cmd/ashmcp/main.go](../../cmd/ashmcp/main.go).
  The verb library and dispatch don't change — the MCP adapter is a
  filter on the same set of runners. (ASH-161 was exactly this.)

- **A new ledger column.** → [internal/ledger](../../internal/ledger)
  (schema), [cmd/ashd](../../cmd/ashd) (write site in `handle()`),
  and any report/recap query consumers. Avoid this unless an existing
  field can't carry the signal — schema migrations are expensive.

- **A new piece of enforcement-layer config (e.g. a new jail rule).** →
  [internal/config](../../internal/config) (TOML parse),
  [internal/jail](../../internal/jail) (enforcement),
  and `applyEnforcementConfig` in [cmd/ashd](../../cmd/ashd) so
  ASH-164's hot-reload picks it up automatically.

- **A new ash.toml section that controls subprocess lifecycle (e.g.
  another LSP server).** → same files as above PLUS the
  `applyEnforcementConfig` boundary explicitly excludes it (subprocess
  lifecycle stays restart-required per ASH-164).

- **A friction-prompt pattern detector.** → NOT a verb. Add the
  heuristic to [.claude/commands/friction.md](../../.claude/commands/friction.md)
  (ASH-168). The recalibration explicitly rejected a Go-side `friction`
  verb in favor of the prompt — synthesis is a prompt, not Go code.

## Tooling (not runtime)

These cmd binaries are dev-time only and don't fit the four tiers:

- [cmd/ashschema](../../cmd/ashschema) — generates `tools.json` from
  `help.Registry()`. Runs via `make schema`; `make schema-check` gates
  drift.
- [cmd/ashvocab](../../cmd/ashvocab) — generates the agent-facing
  string inventory ([docs/vocab/inventory.md](../vocab/inventory.md)).
  Runs via `make vocab`; `make vocab-check` gates drift.
- [cmd/wirecmp](../../cmd/wirecmp) — measures per-fixture CLI vs MCP
  wire cost. Feeds [docs/mcp/wire-cost.md](../mcp/wire-cost.md).
- [cmd/encexplore](../../cmd/encexplore) — encoding validation against
  the Anthropic `count_tokens` endpoint.

## Where this doc fits

This is vocabulary, not policy. It does not replace the more detailed
docs:

- [docs/mcp/design-decisions.md](../mcp/design-decisions.md) — why
  `ashmcp` looks the way it does.
- [docs/streaming.md](../streaming.md) — wire-level streaming envelope.
- [docs/cache-shape.md](../cache-shape.md) — the envelope contract that
  pins volatile fields to the tail.
- [docs/configuration.md](../configuration.md) — ash.toml layering and
  per-section semantics.
- [CLAUDE.md](../../CLAUDE.md) — operational guidance for agents
  working in the repo.

Reach for those when you need depth. Reach for *this* when you need to
agree on which tier a change belongs in.
