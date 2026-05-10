# CLAUDE.md — agent guidance for the ash repo

This file is the operational counterpart to [README.md](README.md). The README is the design and the human-facing pitch; this is how an agent works *inside* the project. Keep it short, keep it current, and update it as the surface evolves.

## Project context

`ash` is an agentic shell for coding agents — see [README §Why](README.md#why) and [§How we're building this](README.md#how-were-building-this) for the pitch and the recursive-development premise.

**Current phase:** Phase 2, ship 14. 17 verbs live (run `ash help` for the authoritative list and per-verb arg schemas). Daemon (`ashd`) auto-starts on first invocation, persists per-call instrumentation to a SQLite ledger at `.ash/ledger.db`, and tokenizes every response with `cl100k_base` for honest token counts.

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

1. **Any path or filename lookup across more than one directory** — use `ash find`. Canonical: `ash find --path <p> --glob '**/*.go' --type file`. Hidden directories (`.git`, `.ash`, `.vscode`) are skipped by default; `.gitignore` at the walk root is respected. Override with `--include_hidden true` / `--respect_gitignore false`. Run `ash help --verb find` for the full schema.

2. **Any pattern search across one or more files** — use `ash grep`. Canonical: `ash grep --pattern '<re>' --path <p> --glob '**/*.go'`. Smart-case by default (uppercase letter switches to case-sensitive). Add `--files_only true` for a path-only result, `--no_text true` for `path:line:col` rows without excerpts (cheap-form), `--fixed_string true` to escape regex metachars, `--context_before/after N` for surrounding lines. `ash help --verb grep` for the full schema.

3. **`git status`, `git log`, `git diff`, and `git show` in this repo** — use `ash git --op status|log|diff|show`. Canonical examples: `ash git --op status`, `ash git --op log --limit 20`, `ash git --op diff --range HEAD~1..HEAD --stat true`, `ash git --op show --ref HEAD`. Status splits into `staged`/`unstaged`/`untracked`/`conflicts`; log emits structured `Commit` records; diff/show return per-file `{path, status, additions, deletions, patch}` with a `limit_bytes` cap (default 256 KiB). Other git ops (`blame`, `commit`, `push`, `reset`, etc.) stay in bash until they ship under `--op`. `ash help --verb git` for the full schema.

4. **Reads of files in this repo** — use `ash read` deliberately, even when the harness Read tool would suffice. Canonical: `ash read --path <p> --range 100:200`. Default cap 256 KiB; UTF-8 returned as-is; binary base64-encoded. We need ledger data to compare. `ash help --verb read` for the full schema.

5. **Ledger queries** — use `ash report` for synthesis, `ash metrics` for raw rows. `ash report --since 1h` is the most common form when a session feels heavy. `--top N` controls the truncation-hotspot / error-histogram cap. Cross-repo: `--root <p>` reads a foreign repo's ledger; `--all_roots true` aggregates across every `ash init`'d repo. `ash help --verb report` / `--verb metrics` for the full schemas.

6. **Filesystem metadata for explicit paths** — use `ash stat`. Canonical: `ash stat --paths a.go,b.go,internal/`. Uses `lstat` (symlinks report as symlinks). Per-entry `error` (`not_found` / `permission` / `broken_symlink`) keeps a bulk call alive when some paths are missing. Pass `--with_meta true` to include mode + mtime in the pretty rows. `ash help --verb stat` for the full schema.

7. **Writing files in this repo** — use `ash write` instead of the harness Write tool. (Do not fall back to harness `Write`: it requires a prior harness `Read`, which the hook also denies — go straight to `ash write`.) Canonical: `ash write --path <p> --content - << 'EOF' … EOF` for non-trivial content; `--content '…'` only for short ASCII-only writes. Atomic via temp-file+rename. `ash help --verb write` for the full schema.

8. **Editing files in this repo** — use `ash edit`. Three modes: string-replacement (`--old_string` / `--new_string`), line-range (`--range start:end --new_content`), or unified-diff (`--patch`). Canonical: `ash edit --path <p> --range 5:10 --new_content - << 'EOF' … EOF`. Errors `match_not_found` / `ambiguous` are signal — be more specific or pass `--replace_all true`. Add `--dry_run true` to preview as a unified diff. `ash help --verb edit` for the full schema.

   **Shell quoting — default to stdin.** `--old_string`, `--new_string`, `--new_content`, and `--patch` are all shell arguments and silently corrupt content containing backticks, single quotes, backslashes, escape sequences, or multiline blocks. **Default to stdin** for any non-trivial replacement, in this order: (a) `ash edit --path f.go --range 5:10 --new_content - << 'EOF' … EOF` for line-range edits when you know the line numbers; (b) `ash edit --path f.go --patch - << 'EOF' … EOF` for cross-cutting or multi-region edits; (c) `ash write --path f.go --content - << 'EOF' … EOF` for whole-file rewrites. Inline `--…='…'` is for short ASCII-only swaps with no quoting hazards. As a last resort for hostile content, write a Python fixer via `ash write --path /tmp/fix.py --content - << 'EOF' … EOF` then `python3 /tmp/fix.py` — Python string concat sidesteps all shell quoting.

9. **Diffing content in this repo** — use `ash diff`. Canonical: `ash diff --path a.go --other b.go` or `ash diff --path f.go --content - < new.go`. Add `--stat true` for token-cheap counts only. Both inputs capped at 2000 lines. `ash help --verb diff` for the full schema.

10. **Running Go tests** — use `ash test` instead of `go test`. Canonical: `ash test` (defaults to `./...`, `count=1` to bypass cache, 60s timeout). Add `--packages internal/walker` for one package, `--run TestX` for name filter, `--race true` for race detector, `--short true` for `-short` mode, `--timeout 10m` for big suites. Failures arrive as a structured `Tests []Test` slice with `file:line` extracted; build failures land as `Status=build_failed`. `ash help --verb test` for the full schema.

11. **Bash equivalents to retire** — `find`, `cat`, `head`, `tail`, `ls -R`, `grep`, `rg`, `git status`, `git log`, `git diff`, `go test`, `stat` should be replaced by their `ash` equivalents in this repo. The PreToolUse hook (next subsection) enforces this.

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

## Gotchas

Hard-won wisdom from real session friction. Read these once; they save tuition.

- **Shell quoting on `ash edit`/`ash write` is the #1 footgun.** Inline string args silently corrupt backticks, single quotes, backslashes, escape sequences, and multiline content. Detailed rules live in switch criterion 8 above; the short version: **default to stdin** (`--content -`, `--new_content -`, `--patch -`) for any non-trivial content. Confirmed by ASH-48 / ASH-60 / ASH-63 session friction.

- **Daemon stickiness — config changes don't hot-reload.** Edits to `ash.toml` (jail policy, git backend, daemon limits) take effect only on daemon restart. Run `ash stop`; the next `ash` invocation auto-starts a fresh daemon. Don't `pkill ashd` (it bypasses graceful shutdown). If a verb behaves as though it's running with old config, this is the first thing to check.

- **Path-form semantics differ across verbs.** `ash find` and `ash grep` mirror the input form: relative `--path` produces relative output paths, absolute produces absolute. `ash git --op *` always returns repo-root-relative paths regardless of `--path`. Don't assume one rule covers all verbs.

- **Ledger-first debugging.** When a session feels slow or surprising, run `ash report --since 1h` before reaching for `sqlite3` or strace. Sub-phase columns (`walk_us` / `io_us` / `regex_us` in `ash metrics` rows) usually tell you exactly which phase is heavy.

- **`ash help` text can lag code.** ASH-33 swept several mismatches; new ones may appear. If a flag's behavior surprises you, verify against [internal/verbs/](internal/verbs/) source and write a session note. Help is authoritative for arg names and types but not for every defaulted edge case.

- **`ash read --range` end is clamped, start is not.** Out-of-bounds end clamps silently to file length (the result reports actual bytes returned); out-of-bounds start returns `range_out_of_bounds` (ASH-57 made the start-side strict so an obviously wrong call fails loudly instead of returning empty).

- **Hook has known-bad cases worth knowing.** `cat > FILE << EOF` produces a malformed `ash read --path '>'` suggestion (ASH-69). When you need to stage multiline content, pipe the heredoc directly into `ash write --content -` instead of using a temp file.

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

The single rule that doesn't change: when in doubt, use bash, write a session note, and let the next agent benefit from your friction.
