# Session note — 2026-05-08 — prefer-ash hook stops denying Read

## Task
Fix the catch-22 surfaced by the prior ASH-13 session: the `prefer-ash.py`
hook denied the harness `Read` tool for text files, but the harness
`Edit`/`Write` tools require a prior native `Read` to satisfy their
internal "file has been read" guard. Net effect: editing any Go file in
this repo forced agents into bash workarounds (`tee` heredocs,
`python3 -c "open(...).write(...)"`) — exactly the kind of friction the
hook was supposed to *remove*.

## Verbs used
`ash read`, `ash find`, `ash grep` (plus a one-shot bootstrap `python3`
heredoc to patch the hook itself, since the hook was actively blocking
the harness `Read` needed to call `Edit` on the hook).

## Decision
Drop the `Read` handler from the hook. CLAUDE.md prose plus ledger
inspection (`ash report`) carry the dogfooding pressure for `ash read`
on pure-exploration reads. Bash `cat`/`head`/`tail` denials still
funnel exploratory reading through `ash`. `Grep`/`Glob` denials are
unaffected.

## Why
- `Read` is load-bearing for the harness `Edit`/`Write` workflow. Hard-
  denying it makes the hook hostile, not directive — the agent has to
  go *around* the harness to do legitimate work, and the resulting bash
  contortions don't go through the ledger anyway.
- The dogfooding signal we actually care about — "did the agent reach
  for `ash` for exploration?" — is still preserved, because bash
  `cat`/`head`/`tail` and the harness `Grep`/`Glob` are still denied.
- Friction-as-feature only works if the friction has a productive exit.
  This one didn't.

## Trade-off
We lose forced telemetry on text-file reads via the harness `Read`
tool — some agent reads will now bypass the `ash read` ledger row. Net
positive vs. agents resorting to undignified bash hacks for every edit.

## What shipped
- [.claude/hooks/prefer-ash.py](../../.claude/hooks/prefer-ash.py): removed
  `handle_read`, `NON_TEXT_EXTS`, and the `Read` dispatch in `main()`.
  Updated module docstring to explain why `Read` is intentionally not
  denied.
- [.claude/settings.json](../../.claude/settings.json): matcher narrowed
  from `Grep|Glob|Read|Bash` to `Grep|Glob|Bash` — no point invoking the
  hook on `Read` if it always allows.
- [docs/PreToolUse.md](../PreToolUse.md): behavior matrix and dispatch
  bullets updated; verification snippet trimmed; new "Why Read isn't
  denied" subsection capturing the catch-22 reasoning so the next agent
  doesn't re-litigate it.

## Suggestions for next phase
- Eventually ship `ash edit` / `ash write` verbs so writes flow through
  the ledger too. Then `Read`/`Edit`/`Write` can all be redirected
  symmetrically and the catch-22 goes away structurally.
- Until then, accept the gap.
