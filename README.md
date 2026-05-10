# ash

> An agentic shell for coding agents. Structured, lean, robot-first.

`ash` is a shell designed from the ground up for AI coding agents as the primary user. It collapses the sprawling, platform-divergent surface of legacy Unix utilities into a small set of structured verbs that return typed, token-efficient results.

**Status:** alpha, phase 2 of 5. 17 verbs live, self-hosting on this repo, instrumented end-to-end. Expect breaking changes.

## Why

Coding agents today drive bash. Bash was designed in 1989 for humans at terminals. The mismatch is real and expensive:

- **Token waste.** Unstructured stdout has to be re-parsed every call. JSON helps but is itself token-hostile (quote escapes, repeated keys, structural noise).
- **Platform variance.** `find` differs between macOS and GNU. `sed -i` takes different arguments. `grep -P` may or may not exist. Agents waste turns probing the environment.
- **State traps.** `cd`, environment variables, subshells, and globbing all introduce silent state that agents lose track of across turns.
- **Tool fragmentation.** Is `rg` installed? `fd`? `jq`? `bat`? Every project's agent has a different available toolset, and the agent has to discover it on every fresh start.
- **No semantic operations.** Agents repeatedly grep for symbols when they actually want callers, definitions, or references — semantic queries that no shell utility provides.

`ash` is the response: a shell where the primary user is a model with a token budget, no eyes, and no hands. The verb surface is small enough to fit in a tool description. Every result is structured, every operation is the same on every platform, and the wire format is chosen for tokenizer efficiency. Every call is recorded in a per-project SQLite ledger with real cl100k_base token counts, so "is this saving tokens?" is an answerable question.

## Quick start

```sh
# Build (pure Go; no system deps required)
make all

# Install on $PATH (symlinks, not copies; a rebuild here updates every install)
make install                       # → $HOME/.local/bin (override with PREFIX=/usr/local/bin)
```

In any target repo:

```sh
cd /path/to/target
ash init                           # adds the PreToolUse hook to .claude/settings.json,
                                   # appends .ash/ to .gitignore, registers the root
ash help                           # see all live verbs
ash help --verb grep               # full argument schema for one verb
```

The daemon (`ashd`) auto-starts on first client invocation per project, talks to the client over a per-project Unix domain socket, and persists per-call instrumentation to `.ash/ledger.db`. Stop it with `ash stop`; the next call auto-starts a fresh one. To uninstall the hook + registry entry from a target repo, run `ash uninit` (the ledger DB is left in place for retroactive analysis).

To analyze captured data, in or out of the target repo:

```sh
ash report                         # per-verb summary for the current daemon session
ash report --since 1h              # last hour, all sessions in this repo
ash report --root /path/to/target  # query a target repo's ledger from anywhere
ash report --all_roots             # aggregate across every repo `ash init` has touched
```

## How we're building this

This project is two experiments at once: the shell itself, and a deliberate study in iteratively building a developer tool *with* the agents that will use it. Both inform the design.

**Self-hosting from day one.** As soon as primordial `ash` existed (`find` / `grep` / `read`), agents working on this repo started using `ash` for those operations. Every verb that lands gets dogfooded immediately; the surface is shaped by friction in real sessions, not spec work. A PreToolUse hook (`ash hook`) enforces the switch by intercepting the harness's built-in `Grep` / `Glob` / `Edit` / `Write` / `Read` and bash equivalents, returning the equivalent `ash` invocation as the deny reason. The hook itself is queryable via `ash report --verb hook`, so the friction is measurable too. See [docs/PreToolUse.md](docs/PreToolUse.md).

**Capturing the experience.** Every call lands in a per-project SQLite ledger: which verb ran, parse / exec / serialize latency, walk / io / regex sub-phases, real cl100k_base token counts (in and out), bytes in/out, truncation events, error class, and a sanitized msgpack arg blob for post-hoc query-shape grouping. Session-level qualitative notes live in [docs/session-notes/](docs/session-notes/) and feed the next ship's design.

**Why read-side first.** `find`, `grep`, and `read` cover the majority of what an agent does in a coding session and they touch nothing on disk. Mistakes cost nothing, feedback comes fast, and the wire protocol gets stress-tested before any verb can corrupt the workspace.

**The recursion is the experiment.** We're betting that an iteratively-built agent tool, evaluated against its own instrumentation, converges on a more useful surface than top-down spec work. The instrumentation tells us if we're wrong. `ash bench` (see [docs/bench.md](docs/bench.md)) closes the loop: it runs canonical cases against ash and the bash equivalent and reports per-case Δtokens / Δlatency.

**Three-tier agent docs.** Three surfaces share the agent-facing load:

