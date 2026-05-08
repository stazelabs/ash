# CLAUDE.md — agent guidance for the ash repo

This file is the operational counterpart to `README.md`. The README is the design; this is how an agent works *inside* the project. Keep it short, keep it current, and update it as the surface evolves.

## Project context

`ash` is an agentic shell for coding agents — a small, structured, instrumented command surface designed to replace the bash-shaped substrate that agents currently drive. The README has the full pitch.

This repo is also a deliberate experiment in *recursive* tool development: as soon as primordial `ash` exists (Phase 1: `find` + `grep` + `read` + daemon + bash shim + ledger), agents working on this repo start using `ash` for those operations. Every session feeds the next phase's design.

**Current phase:** Phase 1, ship 2 — `ash read` and `ash find` are live. Daemon (`ashd`) auto-starts on first invocation, persists per-call instrumentation to a SQLite ledger at `.ash/ledger.db`, and tokenizes every response with `cl100k_base` for honest token counts. Ledger failures surface in `Metrics.LedgerError` and the client prints a `WARNING` line if any call's metrics didn't persist.

## Project constraints

These are hard rules. If a change would violate one, stop and discuss before proceeding.

- **No CGO.** All dependencies must be pure Go. We're prioritizing portability — every developer should be able to clone, `go build`, and run, on any platform Go cross-compiles to, with no native toolchain required. This rules out `mattn/go-sqlite3`, CGO-bound tree-sitter, and similar. We've already paid the perf cost on SQLite (`modernc.org/sqlite`) and accept it.
- **All paths are explicit and absolute-friendly.** No verb relies on `cwd` for path resolution beyond the daemon's project root. The agent passes the full path it cares about; the daemon canonicalizes and validates. Beyond removing a class of mistakes, this gives us a "sandbox-lite" hook for free: a verb can refuse paths outside the project root, reject symlink-escapes, or apply per-path policy *before* the verb body runs. We're not a sandbox today, but we are deliberately keeping the affordance open.

## When to prefer ash over bash

This is the operational checklist. It is **gated by which verbs are live**. Do not try to invoke a verb that hasn't shipped yet.

### Phase 1 ship 2 (now) — `read` and `find` are live

Build the binaries first (one-time per session, cheap to redo):

```sh
go build -o bin/ash ./cmd/ash
go build -o bin/ashd ./cmd/ashd
```

Then any `ash` invocation auto-starts the daemon. Use `bin/ash` from the repo root, or add `bin/` to `$PATH` for the session.

**Switch criteria — what to actually use ash for now:**

1. **Any path or filename lookup across more than one directory** — use `ash find`. Examples:
   - "list all .go files under cmd/" → `ash find --path cmd --glob '**/*.go' --type file`
   - "show top-level files only" → `ash find --path . --max_depth 1`
   - "find anything related to ledger" → `ash find --path . --glob '**/*ledger*'`
   - Hidden directories (`.git`, `.ash`, `.vscode`) are skipped by default. Pass `--include_hidden true` if you actually want them.
   - `.gitignore` at the walk root is respected by default. Pass `--respect_gitignore false` for a raw walk (e.g. when you genuinely need to inspect generated artifacts in `bin/` or `dist/`).
2. **Reads of files in this repo** — use `ash read` deliberately at least a few times per session, even when the harness Read tool would suffice. We need ledger data to compare.
3. Bash `find`, `cat`, `head`, `tail`, `ls -R` should be replaced by their `ash` equivalents in this repo. Bash `grep` stays until ship 3.

**The whole point** is that you are the first user. If `ash find` or `ash read` errors, hangs, or feels heavier than the bash equivalent, that's a bug or a design gap — investigate, don't paper over. Write the session note.

### Phase 1 ship 3 (next) — `grep` will land here

When `ash grep` ships: any pattern search across more than one file uses `ash grep`. Until then, those operations stay in bash.

## How to invoke ash

```sh
ash <verb> [--key value | --key=value]...
```

The client is `bin/ash`; the daemon is `bin/ashd`. The client auto-starts the daemon on first invocation by exec'ing `bin/ashd` (sibling lookup, then `$PATH`, then `$ASH_DAEMON`). Subsequent calls reuse the same daemon over a per-project Unix domain socket.

State lives in:

- `.ash/ledger.db` — SQLite, one row per call.
- `.ash/ashd.log` — daemon stderr/stdout.
- `$XDG_RUNTIME_DIR/ash/` or `$TMPDIR` — UDS file (`ash-<8-byte-hash>.sock`).

The daemon also prints a one-line metrics summary to stderr on every call, which is what you see after the response body.

### Inspecting the ledger

```sh
sqlite3 .ash/ledger.db "SELECT verb, ok, tokens_in, tokens_out, latency_exec_us FROM calls ORDER BY id DESC LIMIT 20"
```

The ledger is the substrate for the recursive-development experiment. If a session feels heavy or surprising, query the ledger first — it almost certainly knows why.

### Live verbs

- `ash read --path <p> [--range start:end] [--range_kind lines|bytes] [--limit_bytes N]` — read a file (or a line/byte range of one). UTF-8 returned as-is; binary returned base64-encoded with `encoding=base64` in the response. Default size cap is 256 KiB.
- `ash find --path <p> [--glob <pattern>] [--type any|file|dir|symlink] [--max_depth N] [--limit N] [--exclude <pattern>] [--include_hidden true|false] [--respect_gitignore true|false]` — list paths under `<p>`. `glob` and `exclude` are doublestar patterns (`**` for recursive, `*.{go,md}` for alternation, etc.). `include_hidden` defaults to false (directories starting with `.` are skipped; leaf dotfiles like `.gitignore` are still findable). `respect_gitignore` defaults to **true** — the `.gitignore` at the walk root (`<p>`) is loaded and applied. Pass `--respect_gitignore false` for a raw filesystem walk. Nested `.gitignore` files are not yet honored. Default `limit` is 256, hard cap 4096; truncation produces a hint about how to narrow. Symlinks are reported but never followed.

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
- **Instrumentation.** When the ledger exists: paste the relevant rows, or summarize them. Numbers beat impressions.

Keep notes terse. A bullet list is fine. The goal is signal, not prose.

## Bash whitelist

Even after primordial `ash` ships, some operations stay in bash. Track them here so the dogfooding rule doesn't push agents into pretending verbs exist that don't.

- **`git`** — until the `git` verb lands in Phase 2, all version control is bash.
- **System package management** (`brew`, `apt`, `npm install -g`, etc.) — never in scope for `ash`.
- **Process management at the OS level** beyond what `proc` covers — bash.
- **Anything not yet implemented as a verb.** When in doubt: bash, with a session note explaining what verb you wished existed.

Update this list as verbs ship and as new bash-only operations are identified.

## Memory hygiene

This file evolves with the surface. **Do not trust stale guidance.** If the verb list in the README has changed but the checklist here hasn't, the checklist is wrong — fix it before relying on it. If you're a fresh agent reading this for the first time, cross-reference the README's "Roadmap" section against the "When to prefer ash" checklist and reconcile any drift before acting.

The single rule that does not change: when in doubt, use bash, write a session note, and let the next agent benefit from your friction.
