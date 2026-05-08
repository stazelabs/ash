# 2026-05-08 — `ash git --op log`: parallel-session collision and the toInt bug

## What happened

User asked me to "keep going to `--op log`" after shipping `--op status`. I started designing and implementing it, only to discover during smoke-testing that another session (Sonnet 4.6, commit `8a6c8c5 phase 2 ship 5b: ash git --op log`) had already landed it earlier the same day, while a third session (`9cdb966 ASH-report`) had landed an `ash report` verb in parallel.

Almost everything I wrote — the file split into `git.go`/`status.go`/`log.go`, the `--format=...%n...%b` parser, the `LogResult`/`Commit` shapes, the empty-repo handling, the `limit+1` truncation trick, the integration tests — was byte-identical or near-identical to what was already in HEAD. The Write tool happily clobbered the existing files with the same content, so git only showed me the *real* divergence at commit time.

## The actual contribution

One genuine bug fix: `internal/verbs/git/git.go`'s `toInt` was missing a `string` case. The wire delivers numeric flags from `parseFlags` as strings (every `--key value` is `string`), so `--limit 5` came in as `"5"`. The toInt without the string arm returned `(0, false)`, and ParseArgs then errored with `"limit must be a positive integer"`. Caught it during smoke-testing the parallel-shipped log; one switch arm fixes it.

Other verbs (find, grep, metrics) have the string arm in their toInt — git was simply missing it.

## Friction (real)

1. **Parallel-session collision is invisible until commit time.** Three sessions worked on overlapping git verb work today and I had no signal in-session that 5b was already done. Editing files that already exist on disk just looks like normal editing; only `git status` revealed how much of "my" work was actually no-op overwrites. Lessons:
   - Before a non-trivial new verb, check `git log --since '1 day ago'` first. (Ironically this is now `ash git --op log --since '1 day ago'`.)
   - The agent asking "what's next?" should explicitly check recent commits, not just session notes and Linear backlog.
   - Concurrent agent sessions on the same repo are a real failure mode in this project's recursive-development setup. Worth a process note.

2. **toInt-string bug.** Real bug, real fix. Caught only by smoke-testing — the unit tests (and probably the parallel session's tests) only constructed Args directly with int literals, never going through ParseArgs's wire-shape coercion. Passing `int(5)` to ParseArgs takes the `case int:` path and works fine; the failure surfaces only end-to-end. Lesson: ParseArgs deserves test coverage for *every* numeric arg with a `string` value, since that's what the daemon actually receives. Worth a follow-up to add string-case ParseArgs tests across all verbs.

3. **No-CGO concurrent-session detection.** Long-term, the daemon could lock per-project to prevent concurrent ash invocations from stepping on each other, but the daemon doesn't see *git* operations directly — those are bash. The drift comes from concurrent *Claude sessions*, not concurrent *ash* sessions. Probably out of scope for ash itself.

## Suggestions

- **Shared `argutil`** (still pending from ship-5's session note observation). Five verbs duplicate `toInt`/`toBool`/`toInt64` and at least one of the copies — git's — was missing the `string` case. The argutil extraction would have prevented this entire bug.

- **Session-start check.** Before claiming "ship 6 will be X," run `ash git --op log --since '1 day ago'` (now possible!) to see what landed since the last session. Saves duplicate work.

- **ParseArgs coverage gap.** Test every numeric/bool arg with a string-typed value to catch wire-shape bugs that direct-Run tests miss.

## Outcome

Committing only the toInt fix and this note. The "ship 6" work itself was done in `8a6c8c5`; this commit makes it actually work via the wire.