- **README** (this file, for humans evaluating ash) owns design, install, the verb surface index, examples.
- **[CLAUDE.md](CLAUDE.md)** (for agents working *in this repo*) owns switch criteria, hard-won gotchas, the session-feedback ritual, and the bash whitelist.
- **`ash help [--verb <name>]`** is the authoritative per-verb arg schema — never duplicated in markdown.

Agents in repos that ran `ash init` see only the hook's deny messages today; no CLAUDE.md is propagated to target repos yet (tracked as a follow-up). The design rationale and maintenance ritual for these surfaces live in [docs/agent-guidance.md](docs/agent-guidance.md).

## The verb surface

17 verbs are live as of phase 2 ship 14. Every verb returns a structured response over a MessagePack-with-schema-dictionary protocol; the same data renders as a token-lean pretty form for human (or LLM) consumption. Run `ash help` for the full schema of every verb, or `ash help --verb <name>` for one.

### File system

| Verb | Purpose |
|---|---|
| `read` | Read a file, or a line/byte range of one. UTF-8 returned as-is; binary base64-encoded. |
| `find` | Walk a tree by glob/type/depth. Respects `.gitignore`; skips hidden by default. |
| `grep` | RE2 pattern search across files. Smart-case, fixed-string, word-boundary, context lines, `--files_only`, `--no_text`. |
| `stat` | Bulk filesystem metadata (`lstat`-based). Per-path errors keep the call alive. |
| `write` | Atomic file write (temp-file + rename). Creates parent dirs. |
| `edit` | In-place edit: string-mode, line-range mode, or unified-diff patch mode. `--dry_run` previews without writing. |
| `diff` | Unified diff between two files, or a file and stdin. `--stat true` for token-cheap counts. |

### Version control

| Verb | Purpose |
|---|---|
| `git --op status\|log\|diff\|show` | Git as structured calls — not text-scraped. Default backend is in-process go-git; `shellout` opt-in for `--staged` / unstaged patch text. |

### Build / test

| Verb | Purpose |
|---|---|
| `test` | Run Go tests via `go test -json`. Structured per-package/per-test results; build failures land as records, not raw stderr. |

### Observability

| Verb | Purpose |
|---|---|
| `metrics` | Raw recent ledger rows. |
| `report` | Aggregated per-verb summary: n, ok%, p50/p95 latency, p50/p95 tokens_out, truncation rate, top error histograms, top truncation hotspots. Cross-repo via `--root` / `--all_roots`. |
| `bench` | Run canonical cases against ash and the bash equivalent the agent would otherwise have used; tokenize both with the same encoder; report Δtokens / Δlatency per case. |

### Lifecycle

| Verb | Purpose |
|---|---|
| `init` | Bootstrap a target repo: PreToolUse hook + `.gitignore` + registry. Idempotent. |
| `uninit` | Reverse `init`. Leaves `.ash/ledger.db` in place. |
| `stop` | Stop the per-project daemon cleanly. Next call auto-starts a fresh one. |
| `hook` | Claude Code PreToolUse decision engine. Not normally invoked manually. |
| `help` | Return the full argument schema for one verb or all of them. |

### Conventions across the surface

- **All paths are explicit.** No verb relies on `cwd` for path resolution beyond the daemon's project root. The agent passes the full path it cares about; the daemon canonicalizes and validates. With `[jail].enabled = true` in `ash.toml`, paths outside the project root (and `allow_paths`) are denied with a `path_denied` error.
- **All string-valued args accept `-` for stdin.** Especially relevant for `write --content -`, `edit --patch -` (or `--new_content -`), `diff --content -` to sidestep shell quoting on multiline or quote-heavy content.
- **Bounded by default.** Every verb has output limits with truncation hints that teach the agent how to narrow the next query.
- **`--format pretty|json|msgpack`** is a global client flag stripped before the request hits the daemon. `pretty` (default) is human-readable; `json` emits the full response envelope as indented JSON; `msgpack` writes raw wire bytes.

## Configuration

`ashd` reads optional TOML configuration from `<root>/ash.toml` (project-level, committed) and `$XDG_CONFIG_HOME/ash/config.toml` (user-global). Layering is last-wins: defaults → user-global → project → `$ASH_CONFIG=<path>` (explicit override). With no file present, behavior is identical to the pre-config era. Restart the daemon (`ash stop`, then any ash invocation) after editing — hot reload is deliberately deferred.

The schema covers five sections; see [`ash.toml.example`](ash.toml.example) for an annotated template:

