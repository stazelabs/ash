# ash

> An agentic shell for coding agents. Structured, lean, 🤖 robot-first.

`ash` is a shell designed from the ground up for AI coding agents as the primary user. It collapses the sprawling, platform-divergent surface of legacy Unix utilities into a small set of structured verbs that return typed, token-efficient results.

This is an aspirational project in early design and prototyping. Expect rough edges, breaking changes, and frequent rethinks.

## Why

Coding agents today drive bash. Bash was designed in 1989 for humans at terminals. The mismatch is real and expensive:

- **Token waste.** Unstructured stdout has to be re-parsed every call. JSON helps but is itself token-hostile (quote escapes, repeated keys, structural noise).
- **Platform variance.** `find` differs between macOS and GNU. `sed -i` takes different arguments. `grep -P` may or may not exist. Agents waste turns probing the environment.
- **State traps.** `cd`, environment variables, subshells, and globbing all introduce silent state that agents lose track of across turns.
- **Tool fragmentation.** Is `rg` installed? `fd`? `jq`? `bat`? Every project's agent has a different available toolset, and the agent has to discover it on every fresh start.
- **No semantic operations.** Agents repeatedly grep for symbols when they actually want callers, definitions, or references — semantic queries that no shell utility provides.

`ash` is the response: a shell where the primary user is a model with a token budget, no eyes, and no hands. The 15-verb surface is small enough to fit in a tool description. Every result is structured, every operation is the same on every platform, and the wire format is chosen for tokenizer efficiency.

## How we're building this

This project is two experiments at once: the shell itself, and a deliberate study in iteratively building a developer tool *with* the agents that will use it. Both experiments inform the design.

**Self-hosting is the goal.** The moment a "primordial" `ash` exists — Phase 1's walking skeleton with `find`, `grep`, and `read` — agents working on this repo start using `ash` for those operations. As each verb lands, agents adopt it for the corresponding bash workflow. We expect the surface to *change* as we use it; that is the entire point of building it this way.

**Why read-side first.** `find`, `grep`, and `read` cover the majority of what an agent does in a coding session and they touch nothing on disk. Mistakes cost nothing, feedback comes fast, and the wire protocol gets stress-tested before any verb can corrupt the workspace.

**Switch criteria.** Once a verb is live, agents in this repo prefer it for the corresponding workflow (any multi-file search, any read of a known path, etc.). The full operational checklist — which bash invocations to replace, in what order, with what fallbacks — lives in `CLAUDE.md` and evolves with the surface.

**Capturing the experience.** Every session that uses `ash` records which verbs were tried, where they fell short, which bash workarounds were needed, and what new verbs or flags the experience suggests. These notes are not retrospectives — they are direct input to the next phase's design. Session notes live in `docs/session-notes/`.

**The recursion is the experiment.** We're betting that an iteratively-built agent tool, evaluated against its own instrumentation, will converge on a more useful surface than top-down spec work. If we're wrong, we'll know — because the instrumentation will tell us so.

## Status

**Alpha.** Phase 2 is underway and self-hosting. Sixteen verbs are live: `find`, `grep`, `read`, `write`, `edit`, `stat`, `git` (status, log, diff, show), `test`, `diff`, `bench`, `metrics`, `report`, `hook`, `help`, `init`, and `uninit`. The daemon auto-starts, persists per-call instrumentation to a SQLite ledger, and tokenizes every response with `cl100k_base`. `ash bench` answers "is ash actually saving tokens?" by running canonical cases against the bash equivalent and reporting per-case Δtokens / Δlatency — see [docs/bench.md](docs/bench.md). Agents working on this repo use `ash` for all covered operations; session notes in `docs/session-notes/` capture the experience. Remaining Phase 2 verbs (`build`, `fmt`) and Phase 3 (`lang`) are upcoming. Expect breaking changes.

## Installing into a target repo

`ash` runs against any project whose harness supports Claude Code-style PreToolUse hooks. The flow is:

```sh
# In the ash repo:
make install                       # symlinks bin/ash and bin/ashd into ~/.local/bin
                                   # (override with PREFIX=/usr/local/bin)

# In any target repo:
cd /path/to/target
ash init                           # adds the PreToolUse hook to .claude/settings.json,
                                   # appends .ash/ to .gitignore, registers the root

# To analyze a target from the ash repo:
ash report --root /path/to/target  # query that target ledger
ash report --all_roots             # aggregate across every repo `ash init` has touched
```

