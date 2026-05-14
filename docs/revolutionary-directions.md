# Revolutionary directions for ash

## Context

ash today is well-shaped for *evolutionary* improvement: the ledger captures every call, the vocab inventory ([ASH-102](https://linear.app/stazelabs/issue/ASH-102)) will close the static-protocol surface, Tier 1/2 token wins from [cli-tokens.md](cli-tokens.md) are queued. The encoding-substitution measurement on 2026-05-13 proved non-ASCII glyphs are a dead end and refocused effort on structural changes.

But every move on the current docket operates inside one frame: **agent issues a call -> ash returns text shaped for an LLM -> ledger records the cost.** Even the strongest single substitution (`metrics_no_equals`) is +0.68% aggregate. The ceiling on that frame is real.

This document lays out *frame-breaking* moves and the immediate next steps. Directional signal from the strategy session:

- **Goal:** adoption -- get ash in front of more agents.
- **First concrete step:** library-reuse audit.
- **Standard:** every revolutionary move worth considering gets a ticket with enough detail to evaluate later.

## Where the leverage hides

Three observations from exploration that should inform any leap:

- **The ledger is call-centric, not journey-centric.** Records every individual call but not retries, causal chains, or agent re-orientation cost. The data needed is mostly captured; what is missing is a layer reading it back at agent-relevant moments.
- **Claude charges ~19% more than cl100k_base** ([session-notes/2026-05-13-encoding-substitution-measurement.md](session-notes/2026-05-13-encoding-substitution-measurement.md)). Anthropic's prompt cache charges **10x less** on cached tokens. The ledger is optimizing the wrong target.
- **Approximation tax.** Agents grep+read+regex toward questions that have exact structured answers (callers, definitions, refs). Each approximation costs tokens *and* correctness.

---

# Part 1 -- Immediate next step: library-reuse audit ([ASH-103](https://linear.app/stazelabs/issue/ASH-103))

The recurring temptation in this repo is to build from scratch when mature pure-Go libraries exist. Before committing to any revolutionary move, an audit pins down current dep posture and what we should pull in vs build.

**Scope.**
- Enumerate every entry in [../go.mod](../go.mod) with one-line purpose.
- For each internal package that does general-purpose work (walker, diff, gitignore, tokenizer, regex, msgpack, sqlite driver, AST walking, etc.), check whether a mature pure-Go library covers it.
- Output a session note + a short table: `area | current | candidate | decision | reason`.

**Candidates worth scrutinizing.**
- **Gitignore parsing** in `internal/walker/` -- audit against `sabhiram/go-gitignore` or `denormal/go-gitignore`.
- **Diff** in the `diff` verb -- if hand-rolled, `sergi/go-diff` (Myers) is the de facto pure-Go choice.
- **SQLite driver** -- confirm `modernc.org/sqlite` (transpiled pure Go); rule out CGO drivers.
- **Tokenizer** -- confirm `pkoukk/tiktoken-go`; this is the universal choice.
- **AST walking** in `cmd/ashvocab/` -- stdlib `go/ast` is correct; no change.
- **MCP server** (incoming) -- `modelcontextprotocol/go-sdk` (Anthropic-maintained, pure Go).
- **LSP client** (incoming for [ASH-109](https://linear.app/stazelabs/issue/ASH-109)) -- `go.lsp.dev/protocol`.

**Output.** Session note + Linear ticket per non-trivial adoption decision. Low cost (one focused session) but tees up every revolutionary move with reduced scope.

---

# Part 2 -- The primary revolutionary lever: MCP server ([ASH-104](https://linear.app/stazelabs/issue/ASH-104)..[ASH-107](https://linear.app/stazelabs/issue/ASH-107))

Given adoption is the goal, **this is the single highest-leverage move on the list.** Today `ash hook` is the harness's enforcement seam -- deny built-in `Read`/`Edit`/`Bash`, force the agent into `ash`. That is *defense*. The native path: `ashmcp` exposes every verb as a typed MCP tool the harness sees as `ash_read`, `ash_grep`, `ash_lang_def` alongside its own built-ins.

## Why MCP is the adoption pivot

- **Current adoption funnel:** install ash -> agent uses bash -> hook denies -> agent eventually tries ash verbs -> maybe likes them. High friction; only works in projects with the hook configured.
- **MCP adoption funnel:** install ash + add to harness MCP config -> harness shows `ash_*` tools alongside built-ins from day one -> agent uses them organically because they are cheaper and typed.
- **Decouples adoption from enforcement.** The hook becomes optional, not central. Works in any MCP-aware harness (Claude Code, Claude Desktop, Cursor, others incoming) without modifying their settings.json.
- **Eliminates the CLI-parsing tax.** Args are typed JSON the harness sends directly. Pretty-format response overhead goes away for MCP-aware sessions.
- **Streaming becomes possible.** Long ops (test, grep across big trees) stream first matches in 50ms instead of waiting 5s for completion.

## Architecture sketch ([ASH-104](https://linear.app/stazelabs/issue/ASH-104))

Recommend a **separate binary** `cmd/ashmcp/` rather than expanding `ashd`'s surface. `ashmcp` is a transport adapter: it speaks MCP over stdio to the harness and talks to `ashd` over the existing UDS. Hundreds of LOC of glue, not thousands. The daemon stays single-purpose. Debugging stays clean (run `ashmcp` standalone for verification).

```
[harness] <--MCP stdio--> [ashmcp] <--UDS msgpack--> [ashd] <--SQLite--> ledger
```

## Schema generation -- the one-source-of-truth pivot ([ASH-105](https://linear.app/stazelabs/issue/ASH-105))

This is the deepest win MCP forces. Verbs declare their args via `help.Registry()`. [ASH-102](https://linear.app/stazelabs/issue/ASH-102) closed the static vocab. **All three artifacts (CLI help, MCP tool schemas, vocab inventory) should derive from a single registry** rather than diverge.

- Generate MCP tool definitions at build time from the verb registry.
- Schema baked into the `ashmcp` binary -- no startup roundtrip to `ashd` to discover tools.
- Same registry powers `ash help`, `docs/vocab/inventory.json`, and MCP schemas. Drift becomes a build failure.

## Streaming responses ([ASH-106](https://linear.app/stazelabs/issue/ASH-106))

MCP supports progressive content blocks. After v1 ships, wire streaming for:
- `grep` -- emit matches as they're found
- `test` -- emit per-package results as Go runs them
- `find` -- emit matches as the walker yields them

Removes the "wait for full result" tax on long ops.

## Naming, namespacing, coexistence

- MCP tool names: `ash_read`, `ash_grep`, `ash_lang_def`. The `ash_` prefix is the namespace.
- For MCP-aware harnesses, the hook becomes vestigial. Document this in rollout.
- For non-MCP harnesses (or when `ashmcp` isn't running), the hook still enforces CLI use. Backwards compatible.

## Library reuse

- **Server:** `modelcontextprotocol/go-sdk`. Do not roll our own.
- **Transport:** stdio. Most harnesses prefer it over HTTP. The SDK handles this.
- **Schema:** JSON Schema draft 2020-12 (MCP's required dialect). Generated from `help.Registry()` -- no hand-maintained schemas.

## Adoption docs ([ASH-107](https://linear.app/stazelabs/issue/ASH-107))

- One-pager: "Add ash to Claude Code" -- snippet for `~/.claude.json` MCP config.
- One-pager: "Add ash to Claude Desktop" -- snippet for Claude Desktop config.
- Migration note: when MCP is in use, the project's `PreToolUse` hook becomes optional.

---

# Part 3 -- The other revolutionary moves (backlog, evaluate later)

Every move worth considering has a ticket with enough detail to evaluate when the time comes.

## [ASH-108](https://linear.app/stazelabs/issue/ASH-108) -- Cache-aware response envelope

**Hypothesis.** Restructuring responses for Anthropic prompt-cache stability could cut conversation-level Claude token cost by 30-60% on cached portions. That dwarfs the entire Tier 1/2 roadmap combined.

**Approach.** Three response zones:
- **Stable prefix:** verb name, schema version, response shape header
- **Bounded-stable middle:** result data (paths, contents, matches)
- **Volatile suffix:** timing, request_id, sub-phase metrics -- moved to separate stderr write for pretty

**Wire-level.** Proto v2 with fixed-shape header. Document the contract in `docs/cache-shape.md`.

**Ledger impact.** Add `tokens_cache_hit` / `tokens_cache_miss` columns. Anthropic exposes both via `usage.cache_read_input_tokens`.

**Why now-ish.** Once [ASH-104](https://linear.app/stazelabs/issue/ASH-104) lands and MCP sessions become measurable, cache-hit telemetry becomes possible. Natural next move once MCP adoption produces real-Claude data.

## [ASH-109](https://linear.app/stazelabs/issue/ASH-109) -- LSP-bridged semantic index pilot

**Hypothesis.** Agents pay an approximation tax on semantic questions (callers, definitions, refs, implementations) that have exact structured answers. A semantic verb could replace 5-10 grep+read sequences with a single call.

**Key reframe.** **Do not write the indexer.** Bridge to existing language servers via `go.lsp.dev/protocol`. ash becomes a *broker* that caches LSP responses in `.ash/lang-cache.db`. Mature compiler teams (gopls, rust-analyzer, pyright) do the heavy lifting. Sidesteps the no-CGO + tree-sitter wall entirely.

**Pilot scope.** Go only, via gopls. Verbs: `ash lang outline`, `ash lang def`, `ash lang refs`, `ash lang callers`. Measure replay deltas vs equivalent grep+read sequences from real prior sessions.

**Adoption tie-in.** Each `lang` verb is also exposed as an MCP tool. Capability win compounds with adoption pivot.

## [ASH-110](https://linear.app/stazelabs/issue/ASH-110) -- Session graph / journey memory

**Hypothesis.** Agents thrash. They re-read the same file, re-grep the same pattern, and after compaction pay full re-orientation cost. A session-memory layer means "where was I?" is one cheap call.

**Approach.** Ledger schema extension: one new `session_links` table (FK to `calls`, causal-link heuristic). Two new verbs:
- `ash recap --since <window>` -- compact "where the agent is" summary
- `ash workspace` -- recent state as one cheap response, replacing re-reads after compaction

**Adoption tie-in.** `ash_workspace` as an MCP tool is the single most addictive ash verb after compaction.

## [ASH-111](https://linear.app/stazelabs/issue/ASH-111) -- Plan-as-object framework

**Hypothesis.** Multi-file refactors today are roughly `grep + N x read + N x edit + verify` -- 2N+1 calls and 2N+1 token charges. Atomic plan-as-object collapses to 2 calls *and* removes the agent's responsibility for cross-file consistency.

**Approach.**
- `ash plan rename --symbol Foo --to Bar` -> preview every edit, type-aware
- `ash plan apply --id <id>` -> all-or-nothing commit; failure rolls back
- Vetted refactorings: `extract-function`, `move-package`, `update-import`, `inline-call`
- Copy-on-write staging in `.ash/staging/`, atomic rename at apply

**Depends on [ASH-109](https://linear.app/stazelabs/issue/ASH-109)** (LSP brokering) for type-aware boundaries. gopls's rename and code-action APIs do the heavy lifting -- we orchestrate, not refactor.

## [ASH-112](https://linear.app/stazelabs/issue/ASH-112) -- `ash replay` regression suite

**Hypothesis.** The recursive-development thesis is currently human-mediated (session notes -> tickets -> ships). With replay, *the ledger itself becomes a regression test suite*. Every Tier 1 claim validated against real prior sessions, not synthetic benchmarks. Every other move on this list gets an empirical scoreboard.

**Approach.** `ash replay --session <id>` re-runs a recorded session against the current build. `ash replay --since 7d --diff` reports per-verb token deltas vs originals. Wire into CI; regressions block PRs.

**Cost.** Args + responses already in the ledger (msgpack). Need deterministic file-state snapshots per session (git refs or `.ash/snapshots/`). Probably the cheapest ticket on this list -- half a ship.

**Why this could be next-after-MCP.** Once MCP adoption produces real cross-harness session data, replay's value compounds: we can prove that [ASH-108](https://linear.app/stazelabs/issue/ASH-108) (cache envelope) actually moved Claude-side token cost on real Claude Code sessions, not just synthetic measurements.

---

# Recommended execution order

1. **[ASH-103](https://linear.app/stazelabs/issue/ASH-103) (library-reuse audit)** -- immediate. Half a session. Informs everything else.
2. **[ASH-104](https://linear.app/stazelabs/issue/ASH-104) + [ASH-105](https://linear.app/stazelabs/issue/ASH-105) (MCP server + one-source schema)** -- one ship + change. This is the adoption pivot.
3. **[ASH-107](https://linear.app/stazelabs/issue/ASH-107) (adoption docs)** -- alongside ASH-104.
4. **[ASH-112](https://linear.app/stazelabs/issue/ASH-112) (replay)** -- cheap, unlocks empirical validation for every subsequent move.
5. **[ASH-108](https://linear.app/stazelabs/issue/ASH-108) (cache envelope)** -- biggest real Claude-side token win; measurable via replay.
6. **[ASH-106](https://linear.app/stazelabs/issue/ASH-106) (streaming MCP)** -- once v1 stabilizes.
7. **[ASH-109](https://linear.app/stazelabs/issue/ASH-109) (LSP-bridged semantic pilot)** -- the capability pivot; Phase 3 territory.
8. **[ASH-110](https://linear.app/stazelabs/issue/ASH-110) (session memory)** -- after MCP and replay are live.
9. **[ASH-111](https://linear.app/stazelabs/issue/ASH-111) (plan-as-object)** -- after ASH-109 lands. Phase 4.

## Dependency graph

- ASH-103 (audit) -> blocks ASH-104, ASH-109
- ASH-105 (schema) -> blocks ASH-104
- ASH-104 (ashmcp) -> blocks ASH-106, ASH-107, ASH-108, ASH-110
- ASH-109 (LSP) -> blocks ASH-111
- ASH-112 (replay) -> blocks ASH-108
- ASH-103, ASH-105, ASH-112 have no blockers; safe to start in parallel.

## Critical files

- [../README.md](../README.md) -- vision, phase plan, constraints
- [cli-tokens.md](cli-tokens.md) -- Tier 1-4 evolutionary token roadmap
- [session-notes/2026-05-13-encoding-substitution-measurement.md](session-notes/2026-05-13-encoding-substitution-measurement.md) -- +19% Claude finding
- [session-notes/2026-05-13-vocab-design.md](session-notes/2026-05-13-vocab-design.md) -- design predecessor for [ASH-102](https://linear.app/stazelabs/issue/ASH-102) and [ASH-105](https://linear.app/stazelabs/issue/ASH-105)
- [../go.mod](../go.mod) -- entry point for [ASH-103](https://linear.app/stazelabs/issue/ASH-103)
- `internal/help/` -- registry that [ASH-105](https://linear.app/stazelabs/issue/ASH-105) will derive schemas from
- `internal/ledger/ledger.go` -- call-centric schema; extended by [ASH-110](https://linear.app/stazelabs/issue/ASH-110)
- `internal/vocab/` -- static-vocab inventory; load-bearing for [ASH-105](https://linear.app/stazelabs/issue/ASH-105)
