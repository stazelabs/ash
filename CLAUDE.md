# CLAUDE.md — agent guidance for the ash repo

This file is the operational counterpart to `README.md`. The README is the design; this is how an agent works *inside* the project. Keep it short, keep it current, and update it as the surface evolves.

## Project context

`ash` is an agentic shell for coding agents — a small, structured, instrumented command surface designed to replace the bash-shaped substrate that agents currently drive. The README has the full pitch.

This repo is also a deliberate experiment in *recursive* tool development: as soon as primordial `ash` exists (Phase 1: `find` + `grep` + `read` + daemon + bash shim + ledger), agents working on this repo start using `ash` for those operations. Every session feeds the next phase's design.

**Current phase:** Phase 0 — design and scaffolding. No working `ash` binary yet. Until that changes, the "use ash" guidance below is forward-looking.

## When to prefer ash over bash

This is the operational checklist. It is **gated by which verbs are live**. Do not try to invoke a verb that hasn't shipped yet.

### Phase 0 (now) — no ash binary exists

Use bash and the standard agent toolset (Read, Grep, Edit, Write, Bash) for everything. There is nothing to switch to. While doing so, notice:

- Operations that felt token-heavy (large outputs, repeated re-greps).
- Operations that required platform-specific incantations (BSD vs GNU `find`/`sed`).
- Workflows where you re-parsed the same data twice in different shapes.

These notes inform Phase 1 design — write them down (see "Session feedback ritual" below).

### Phase 1 (after primordial ash ships) — checklist will go here

Placeholder. Once `ash find`, `ash grep`, and `ash read` exist, this section will spell out:

1. Which bash invocations get replaced (e.g. multi-file `grep -r` → `ash grep`).
2. The fallback rule when an `ash` verb hits a limit it can't handle.
3. How to record the switch in the session ledger.

Do not edit this block speculatively — fill it in when the verbs actually land.

## How to invoke ash

Placeholder for Phase 1+. Until then, `ash` is not invokable. The expected shape (subject to change as we build):

```sh
ash <verb> [--flag value] ...
```

A daemon (`ashd`) runs per-project; the `ash` CLI is a thin client that speaks MessagePack over a Unix domain socket. Agents will not need to manage the daemon lifecycle by hand — startup is automatic on first invocation.

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
