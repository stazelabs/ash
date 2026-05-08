# 2026-05-08 — Ship 5: `ash git --op status` (Phase 2 begins)

## Task

First Phase-2 verb. Ship `git` as a structured replacement for one of the most-used bash commands in any agent session. Scope kept tight: `--op status` only, with `log`/`diff`/`blame` queued for follow-up ships.

## Verbs used

- `ash git --op status` (new), with `--ignored true` and `--path /tmp` (error path) variants.
- `ash metrics --last 4 --verb git` to confirm sub-phase latency was captured for the new verb.
- Bash `git status --porcelain=v2 --branch` to inspect the wire format I'd be parsing.

## Friction

1. **Subcommand vs --op shape.** README example uses `git diff --range HEAD~1..HEAD --summary` (positional subcommand). Our client rejects positional args (every other verb is flags-only). Two options: special-case git or use `--op <subcommand>`. Picked `--op` for consistency. Documented the README inconsistency in CLAUDE.md so a future agent doesn't re-litigate.

2. **Porcelain-v2 unmerged-entry field count.** I copy-pasted the format string `<XY> <sub> <m1> <m2> <m3> <mW> <h1> <h2> <h3> <path>` and counted 11 fields. Actually 10 (XY+sub+3 modes+mW+3 hashes+path = 10). My SplitN limit was off-by-one and the conflict test failed. Counting fields in a copied format-string is a known footgun; next time write a single canonical example, count it once, and reference *that* in code comments.

3. **Tests-with-git in CI.** The integration test `git init`s a temp repo. Skips if `git` not on PATH. Today every dev machine has git; CI usually does too. Worth a session note: if we ever target a "no git" CI image (e.g., minimal Alpine), this test silently skips and leaves the exec layer uncovered. Not a problem yet, but flagging.

4. **Path-form-mirrors-input doesn't apply here.** find/grep emit paths in the form the caller passed in (relative-in → relative-out). git status emits paths *relative to the repo root*, regardless of what `--path` looked like. That's correct for git semantics but breaks the symmetry the other verbs share. Mention in the verb's doc; don't try to "fix" it because it would conflict with how downstream `git apply`-style work would interpret these paths.

## Workarounds

None — all bugs were caught in test or smoke. The only "workaround" is `--op` instead of positional `git status`.

## Suggestions

In rough priority order:

- **`--op log` next ship.** Sketch: `ash git --op log [--path <p>] [--limit N] [--range <rev>] [--author <s>] [--since <d>]`. Result: `[]Commit` with full+short SHA, author/committer name+email+time, subject, body, parents. Parse `git log --format=...` with a stable separator. ~250 lines plus tests.

- **`--op diff` after that.** Bigger surface. Two modes worth supporting: file-level summary (`git diff --stat` shape) and hunk-level (`git diff --raw --patch` shape). The agent value is high — bash `git diff` parsing is one of the most tedious tasks in code review.

- **Path-form note.** Document explicitly in the help schema that git's `path` arg is "any path inside a work tree" and that returned paths are repo-root-relative. Currently it's described as `Repository path`, which doesn't say enough.

- **Detached/initial branch handling.** Today `Detached: true` and `Branch: ""` is the only signal for detached HEAD; pretty-render says "HEAD detached" but the structured caller has to check both fields. Consider adding a `Head` field that's always populated (branch name OR commit short-sha) so a downstream caller has one canonical "where am I" string.

- **Concurrent git invocations.** Today the daemon runs one verb call per request handler goroutine; nothing prevents two simultaneous status calls in different repos. `os/exec` is goroutine-safe, but if we ever cache parsed-tree state per repo we'll need to guard. Not blocking; flag for the day someone profiles concurrency.

- **Unify "shell out" pattern across verbs.** If `git log/diff/blame` and (eventually) `test/build/fmt` all shell out to system tools, an `internal/runner` helper that handles exec.LookPath, captures stdout/stderr, maps "command not found" / "exit code != 0" to typed proto errors, and times via the tracer would dedupe a lot. Not for ship 5; flag for ship 7+ when the third shell-out user lands.

## Instrumentation

5 git calls during smoke (see `ash metrics --last 5 --verb git` after the rebuild):

| call | shape | tokens_in | tokens_out | exec_us | io_us | notes |
|---|---|---|---|---|---|---|
| 1 | `--op status` (cold) | 5 | 62 | 41,312 | 41,168 | first call after fresh daemon |
| 2 | `--op status --ignored true` | 8 | 73 | 36,213 | 36,119 | warm-ish |
| 3 | `--op status --path /tmp` (err) | 9 | 12 | 17,883 | 17,811 | git exits non-zero quickly |
| 4 | `--op log` (unknown) | 5 | 14 | 0 | 0 | rejected before exec |

Observations:

- **io_us ≈ exec_us.** ~99% of exec time is `git` itself. We're paying git's cost; nothing to optimize here.
- **40 ms cold, 36 ms warm.** Real git takes ~30-40 ms on this 12-file repo. Our overhead (parse + dispatch + render) is the missing 100-200 µs. The verb is essentially free relative to the underlying tool.
- **Unknown ops cost zero.** The dispatch rejects before exec, which is the right shape — agents trying `--op weird` shouldn't pay git startup cost.
- **Tokens-out at 62 for the dirty-repo summary.** That's competitive with `git status --short` on this same repo (which prints ~5 lines of `M …`). The structure tax is roughly nothing for status.

## Next session

- **Ship 6: `--op log`.** Stable parse format, structured `Commit` records, tracer integration. Covers ~50% of the remaining bash-git noise.
- **Document agent-facing `git --op` design** in the README so the README's positional-style example doesn't keep tripping people up. Or: revise the README to match the wire shape we actually ship.
- **Cleanup CLAUDE.md and docs/session-notes** as the surface grows. Currently the live-verbs list has six entries (read/find/grep/git/metrics/help) and the section is getting long. May warrant a refactor at ship 7-ish.