Symlinks (not copies) so a rebuild of `ash` auto-updates every target — the daemon detects a stale binary and restarts on the next call. `ash uninit --path <p>` reverses everything except the captured `.ash/ledger.db` (kept for retroactive analysis). See [docs/install.md](docs/install.md) for the full design.

## Configuration

`ashd` reads optional TOML configuration from `<root>/ash.toml` (project-level, committed) and `$XDG_CONFIG_HOME/ash/config.toml` (user-global). Layering is last-wins: defaults → user-global → project → `$ASH_CONFIG=<path>` (explicit override). With no file present, behavior is identical to today.

The schema covers three sections:

- `[jail]` — when `enabled = true`, every path-taking verb refuses paths outside the project root or `allow_paths`, and rejects paths under `deny_paths`. Symlink escapes are caught by canonical-path resolution. Denied calls record a `path_denied` error in the ledger so deny rate is queryable via `ash report --verb <v>`.
- `[daemon]` — `max_concurrent_handlers`, `read_deadline`, `shutdown_grace`. Schema accepted today; enforcement ships under ASH-49.
- `[git]` — `backend = "go-git"` (default, in-process via go-git/v5; no system git required) or `"shellout"` (forks system git). The go-git backend supports `status`, `log`, `diff` (range), and `show`; for `--staged` or worktree patch text, opt back to shellout.

Copy `ash.toml.example` to `ash.toml` and uncomment the sections you want. The daemon must be restarted (`pkill ashd`, then any ash invocation auto-restarts it) for changes to take effect — hot reload is deliberately deferred. The full design lives in [docs/configuration.md](docs/configuration.md).

## Design principles

1. **Robot-first, human-second.** Every design decision optimizes for agent token efficiency and machine parseability. Human-readable rendering is a separate output mode, not the default.
2. **One canonical way.** No flag aliases, no synonyms, no "well, you could also do it like this." `ash grep`, not `ash search` or `ash find-text`. Consistency is enforced mechanically.
3. **Structured everything.** Every command returns the same envelope. Stdout-as-text is a fallback for human debugging, not the protocol.
4. **Bounded by default.** Every verb has sane output limits. Truncation messages teach the agent how to narrow the next query.
5. **Token-aware.** Every response reports its token cost. The agent can self-budget.
6. **Platform-uniform.** `ash grep` behaves identically on macOS, Linux, and Windows. No GNU-vs-BSD divergence, no missing utilities, no per-platform conditionals in agent prompts.
7. **Persistent context.** The shell is a daemon, not a per-command process. Sessions, objects, and jobs survive across invocations.
8. **Semantic when possible.** The `lang` verb gives agents callers/definitions/references via tree-sitter, not regex approximations.
9. **Instrumented by default.** Every verb call records latency, token cost, output size, truncation events, error class, and retry count to a session-scoped ledger. Performance and ergonomics claims are evaluated against the ledger, not against intuition. "Is `ash` actually better than bash?" needs to be an answerable question.

## The 15 verbs

| Verb | Purpose |
|---|---|
| `find` | List paths by glob, type, size, age. Replaces `find`, `ls`, `tree`. |
| `grep` | Pattern search across files. Ripgrep semantics, always. |
| `read` | Read file or byte range. Returns structured content + metadata. |
| `write` | Atomic file write. |
| `edit` | Apply structured patches (line ranges + replacements). |
| `stat` | File metadata, bulk-friendly. |
| `run` | Execute a registered tool with structured args. |
| `proc` | Manage long-running processes (start, stop, status, logs). |
| `git` | Version control as structured calls — not text-scraped. |
| `lang` | Semantic queries: callers, definition, references, outline. |
| `test` | Run project test suite. Structured pass/fail records. |
| `build` | Invoke project build. Errors as records (file, line, col, msg). |
| `fmt` | Format files per project config. Returns the diff. |
| `obj` | Manage named objects in the session. |
| `help` | Get the schema for any verb. Always machine-readable. |

That's the entire surface. Every coding task is a composition of these.

## What's deliberately gone

