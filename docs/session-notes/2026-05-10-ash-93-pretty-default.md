# Session: ASH-93 — pretty is the default everywhere

**Task.** Fix `--format pretty` defaulting incorrectly to JSON envelope in non-TTY contexts for row-shaped verbs.

**Verbs used.** ash read, ash grep, ash edit, ash find, ash test.

**Root cause.** `cmd/ash/main.go` promoted row verbs (`find`, `grep`, `metrics`, `report`, `stat`, `git`, `test`) to `--format compact` unless `--format` was explicitly passed. This meant agents (running via a non-TTY harness) received the JSON envelope rather than the pretty list. Meanwhile `ash bench` tokenized pretty output via `d.Pretty()` — bench numbers were measuring a different surface than what agents actually consumed.

**Fix (Option A).** Removed the `rowVerbs` map and the `!fmtSpecified && rowVerbs[verb]` promotion in `main.go`. Pretty is now unconditionally the default; `--format compact` and `--format json` remain available as opt-in. The bench already uses `d.Pretty()` — no bench changes needed. The format-vs-tokenization invariant is now explicit in `docs/cli-tokens.md`.

**Verbs changed.** `cmd/ash/main.go` only (–11 lines).

**Friction.** None. Clean diagnosis from the ticket; one-file fix.

**Instrumentation.** 33/33 tests pass. Verified `ash find --path docs --glob '**/*.md'` in non-TTY bash outputs the pretty list header and paths (not JSON envelope) after rebuild.

**Suggestion.** Consider adding a golden-file or integration test that asserts `ash find` stdout starts with `=== ash find:` when invoked without `--format`, covering the regression.
