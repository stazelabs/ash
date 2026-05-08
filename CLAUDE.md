# CLAUDE.md — agent guidance for the ash repo

This file is the operational counterpart to `README.md`. The README is the design; this is how an agent works *inside* the project. Keep it short, keep it current, and update it as the surface evolves.

## Project context

`ash` is an agentic shell for coding agents — a small, structured, instrumented command surface designed to replace the bash-shaped substrate that agents currently drive. The README has the full pitch.

This repo is also a deliberate experiment in *recursive* tool development: as soon as primordial `ash` exists (Phase 1: `find` + `grep` + `read` + daemon + bash shim + ledger), agents working on this repo start using `ash` for those operations. Every session feeds the next phase's design.

**Current phase:** Phase 1, ship 1 — `ash read` is live. Daemon (`ashd`) auto-starts on first invocation, persists per-call instrumentation to a SQLite ledger at `.ash/ledger.db`, and tokenizes every response with `cl100k_base` for honest token counts.

## When to prefer ash over bash

This is the operational checklist. It is **gated by which verbs are live**. Do not try to invoke a verb that hasn't shipped yet.

### Phase 1 ship 1 (now) — `read` is live

Build the binaries first (one-time per session, cheap to redo):

```sh
go build -o bin/ash ./cmd/ash
go build -o bin/ashd ./cmd/ashd
```

Then any `ash` invocation auto-starts the daemon. Use `bin/ash` from the repo root, or add `bin/` to `$PATH` for the session.

**Use `ash read` deliberately at least a few times per session that touches the project**, even when the harness Read tool would suffice. The point right now is not that `ash read` outperforms Read — it doesn't yet. The point is to:

- Generate ledger data so we have something to compare against when `find` and `grep` ship.
- Notice friction in the wire format, the auto-start dance, the pretty-print, the args mapping.
- Build muscle memory for the dogfooding habit before the verbs that genuinely matter (`grep`, `find`) land.

Concrete checklist:

1. Do at least one `ash read` per session in this repo. Hit a small file (top of `README.md` or a Go file). Write a session note (see ritual below) about anything that felt off.
2. If `ash read` errors or hangs, that's a bug — investigate, don't paper over. The whole point of building it this way is that *you* are the first user.
3. Other reads (the harness Read tool, bash `cat`/`head`) are still completely fine. We're not replacing anything yet.

### Phase 1 ship 2-3 (next) — `find` and `grep` will land here

When `ash find` ships, this block grows: any path/glob lookup across more than one directory uses `ash find`. When `ash grep` ships: any pattern search across more than one file uses `ash grep`. Until then, those operations stay in bash.

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
