# Session note: ASH-18 — bench case to exercise grep truncation

**Task.** The first `ash bench` run reported `grep` at only **-12% tokens** vs bash on the per-verb summary, well short of the hypothesis "ash should win big on grep when there are many matches because ash truncates with a narrow-this-query hint and bash dumps everything." The four canonical grep cases (TODO, ParseArgs, files-only, rare pattern) all returned under the default `max_matches=256` cap, so the load-bearing argument was untested. Add a case that fires it.

**Verbs used.** `ash find`, `ash grep`, `ash read`, `ash edit`, `ash stat`, `ash bench`.

## What shipped

One new bench case appended to the canonical list:

```go
{
    Name:    "grep_heavy_func_internal",
    Verb:    "grep",
    AshArgs: map[string]any{"pattern": "func", "path": "internal"},
    Why:     "many-match query; ash truncates at default max_matches=256 with a narrow-this hint while bash dumps every line — the load-bearing token-savings case",
},
```

`func` under `internal/` returns ~586 matches in this repo — well over the 256 cap — so ash truncates and emits a narrow-this hint while bash returns the full dump.

`docs/bench.md` got a new dated section (`Heavy-tree case (2026-05-08)`) recording the result alongside the earlier `find`-pretty optimization.

## Why `internal/` not `.`

The bash subprocess in `bench.RunBash` does not pass any path filters — it runs literally `grep -rn func <path>`. Scoping at the repo root would pull in `.git/`, `bin/`, and `.ash/`, all of which are gitignored on the ash side and *not* gitignored on the bash side, and all of which carry machine-state-dependent contents (`.ash/ledger.db`, daemon log, build artifacts, git pack churn). `internal/` is fully checked-in and stable across machines, so the case is reproducible.

This is the same reproducibility constraint the ticket called out ("case results should not depend on machine state outside the repo").

## Numbers

Before (4 grep cases):

```
grep         4         3746         4475    -16%
overall (13)         11611        25237    -40%  (per ticket; first bench)
```

After (5 grep cases):

```
grep_heavy_func_internal   grep   4982   14518    -66%
grep         5         8728        19001    -54%
overall (14)         15593        31454    -50%
```

Per-verb grep token delta moved from -16% to -54%. The new case alone returns -66%, clearing the >50% acceptance bar.

## Friction

- None on the bench side. The case-list extension surface is well-shaped: add a literal struct entry, rebuild, run. `internal/bench/translate.go` already handled the `grep` shape — no translator change needed.
- The harness `Edit` tool refused to edit `cases.go` because I had read the file with `ash read`, not the harness `Read`. Used `ash edit` instead, which is the documented in-repo path anyway. No friction with `ash edit` — `--old_string` matched on first try.
- The harness `Write` tool was redirected by the hook to `ash write` (this note is being written via `ash write --content -` for that reason). Working as designed.

## Suggestions

1. The "heavy" tree on this repo is `internal/` (~586 `func` matches). When ash starts being benched against larger codebases — `vendor/`, `node_modules/`, multi-million-line monorepos — that's where the truncation gap should widen further. Worth adding an `extra-cases` shape (mentioned as optional in the ticket) only when there's a real heavy tree to point at; on the current repo it would be premature.
2. The bench bash translator does not pass `--max_matches` or any other ash-only flag. That is correct (the comparison is against what bash *actually* would have written), but it does mean a future ash-side change to the default `max_matches` will silently swing this case's numbers. The case `Why:` field documents the reliance on the default to make that intentional.

## Instrumentation

Bench output for the new case:

```
case                       verb      ash_tok bash_tok   Δtok%     ash_us    bash_us   Δlat%
grep_heavy_func_internal   grep         4982    14518    -66%       2538       6100    -53%
```

The new case also dominates the per-verb grep latency story — bash `grep -rn func internal` is 2-3× slower than ash even on a small tree, because ash skips binary files and uses RE2 with a per-file cap.
