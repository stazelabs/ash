# Session: walker perf profile — git outlier, gitignore 3× cost

**Task.** Investigate two ledger observations: (1) `ash git` is a 30–65ms latency outlier vs the µs-range of in-process verbs, (2) `find` spends ~98% of exec in walk. Profile the walker, identify levers, file follow-ups.

**Verbs used.** `ash report`, `ash metrics`, `ash find`, `ash grep`, `ash read`, `ash write`, `ash edit`. Bash for `go test -bench`, `/usr/bin/time`, `go tool pprof`.

## Findings

### 1. `ash git` is a subprocess-cost problem, not a wrapper problem

Direct timing of raw git on this machine matched the ledger almost exactly:

- `git --version` (no actual work) ≈ 10–30ms — pure fork+exec + dyld + git init.
- `git status --porcelain=v2 --branch` ≈ 30–40ms.
- `git log --max-count=20` ≈ 30ms.

Ledger numbers are identical, so wrapper overhead in `internal/runner` is in the noise. The structural reason: **git is the only ash verb that fork+exec's a subprocess** — every other live verb runs in-process inside the long-lived daemon. The 30ms floor is the macOS subprocess startup tax, not anything we can shave by tuning.

**Filed:** ASH-35 — explore replacing the shell-out with pure-Go `go-git` (deferred, Low; current cost is tolerable but worth revisiting if `test`/`build` ship and inherit the same problem).

### 2. Walker is syscall-bound; gitignore-on is 3× wall time

Bench harness landed at `internal/walker/walker_bench_test.go`. Numbers on this repo (M3 Pro):

| bench | ns/op | allocs |
| -- | -- | -- |
| `NoFilters` (no glob, no gitignore) | 564k | 980 |
| `NoGitignore` (`**/*.go`, gitignore off) | 529k | 800 |
| `GoGlob` (`**/*.go` + gitignore) | **1500k** | **4012** |
| `NoGlob` (no glob + gitignore) | 1568k | 4183 |

CPU profile of `GoGlob` (the realistic case):

```
  91.87%  syscall.rawsyscalln
   2.71%  doublestar / gitignore matcher (regex)
```

Subtree attribution (cumulative):

```
  os.ReadDir   1.90s   (51%)   ← os.openDir(1.58s) + getdents loop(0.32s)
  os.Lstat     1.48s   (40%)   ← driven by d.Info() / d.Type()
  gitignore matcher  0.10s    (3%)
```

**Surprise #1: pure-CPU optimizations are not the lever.** Pre-session hypotheses (replace `filepath.Rel` with parent-stack tracking, fast-path `*.ext` globs, skip `filepath.Base` alloc) are all <3% of CPU. None would move wall time.

**Surprise #2: gitignore matching itself is only 100ms / 2.7% of CPU**, but gitignore-on is 970µs slower per op than gitignore-off. The delta is in syscalls, not the matcher — most likely `gitignore.LoadFromDir` recompiling the rule set every `Walk` (`os.ReadFile` + per-pattern `regexp.Compile`). The daemon is long-lived; this is wasted work.

**Surprise #3: `d.Info()` is a real but smaller cost than the cumulative profile implied.** Stubbing it out and re-benching:

| bench | with Info() | without | Δ |
| -- | -- | -- | -- |
| `NoFilters` | 564k | 459k | **−19%** |
| `NoGitignore` | 529k | 502k | −5% |
| `GoGlob` | 1500k | 1446k | −4% |

`find` legitimately needs Info; `grep` doesn't. Real win for `grep` walks specifically.

**Filed:**
- ASH-36 (High) — cache compiled gitignore matcher across Walk calls. Target: GoGlob ≤ 600µs.
- ASH-37 (Medium) — make `d.Info()` opt-in via `walker.Options.WantInfo`.

## Friction

- Hook denied `cat`/`head`/`tail` patterns I reflexively used to read files outside the project root (`.claude/projects/.../memory/`). `ash write --path - <<'EOF'` works for arbitrary absolute paths, no project-root sandbox today; flagged here in case that affordance changes later.
- `go tool pprof -top` cumulative columns can mislead: `Lstat` showed 40% cumulative in GoGlob but only ~5% impact when actually removed. Cumulative attribution overcounts because the same `rawsyscalln` time gets credited to multiple call paths. Trust `flat` + diff-bench experiments over cumulative %.

## Suggestions

Land ASH-36 first; re-profile; if walks still feel heavy after that, ASH-37 is the obvious next step. Concurrency / raw `getdents` are deferred — premature given the absolute numbers.