- **`[jail]`** — `enabled = true` makes every path-taking verb refuse paths outside the project root or `allow_paths`, and reject paths under `deny_paths`. Symlink escapes are caught by canonical-path resolution. Denied calls record a `path_denied` error in the ledger so deny rate is queryable via `ash report --verb <v>`.
- **`[daemon]`** — `max_concurrent_handlers` (default 0 = unlimited; opt-in cap), `read_deadline` (default 30s, per-frame socket timeout), `shutdown_grace` (default 5s, bounded handler drain on SIGTERM).
- **`[git]`** — `backend = "go-git"` (default, in-process via go-git/v5; no system git required) or `"shellout"` (forks system git). go-git has full functionality for `status`, `log`, range diff, and `show`. For `--staged` or unstaged worktree patch text (not just counts), opt back to shellout.
- **`[ledger]`** — `max_age` (default `"720h"` / 30 days; `"0s"` disables), `max_rows` (default 0 = no row cap), `vacuum` (default false; `PRAGMA optimize` runs instead). Cleanup runs once at daemon startup before the accept loop opens.
- **`[hook]`** — `exclude_verbs` to exempt specific verbs from PreToolUse enforcement (e.g. `["test"]` while debugging the test verb itself). Excluded denies are still recorded in the ledger with a `:excluded` suffix on `matched_rule`.

Full reference: [docs/configuration.md](docs/configuration.md).

## Design principles

1. **Robot-first, human-second.** Every design decision optimizes for agent token efficiency and machine parseability. Human-readable rendering is a separate output mode, not the default.
2. **One canonical way.** No flag aliases, no synonyms, no "well, you could also do it like this." `ash grep`, not `ash search` or `ash find-text`. Consistency is enforced mechanically.
3. **Structured everything.** Every command returns the same envelope. Stdout-as-text is a fallback for human debugging, not the protocol.
4. **Bounded by default.** Every verb has sane output limits. Truncation messages teach the agent how to narrow the next query.
5. **Token-aware.** Every response reports its real token cost. The agent can self-budget.
6. **Platform-uniform.** `ash grep` behaves identically on macOS, Linux, and Windows. No GNU-vs-BSD divergence, no missing utilities, no per-platform conditionals in agent prompts.
7. **Persistent context.** The shell is a daemon, not a per-command process. Sessions, objects, and jobs survive across invocations.
8. **Semantic when possible.** The forthcoming `lang` verb gives agents callers/definitions/references via tree-sitter, not regex approximations.
9. **Instrumented by default.** Every verb call records latency, token cost, output size, truncation events, error class, and sanitized args to a session-scoped ledger. Performance and ergonomics claims are evaluated against the ledger, not against intuition.

## Constraints

These are hard rules. A change that violates one is a stop-and-discuss.

- **No CGO.** All dependencies must be pure Go. Every developer should be able to clone, `go build`, and run on any platform Go cross-compiles to, with no native toolchain required. This rules out `mattn/go-sqlite3`, CGO-bound tree-sitter, and similar. We've already paid the perf cost on SQLite (`modernc.org/sqlite`) and accept it. Portability is a precondition for self-hosting on any developer's machine without ceremony.
- **All paths are explicit and absolute-friendly.** No verb relies on `cwd` beyond the daemon's project root. Optional `[jail]` policy refuses paths outside the project root.

## What's deliberately gone

- **No `cd`, no `pwd`.** Every command takes an explicit path. State-free. The path-explicit shape has a quiet second benefit: it makes per-call sanitization tractable. Because every operation arrives with its full target path in the args, the daemon can validate, normalize, and reject paths *before* the verb runs — the optional `[jail]` policy already does exactly this. Bash never sees the canonical form.
- **No `cat`, `head`, `tail`, `wc`, `sort`, `uniq`, `cut`, `awk`, `sed`, `xargs`, `tr`, `tee`.** All of these are operations *on* the result of another verb. `find ... | head 10` becomes `find --limit 10`. Arguments live on the producer.
- **No subshells, command substitution, backticks.** Object references will replace them once `obj` ships.
- **No shell globbing.** Verbs take patterns as explicit arguments.
- **No bash/sh/zsh dispatch.** Scripts will run via `run` with an explicit interpreter once `run` ships.
- **No platform variance.** `ash` is the same everywhere. If a verb works on Linux, it works identically on macOS and Windows.

## Architecture

