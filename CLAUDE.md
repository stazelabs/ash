# CLAUDE.md — agent guidance for the ash repo

This file is the operational counterpart to [README.md](README.md). The README is the design and the human-facing pitch; this is how an agent works *inside* the project. Keep it short, keep it current, and update it as the surface evolves.

## Project context

`ash` is an agentic shell for coding agents — see [README §Why](README.md#why) and [§How we're building this](README.md#how-were-building-this) for the pitch and the recursive-development premise.

**Current phase:** Phase 2, ship 14. 17 verbs live (run `ash help` for the authoritative list and per-verb arg schemas). Daemon (`ashd`) auto-starts on first invocation, persists per-call instrumentation to a SQLite ledger at `.ash/ledger.db`, and tokenizes every response with `cl100k_base` for honest token counts. A third binary, `ashmcp`, exposes the read-side verbs as MCP tools over stdio (ASH-104) so MCP-aware harnesses see `ash_read`, `ash_grep`, etc. alongside their built-ins; it dispatches to the same `ashd` over the per-project UDS and is recorded in the same ledger.

## Project constraints

See [README §Constraints](README.md#constraints) for the full list. The two operational rules that bite agents most often:

- **No CGO.** All deps must be pure Go. Don't propose `mattn/go-sqlite3` or CGO-bound tree-sitter, even when they're faster.
- **All paths are explicit and absolute-friendly.** Pass full paths; the daemon canonicalizes. With `[jail].enabled = true` in `ash.toml`, paths outside the project root are denied with `path_denied`.

## Configuration

See [README §Configuration](README.md#configuration) and [`ash.toml.example`](ash.toml.example) for the schema. Restart with `ash stop` after editing `ash.toml` — hot reload is deferred.

### Error codes touching this

- `path_denied` — verb path arg fell outside the active jail policy.
- `not_implemented` — reserved for future ops a backend genuinely cannot perform. No live verb returns this today.

## When to prefer ash over bash

This is the operational checklist. It is **gated by which verbs are live**. Run `ash help` if unsure whether a verb has shipped.

Build the binaries first (one-time per session, cheap to redo):

```sh
make all   # or: go build -o bin/ash ./cmd/ash && go build -o bin/ashd ./cmd/ashd
```

Any `ash` invocation auto-starts the daemon. Use `bin/ash` from the repo root, or add `bin/` to `$PATH` for the session.

**Switch criteria — what to actually use ash for now:**




4. **Reads of files in this repo** — use `ash read` deliberately, even when the harness Read tool would suffice. Canonical: `ash read --path <p> --range 100:200`. Default cap 256 KiB; UTF-8 returned as-is; binary base64-encoded. We need ledger data to compare. `ash help --verb read` for the full schema.



7. **Writing files in this repo** — use `ash write` instead of the harness Write tool. (Do not fall back to harness `Write`: it requires a prior harness `Read`, which the hook also denies — go straight to `ash write`.) Canonical: `ash write --path <p> --content - << 'EOF' … EOF` for non-trivial content; `--content '…'` only for short ASCII-only writes. Atomic via temp-file+rename. `ash help --verb write` for the full schema.



9. **Diffing content in this repo** — use `ash diff`. Canonical: `ash diff --path a.go --other b.go` or `ash diff --path f.go --content - < new.go`. Add `--stat true` for token-cheap counts only. Both inputs capped at 4000 lines. `ash help --verb diff` for the full schema.

10. **Running Go tests** — use `ash test` instead of `go test`. Canonical: `ash test` (defaults to `./...`, `count=1` to bypass cache, 60s timeout). Add `--packages internal/walker` for one package, `--run TestX` for name filter, `--race true` for race detector, `--short true` for `-short` mode, `--timeout 10m` for big suites. Failures arrive as a structured `Tests []Test` slice with `file:line` extracted; build failures land as `Status=build_failed`. `ash help --verb test` for the full schema.

11. **Bash equivalents to retire** — `find`, `cat`, `head`, `tail`, `ls -R`, `grep`, `rg`, `git status`, `git log`, `git diff`, `go test`, `stat`, `sed` should be replaced by their `ash` equivalents in this repo. The PreToolUse hook (next subsection) enforces this. (`sed` routes to `ash edit` for in-place edits and `ash read --range` for line-range reads; pure pipeline `cmd | sed …` is allowed through.)

12. **Restarting the daemon** (after editing `ash.toml`, or after a rebuild) — use `ash stop`. The next `ash` invocation auto-starts a fresh daemon. Don't reach for `pkill ashd`.

**The whole point** is that you are the first user. If a verb errors, hangs, or feels heavier than the bash equivalent, that's a bug or design gap — investigate, don't paper over. Write the session note.

### Enforcement

The repo ships a `PreToolUse` hook (registered in [.claude/settings.json](.claude/settings.json)) that runs `ash hook` to deny the harness's built-in `Grep`/`Glob`/`Edit`/`Write`/`Read` tools and bash `grep`/`rg`/`find`/`cat`/`head`/`tail`/`ls -R`/`git status`/`git log`/`stat` in this project, returning the equivalent `ash` invocation as the deny reason. Image/PDF/notebook reads are allowed through (`ash read` can't render them). `git blame`/`commit`/etc. and other not-yet-shipped ops are allowed through. See [docs/PreToolUse.md](docs/PreToolUse.md) for the full design and behavior matrix.

`ash hook` is the only client-only verb in ash with ledger instrumentation: the deny decision runs in-process for low latency, then a best-effort fire-and-forget request to the daemon writes a ledger row when the daemon is up. Hook denials are queryable via `ash report --verb hook`. (`ash stop` is also fully client-side — it cannot contact the daemon it is stopping.)

If `ash` genuinely doesn't fit (a verb that hasn't shipped, a non-text artifact, etc.), the hook is best-effort — when it gets in the way, that is a session-note finding, not a hook bug to "work around" with `--no-verify`-style escape hatches.

## How to invoke ash

```sh
ash <verb> [--key value | --key=value]... [--format pretty|json|msgpack]
```

`--format` is a global client flag stripped before the request hits the daemon. `pretty` (default) is human-readable; `json` emits the full response envelope; `msgpack` writes raw wire bytes.

The client is `bin/ash`; the daemon is `bin/ashd`. The client auto-starts the daemon on first invocation by exec'ing `bin/ashd` (sibling lookup, then `$PATH`, then `$ASH_DAEMON`). Subsequent calls reuse the same daemon over a per-project Unix domain socket.

State lives in:

- `.ash/ledger.db` — SQLite, one row per call.
- `.ash/ashd.log` — daemon stderr/stdout.
- `$XDG_RUNTIME_DIR/ash/` or `$TMPDIR` — UDS file (`ash-<8-byte-hash>.sock`).

The daemon prints a one-line metrics summary to stderr on every call.

### Inspecting the ledger

Use `ash report` for synthesis, `ash metrics` for raw rows — no `sqlite3` required:

```sh
ash report                         # per-verb summary for this session
ash report --since 1h              # last hour
ash report --verb grep             # drill into one verb
ash report --top 10                # raise the truncation/error histogram cap
ash metrics --last 50              # raw rows
ash metrics --verb find            # only find rows
```

If you need raw SQL access these verbs can't express:

```sh
sqlite3 .ash/ledger.db "SELECT verb, ok, tokens_in, tokens_out, latency_exec_us FROM calls ORDER BY id DESC LIMIT 20"
```

The ledger is the substrate for the recursive-development experiment. If a session feels heavy or surprising, query the ledger first — it almost certainly knows why.

## Token cross-validation

`cl100k_base` is a fast local proxy for Claude's tokenizer, but the two disagree on edge cases. To confirm a substitution actually saves *Claude* tokens (not just cl100k ones), use the encexplore validate harness:

1. Copy [`.env.example`](.env.example) to `.env` (or `.env.local` — both are gitignored; `.env.local` wins if both exist) and fill in `ANTHROPIC_API_KEY`.
2. Run `make validate`. The target builds `bin/encexplore`, sources the env file, and writes [`testdata/validate_results.md`](testdata/validate_results.md) with per-rule cl100k vs Claude deltas and an agreement marker.

The model is pinned to `claude-sonnet-4-5` in the Makefile so the cross-check baseline doesn't drift session-to-session. A `✗` marker means the cl100k delta and the Claude delta disagree in sign — the rule "saves tokens" in one tokenizer but adds them in the other. Investigate before claiming a win.

Cost: ~160 `count_tokens` calls in the default run, cached by body — usually many fewer. The endpoint is cheap, not free. Run `make validate` after non-trivial header/footer/error-string changes (ASH-100, ASH-114, future token-shape work) before claiming a saving.

`make validate-check` is the no-API-key companion gate: it parses the checked-in `testdata/validate_results.md` for `✗` rows (sign disagreement between cl100k Δ and Claude Δ) and exits non-zero if any are present. Pair it with `make validate` in the same way `make vocab-check` pairs with `make vocab` — regenerate, then gate.

## Gotchas

Hard-won wisdom from real session friction. Read these once; they save tuition.


- **Daemon stickiness — config changes don't hot-reload.** Edits to `ash.toml` (jail policy, git backend, daemon limits) take effect only on daemon restart. Run `ash stop`; the next `ash` invocation auto-starts a fresh daemon. Don't `pkill ashd` (it bypasses graceful shutdown). If a verb behaves as though it's running with old config, this is the first thing to check.

- **Path-form semantics differ across verbs.** `ash find` and `ash grep` mirror the input form: relative `--path` produces relative output paths, absolute produces absolute. `ash git --op *` always returns repo-root-relative paths regardless of `--path`. Don't assume one rule covers all verbs.

- **Ledger-first debugging.** When a session feels slow or surprising, run `ash report --since 1h` before reaching for `sqlite3` or strace. Sub-phase columns (`walk_us` / `io_us` / `regex_us` in `ash metrics` rows) usually tell you exactly which phase is heavy.

- **`ash help` text can lag code.** ASH-33 swept several mismatches; new ones may appear. If a flag's behavior surprises you, verify against [internal/verbs/](internal/verbs/) source and write a session note. Help is authoritative for arg names and types but not for every defaulted edge case.

- **`ash read --range` end is clamped, start is not.** Out-of-bounds end clamps silently to file length (the result reports actual bytes returned); out-of-bounds start returns `range_out_of_bounds` (ASH-57 made the start-side strict so an obviously wrong call fails loudly instead of returning empty).

- **Hook redirects `cat`/`echo`/`printf`/`tee` + `>` to `ash write`.** Bare output-redirection write idioms (`cat > FILE << EOF`, `echo "x" > FILE`, `printf '...' > FILE`, `tee >> FILE`) deny under rule `Bash:redirect-write` and suggest `ash write --path FILE --content - << 'EOF'` (ASH-69). Skip the temp-file dance: pipe heredocs directly into `ash write --content -`.

## Session feedback ritual

This is the most important habit in this repo. The whole point of building `ash` recursively is that real session experience drives design. If we lose the feedback loop, the project loses its edge over top-down spec work.

**When to write a note.** Any session that touches `ash` (using it, debugging it, working around it, designing the next verb). When in doubt, write the note — it costs nothing and the project is still small enough that every data point matters.

**Where notes live.** `docs/session-notes/YYYY-MM-DD-<slug>.md` — one file per session, dated and slugged.

**What a note should contain.**

- **Task.** One line: what was the agent trying to do?
- **Verbs used.** Which `ash` verbs (or bash equivalents) were used.
- **Friction.** Where did `ash` fall short? Where did bash feel heavier than it needed to?
- **Workarounds.** Bash incantations or extra steps the agent reached for.
- **Suggestions.** New verbs, new flags, behavior changes — anything the experience pointed at.
- **Instrumentation.** Paste the relevant ledger rows, or summarize them. Numbers beat impressions.

Keep notes terse. A bullet list is fine. The goal is signal, not prose.

## Bash whitelist

Even after primordial `ash` ships, some operations stay in bash. Track them here so the dogfooding rule doesn't push agents into pretending verbs exist that don't.

- **`git` ops other than `status`, `log`, `diff`, `show`** — `blame` and all destructive ops (commit/push/reset/rebase/checkout/etc.) stay in bash until they ship under `ash git --op <name>`.
- **`go build`, `go vet`** — until `build` lands in Phase 2, build/vet orchestration stays in bash. `go test` is now `ash test`.
- **System package management** (`brew`, `apt`, `npm install -g`, etc.) — never in scope for `ash`.
- **Process management at the OS level** — bash. (`proc` hasn't shipped yet.)
- **Anything not yet implemented as a verb.** When in doubt: bash, with a session note explaining what verb you wished existed.

Update this list as verbs ship and as new bash-only operations are identified.

## Memory hygiene

This file evolves with the surface. **Do not trust stale guidance.** If [README §The verb surface](README.md#the-verb-surface) has changed but the checklist here hasn't, the checklist is wrong — fix it before relying on it. Cross-reference `ash help` (authoritative) against the "When to prefer ash" list and reconcile any drift before acting.

For the *output* surface — every stable agent-facing string ash emits, with cl100k token costs and source-code locations — [docs/vocab/inventory.md](docs/vocab/inventory.md) is the authoritative inventory. It is checked in and regenerated by `make vocab`; `make vocab-check` fails when the inventory drifts from the code. Run `make vocab` after editing a verb schema, adding/renaming an error code, or changing a pretty header/label, and commit the regenerated artifact alongside your change.

For the *MCP tool surface* — JSON Schema draft 2020-12 tool definitions derived from the same `help.Registry()` — [docs/mcp/tools.json](docs/mcp/tools.json) is the canonical checked-in artifact (ASH-105). A byte-identical copy lives at [cmd/ashmcp/tools.json](cmd/ashmcp/tools.json) because `//go:embed` cannot reach outside the package directory; ASH-104 (`ashmcp`) embeds the in-package copy. `make schema` regenerates both at once and `make schema-check` is the CI drift gate that catches either drifting. Regenerate after any verb-schema edit (same trigger as `make vocab`).

The single rule that doesn't change: when in doubt, use bash, write a session note, and let the next agent benefit from your friction.