- **No `cd`, no `pwd`.** Every command takes an explicit path. State-free. The path-explicit shape has a quiet second benefit: it makes per-call sanitization tractable. Because every operation arrives with its full target path in the args, the daemon can validate, normalize, and (when desired) reject paths *before* the verb runs — a "sandbox-lite" that bash never offers because bash never sees the canonical form. We're not building a sandbox today; we're keeping the option open for free by refusing implicit state.
- **No `cat`, `head`, `tail`, `wc`, `sort`, `uniq`, `cut`, `awk`, `sed`, `xargs`, `tr`, `tee`.** All of these are operations *on* the result of another verb. `find ... | head 10` becomes `find --limit 10`. Arguments live on the producer.
- **No subshells, command substitution, backticks.** Object references replace them.
- **No shell globbing.** Verbs take patterns as explicit arguments.
- **No bash/sh/zsh dispatch.** Scripts run via `run` with an explicit interpreter.
- **No platform variance.** `ash` is the same everywhere. If a verb works on Linux, it works identically on macOS and Windows.

## Architecture

```
┌──────────────┐       msgpack over UDS       ┌──────────────┐
│  agent /     │ ──────────────────────────►  │   ash        │
│  human REPL  │ ◄──────────────────────────  │   daemon     │
└──────────────┘                              └──────┬───────┘
                                                     │
                                          ┌──────────┴──────────┐
                                          │                     │
                                  ┌───────▼───────┐    ┌────────▼────────┐
                                  │  builtins     │    │  registered     │
                                  │  (find, grep, │    │  tools          │
                                  │   read, ...)  │    │  (cargo, npm,   │
                                  └───────────────┘    │   pytest, ...)  │
                                                       └─────────────────┘
```

A single long-running daemon per project holds the session graph, object store, job ledger, and tool registry. Clients (agents or humans) connect over a Unix domain socket using a MessagePack-based protocol with a fixed schema dictionary for token efficiency.

## Wire format

The protocol is **MessagePack with a schema dictionary**, not JSON.

Why not JSON: quote escapes, repeated keys, and structural punctuation cost tokens. Field names like `"line_number"` show up thousands of times per session and pay full token cost every time.

Why MessagePack with a dictionary: integer-keyed fields tokenize to a single byte on the wire and expand to known names at the client. A typical `grep` result is roughly 4x more compact than the JSON equivalent.

A pretty-print mode (terse line-oriented format, not JSON) is available for human debugging and for harnesses that can't yet speak the binary protocol.

## Instrumentation

Instrumentation is wired in from the first verb, not retrofitted. A tool that can't honestly measure itself can't honestly claim improvement.

**What every call records.**

- Wall-clock latency, broken into parse / execute / serialize phases.
- Token cost — counted on the actual response bytes, not estimated.
- Bytes in and out.
- Output truncation events (which limits hit, by how much).
- Error class and retry count.
- Daemon CPU and memory delta for the call.

**The session ledger.** A persistent, queryable log per session, itself reachable through `ash` (probably via a system-managed object queryable through `obj`, possibly via a dedicated `metrics` verb — to be decided once we have data on which shape is more agent-friendly).

**Comparative evaluation.** When a session does the same work first in bash and then in `ash`, both runs land in the ledger. That gives apples-to-apples data for the Phase 4 benchmark suite (SWE-bench-Verified, Aider, Terminal-bench) instead of relying on after-the-fact reconstructions.

**Privacy.** The ledger is local-only. Export is opt-in and explicit; nothing leaves the machine without an action that says so.

**Why this matters now, not later.** Instrumentation needs to be present from the first verb call, not bolted on once the surface stabilizes. Retrofitted instrumentation is a known way to ship a tool that can't honestly evaluate itself.

## Example: a typical agent loop

**In bash today:**

```sh
$ rg "fn validate" --json | jq -r '.data.path.text' | sort -u
$ cat src/auth.rs | sed -n '140,160p'
$ cargo test --message-format=json | jq 'select(.reason=="test-result")'
$ git diff --stat HEAD~1
$ git log --oneline -20
```

Five commands, four output formats, three pieces of `jq` dialect, one platform-specific `sed`. The agent re-parses text in four different ways.

**In `ash`:**

```sh
ash> grep --pattern "fn validate" --path src/ --files-only
ash> read --path src/auth.rs --range 140:160
ash> test
ash> git diff --range HEAD~1..HEAD --summary
ash> git log --limit 20 --format short
```

Same five operations. One mental model. Structured results throughout. Roughly 2-3x lower token cost on the same workflow.

## Roadmap

### Phase 0 — Design (now)

- Lock the 15-verb surface and per-verb schemas
- Define the wire protocol and schema dictionary v1
- Build the bash-compatibility shim spec

