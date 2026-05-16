# `ash replay` — ledger-as-regression-suite

`ash replay` re-runs prior ledger calls against the current build and
reports per-verb token deltas vs the originals. Companion verb to
[`ash bench`](bench.md): bench compares ash to bash on synthetic cases;
replay compares the current build to recorded real sessions.

With `--cache_prefix=true` (ASH-135) it also surfaces an empirical A/B
of the ASH-108 envelope reorder: per-verb average matching-byte-prefix
between consecutive same-verb responses, encoded once with today's
cache-aware envelope and once with a struct mirroring the pre-ASH-108
ordering. The delta is how many bytes the reorder bought a hypothetical
Anthropic prompt cache. See [docs/cache-shape.md](cache-shape.md) for the
contract this measures.

The empirical scoreboard for token-saving claims — ASH-108 (cache
envelope), ASH-114 (read-header compaction), ASH-121 (truncation-hint
compaction) — replay is what tells you a claimed saving actually
held against real prior sessions, not just synthetic fixtures.

## Surface

```
ash replay [--session current|all|<id>]
           [--since <dur>]
           [--verb <verb>]
           [--limit N]
           [--regress_tokens %]
           [--top N]
           [--cache_prefix true|false]
```

Same flag idiom as `ash report`. `--regress_tokens` uses bench's
semantics: a row counts as a regression when `|Δtok%| > regress_tokens`.

## Skip rules

Replay deliberately won't run:

- **Mutating verbs** (`write`/`edit`/`init`/`uninit`/`stop`) — replaying
  these would mutate the world.
- **Heavy verbs** (`test`/`bench`) — too slow for a regression sweep.
- **Recursive** (`replay`) — infinite loop.
- **`args_truncated`** — the `<truncated:N>` sentinel ashd writes for
  long string args. Honest replay isn't possible when the args were
  redacted.
- **`no_args`/`decode_failed`** — for verbs that need args, these rows
  can't be replayed.

The skip counts surface in the pretty output (`skipped: args_truncated=4,
mutating=6, recursive=1`).

## Output shape

```
§replay: all — 39 replayed, 11 skipped
totals: orig_tok=41054 new_tok=41319 Δtok=+265 (+0.6%)  ok_mismatches=0  regressions=1
skipped: args_truncated=4, mutating=6, recursive=1

verb         n  orig_tok  new_tok    Δtok   Δtok%  regr
read        11    35743    36008    +265   +0.7%     1
grep         6     4900     4900      +0   +0.0%     0
...

top regressors:
  read path=internal/verbs/verbs.go — orig=2150 new=2415 Δ=+265 (+12.3%)
```

`ok_mismatches` counts replays where success/failure flipped between the
original and the replay — distinct from token regression and worth
surfacing separately.

## Architecture

Read-only meta-verb. Dispatches in-process via a `Deps` closure over the
live runner + pretty registries, same pattern as `ash bench`. No subprocess
overhead; replay shares the daemon's verb implementations directly.

## Scope of v1 / deferred work

**In scope:** the skip rules, the per-verb table, the regression flag,
the top-regressors histogram. Same shape as `ash bench`'s output so the
two verbs feel familial.

**Not in scope (file separate tickets when needed):**

- **File-state snapshots / git-ref pinning.** Replay runs against the
  current working tree. If files changed since the recorded call, the
  delta is real but uninterpretable as a token-savings result. A future
  pass could snapshot working-tree state per session and replay
  deterministically.
- **CI integration.** The wire result has the structure CI needs
  (`Overall.Regressions`, `OKMismatch`), but no Make target or GitHub
  action wires it up yet. Plausible shape: `make replay-check` runs the
  full ledger replay and fails on any regression above a threshold.
- **Persistence.** Bench writes runs to `bench_runs` /
  `bench_case_results`; replay returns its result ephemerally. Mirror
  the bench pattern if trend lines on replay results turn out to matter.
- **Replaying mutating verbs.** `write`/`edit` args are 1024-byte-capped
  in the ledger sanitizer, so honest replay isn't possible for
  content-heavy calls without a separate args-storage scheme. No
  `--include_mutating` flag.

## Operational notes

- **`UPDATE_GOLDEN=1 ash test` doesn't propagate the env to `go test`.**
  `ash test` doesn't set `cmd.Env`, so the subprocess inherits the
  daemon's env, not the client's. Small follow-up: plumb env through, or
  accept `--env`.
- The first regression the verb caught against itself in its own
  ship session was the registry edit that *added the new verb* —
  `internal/verbs/verbs.go` grew +265 tokens. The regression detector
  works on day zero.