```
┌──────────────┐       msgpack over UDS       ┌──────────────┐
│  agent /     │ ──────────────────────────►  │   ashd       │
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

A single long-running daemon per project holds the session, ledger, and (eventually) object store and tool registry. Clients connect over a Unix domain socket. The daemon auto-starts on first client invocation by exec'ing `ashd` (sibling lookup, then `$PATH`, then `$ASH_DAEMON`); subsequent calls reuse the same daemon.

State lives in:

- `.ash/ledger.db` — SQLite, one row per call. 30-day retention by default.
- `.ash/ashd.log` — daemon stderr/stdout.
- `$XDG_RUNTIME_DIR/ash/` or `$TMPDIR` — UDS file (`ash-<8-byte-hash>.sock`).

The daemon also prints a one-line metrics summary to stderr after every call, which is the `[ash metrics: …]` line the client surfaces below each response.

## Wire format

The protocol is **MessagePack with a schema dictionary**, not JSON.

Why not JSON: quote escapes, repeated keys, and structural punctuation cost tokens. Field names like `"line_number"` show up thousands of times per session and pay full token cost every time.

Why MessagePack with a dictionary: integer-keyed fields tokenize to a single byte on the wire and expand to known names at the client. A typical `grep` result is roughly 4× more compact than the JSON equivalent.

A pretty-print mode (terse line-oriented format, not JSON) is the default for human debugging and for harnesses that can't yet speak the binary protocol. `--format json` returns the full response envelope; `--format msgpack` writes raw wire bytes.

## Instrumentation

Instrumentation was wired in from the first verb, not retrofitted. A tool that can't honestly measure itself can't honestly claim improvement.

**What every call records.**

- Wall-clock latency, broken into parse / execute / serialize phases.
- Sub-phase latency for verbs that instrument it (walk / io / regex).
- Token cost — counted on the actual response bytes via cl100k_base, not estimated.
- Bytes in and out.
- Output truncation events (which limits hit, by how much).
- Error class and message.
- A sanitized msgpack arg blob, so post-hoc analysis can group by query shape.

**Querying.** `ash metrics` for raw rows; `ash report` for synthesis (n, ok%, p50/p95 latency, p50/p95 tokens_out, truncation rate, top error histograms, top truncation hotspots). Both work cross-repo via `--root <p>` (read a foreign repo's ledger directly) or `--all_roots true` (aggregate across every repo `ash init` has touched).

**Comparative evaluation.** `ash bench` runs canonical cases against ash and the bash equivalent the agent would otherwise have used, tokenizes both with the same encoder, and reports per-case Δtokens / Δlatency, plus per-verb and overall summaries. See [docs/bench.md](docs/bench.md).

**Privacy.** The ledger is local-only. Export is opt-in and explicit; nothing leaves the machine without an action that says so.

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
ash grep --pattern 'fn validate' --path src/ --files_only true
ash read --path src/auth.rs --range 140:160
ash test
ash git --op diff --range HEAD~1..HEAD --stat true
ash git --op log --limit 20
```

Same five operations. One mental model. Structured results throughout. (Run `ash bench` for the actual numbers on your machine.)

## Roadmap

### Phase 0 — design — done
Surface locked, wire protocol drafted, switch-criteria doc ([CLAUDE.md](CLAUDE.md)) written.

### Phase 1 — walking skeleton — done
Go daemon, UDS transport, MessagePack with versioned schema dictionary, three read-side verbs (`find` / `grep` / `read`), instrumentation ledger from day one, self-hosting on this repo.

### Phase 2 — coding-agent core — current (ship 14)

Live: `write`, `edit`, `stat`, `diff`, `git` (status/log/diff/show), `test`, `bench`, `metrics`, `report`, `hook`, `help`, `init`, `uninit`, `stop`. Plus: configuration substrate (`ash.toml`), jail policy, ledger retention, in-process go-git backend, cross-repo report aggregation, PreToolUse hook with per-verb exclusions.

Upcoming for phase 2: `build`, `fmt`, `run`, `proc`, `obj`, persistent session/object store, job ledger for async operations, reference Go client library.

### Phase 3 — semantic layer

- `lang` verb backed by tree-sitter
- Symbol search, callers, definitions, references
- File outlines without bodies (token-efficient orientation)

### Phase 4 — adoption

- Claude Code skill for `ash`
- Codex CLI integration
- Cursor extension
- Benchmark suite (SWE-bench-Verified, Aider, Terminal-bench) comparing bash vs ash
- Token-cost case studies from real sessions

### Phase 5 — ecosystem

- Tool registration protocol (`ash.toml`) for third-party CLIs
- Adapters for the top 30 development tools
- Native protocol support in at least one major harness

## Why Go

Cross-compilation that just works. Single static binary per platform. Strong concurrency model for a daemon serving multiple agents. Stdlib breadth that minimizes dependencies. Ecosystem alignment — most agent-tooling projects ship in Go.

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
- Tree-sitter — the parsing infrastructure behind the upcoming `lang` verb.
- Git's plumbing/porcelain split — the original instinct that machine consumers deserve a stable, structured interface alongside the human one.

## Contributing

Alpha. Issues and design discussion are welcome. The design isn't settled and disagreement at this stage is more useful than agreement. PRs are welcome but the surface still moves under foot — open an issue first.

## License

MIT

---

*The README is the spec; the spec is the design; the design will change. Watch this space.*