### Phase 1 — Walking skeleton

- Go daemon with Unix domain socket transport
- MessagePack protocol with versioned schema dictionary
- Three verbs: `find`, `grep`, `read`
- Bash-compatible shim that translates legacy invocations
- Instrumentation ledger from day one — every call recorded
- Switch-criteria doc (operational guidance for agents in this repo, lives in `CLAUDE.md`)
- Self-host: agents on this repo switch to `ash` for `find` / `grep` / `read` the moment those verbs land

### Phase 2 — Coding-agent core

- Shipped: `write`, `edit`, `stat`, `git` (status, log, diff, show), `test`, `diff`, `bench`, `metrics`, `report`, `hook`, `help`, `init`, `uninit`
- Upcoming: `build`, `fmt`
- Persistent session and object store
- Job ledger for async operations
- Reference Go client library

### Phase 3 — Semantic layer

- `lang` verb backed by tree-sitter
- Symbol search, callers, definitions, references
- File outlines without bodies (token-efficient orientation)

### Phase 4 — Adoption

- Claude Code skill for `ash`
- Codex CLI integration
- Cursor extension
- Benchmark suite (SWE-bench-Verified, Aider, Terminal-bench) comparing bash vs ash
- Token-cost case studies from real sessions

### Phase 5 — Ecosystem

- Tool registration protocol (`ash.toml`) for third-party CLIs
- Adapters for the top 30 development tools
- Native protocol support in at least one major harness

## Why Go

Cross-compilation that just works. Single static binary per platform. Strong concurrency model for a daemon serving multiple agents. Stdlib breadth that minimizes dependencies. Ecosystem alignment — most agent-tooling projects ship in Go.

**No CGO.** A hard constraint, not a preference. CGO destroys cross-compilation and turns "single static binary" into "pile of platform-specific shared-library hunts." Every dependency must be pure Go. This already costs us — we use `modernc.org/sqlite` (pure-Go transpile) instead of the faster `mattn/go-sqlite3`, and similarly avoid CGO-bound tree-sitter bindings — and we accept that cost. Portability is a precondition for self-hosting on any developer's machine without ceremony.

The honest tradeoff: the killer libraries for the search and semantic layers (ripgrep's internals, tree-sitter's bindings) are best-in-class in Rust. We accept that tradeoff in exchange for build velocity and the ability to ship across platforms without scaffolding. If `ash` finds traction, a Rust rewrite of the hot paths is on the table; until then, shipped Go beats unshipped Rust.

## Non-goals

- **Not a bash replacement for sysadmin work.** `ash` doesn't try to win the human terminal. Bash and zsh keep that job.
- **Not Turing-complete scripting.** `ash` is a command bus, not a programming language. Loops, conditionals, and computation belong in Python or Go scripts that `ash` orchestrates.
- **Not a model wrapper.** No LLMs inside the shell. The shell is dumb infrastructure that makes smart agents efficient.
- **Not a lock-in.** Every session, object, and job is exportable. The value is in the running daemon, not in trapping data.

## Inspirations and credits

- Trevin Chow's [10 Principles for Agent-Native CLIs](https://trevinsays.com/p/10-principles-for-agent-native-clis) — the per-CLI principles that `ash` extrapolates to the shell layer.
- Cloudflare's [The CLI for all of Cloudflare](https://blog.cloudflare.com/cf-cli-local-explorer/) — schema-driven generation, vocabulary consistency, agents-as-primary-customer framing.
- Peter Steinberger's [discrawl](https://github.com/steipete/discrawl) and [gogcli](https://github.com/steipete/gogcli) — local SQLite + structured output as a CLI design pattern.
- Matt Van Horn's [printing-press](https://github.com/mvanhorn/cli-printing-press) — agent-native CLI generation, the absorb-and-transcend model.
- BurntSushi's ripgrep — the search semantics `ash grep` adopts wholesale.
- Tree-sitter — the parsing infrastructure behind the `lang` verb.
- Git's plumbing/porcelain split — the original instinct that machine consumers deserve a stable, structured interface alongside the human one.

## Contributing

Pre-alpha. Issues and design discussion are welcome; PRs are premature until Phase 1 lands. If you're thinking about agent-shell design, open an issue with your sharpest objection to the approach above. The design isn't settled and disagreement at this stage is more useful than agreement.

## License

MIT

---

*An aspirational project. The README is the spec; the spec is the design; the design will change. Watch this space.*
