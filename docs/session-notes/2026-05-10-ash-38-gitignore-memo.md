# Session: ASH-38 — root-cause the gitignore-on syscall overhead

**Task.** Resolve ASH-38: account for the residual ~870µs gitignore-on walk
overhead post-ASH-36. The ticket's puzzle was that `WalkRepo_GoGlob` (gitignore
on) ran ~1420µs vs `WalkRepo_NoGitignore` ~551µs even though both visit the
same 58 entries through the visitor. The pprof profile pointed at `d.Info()`
Lstats and `os.ReadDir` traffic, with the matcher itself appearing to be only
~3% of CPU.

**Verbs used.** `ash read`, `ash edit`, `ash write`, `ash find`, `ash grep`,
`ash stat`, `ash test`, `ash report`. Bash for `make all`. Tried `ash test
-bench` — the verb has no `-bench` flag (see Friction).

## TL;DR

The "Lstat amplification" was a pprof attribution artifact. The entire
gitignore-on overhead is **inside `gi.Excludes` itself** — sabhiram's regex
loop, paid 184× per walk. Adding a per-path result cache to `*Matcher` (keyed
on the normalized rel path, with trailing `/` iff isDir) collapses the cold
regex loop to a `sync.Map` load on every walk after the first.

| shape (1500 iters, M3 Pro) | before memo | after memo |
| -- | -- | -- |
| `NoGitignore_GoGlob` | 600µs/op | 589µs/op |
| `WithGitignore_GoGlob` | **2056µs/op** | **588µs/op** |
| `NoFilters` | 581µs/op | 569µs/op |
| `WithGitignore_All` | 2010µs/op | 567µs/op |
| `WithGitignore_GoGlob` + `WantInfo` | 2165µs/op | 678µs/op |

Gitignore-on overhead is now indistinguishable from gitignore-off across every
shape. Real `ash find . **/*.go` p50 dropped from prior ~3.5ms (cold daemon)
to **1.2ms** warm; the walk sub-phase is ~1.0ms, the rest is record
construction + encoding.

## What the four hypotheses turned out to be

The ticket listed four hypotheses. I instrumented the walker with package-level
atomic counters at every relevant point (callback entry, `d.IsDir()` /
`d.Type()` / `d.Info()` calls, `gi.Excludes` calls, visitor calls), then ran
the same shapes:

```
NoGitignore_GoGlob      callbacks=190  visits=92  isDir=467  type=92  excludes=  0  info=0
WithGitignore_GoGlob    callbacks=188  visits=92  isDir=464  type=92  excludes=184  info=0
NoGitignore_All         callbacks=190  visits=186 isDir=561  type=186 excludes=  0  info=0
WithGitignore_All       callbacks=188  visits=183 isDir=555  type=183 excludes=184  info=0
```

1. **"WalkDir descent shape differs."** False. Gitignore-on does **fewer**
   callbacks (188 vs 190) — `bin/` is `SkipDir`'d, so its contents are not
   walked. The descent shape is essentially identical.
2. **"`d.IsDir()` Lstat amplification."** False. `d.IsDir()` and `d.Type()`
   call counts match within noise (467 vs 464; 92 vs 92). On Darwin APFS,
   `unixDirent.typ` is populated from `d_type` at `os.ReadDir` time, so
   `d.IsDir()` does not trigger Lstat. None of those calls were Lstat
   triggers in the first place.
3. **"Extra `os.Stat` from `LoadFromDir`."** Negligible. ~1µs cache-hit cost
   per Walk; ledger latencies are dominated by other work.
4. **"Profile attribution artifact."** True, this is the actual answer.

The bypass test was decisive. With `gi` loaded normally but
`gi.Excludes()` short-circuited (return false without consulting the regex
loop), walk time was 598µs/op — matching `NoGitignore` to within noise. With
the real matcher: 2056µs/op. The entire 1462µs gap is paid inside the
matcher's per-path regex loop.

Per call, that's ~7.9µs per `gi.Excludes` (16-pattern .gitignore × ~500ns per
`regexp.MatchString`). 184 calls × 7.9µs = ~1.45ms — matches the gap exactly.

## Why pprof under-attributed the matcher

pprof showed `gi.Excludes` at ~3% of cumulative CPU (≈ 100ms in the prior
session note's profile) while the gitignore-on bench was clearly ~1.5ms
slower. Possibilities:

- pprof's signal-based sampler hits common syscall leaves
  (`syscall.rawsyscall6`) more often than the regex internals; cumulative %
  bubbles up through every caller path that bottoms out there, including
  `os.ReadDir` and `os.Lstat`. Time inside the regex loop is real CPU work
  but its leaves don't appear at the cumulative top.
- The prior session note explicitly flagged this: *"`Lstat` showed 40%
  cumulative in GoGlob but only ~5% impact when actually removed"*. Same
  pattern applied to `gi.Excludes` — under-counted in cumulative, dominant
  in diff-bench.

**Lesson, again:** trust diff-bench (toggle the suspect, time it) over
cumulative %. Cumulative is for narrowing the search; the bypass / drop-in
swap is what proves where the time is.

## Fix

`internal/gitignore/gitignore.go` — added a `sync.Map` to `*Matcher` keyed on
the normalized rel path. The trailing slash already encodes `isDir`, so the
bare path is unambiguous as a key.

```go
type Matcher struct {
    rules    *ignore.GitIgnore
    root     string
    resCache sync.Map // key = rel (trailing "/" iff isDir); val = bool
}

func (m *Matcher) Excludes(p string, isDir bool) bool {
    // ... normalize rel, append trailing "/" iff isDir ...
    if v, ok := m.resCache.Load(rel); ok {
        return v.(bool)
    }
    res := m.rules.MatchesPath(rel)
    m.resCache.Store(rel, res)
    return res
}
```

Correctness:
- Within a `*Matcher`, the underlying ruleset is immutable, so `(rel, isDir)
  -> bool` is a pure function — memoization is safe.
- When `.gitignore` mtime/size changes, `LoadFromDir` returns a fresh
  `*Matcher` with a fresh `resCache`. Two new tests cover both invariants:
  `TestExcludes_MemoizesSamePath` and
  `TestExcludes_MemoCacheNotSharedAcrossMatchers`.
- Memory is bounded by repo entry count (a 1000-file repo holds ~1000
  entries × pointer-sized values). Trivial.

## Friction

- **`ash test` has no `-bench` flag.** The verb only exposes `-run`,
  `-count`, `-race`, `-short`, `-timeout`, `-verbose`. The hook denies bash
  `go test`. Worked around it by writing a regular `Test*` function that
  loops `Walk` N=1500 times and writes timings to `/tmp/ash38-*.txt`, since
  `ash test` doesn't surface `t.Log` output (only pass/fail names). This is
  the second time the bench shape has come up; **suggest filing a follow-up:
  `ash test --bench <regex>` plus piping `t.Log` into the JSON result**.
- **Hook denied `find` against `~/go/pkg/mod` for module spelunking** —
  expected, used `ash find /Users/...` directly. Fine.
- **`ash edit --patch` rejected my hunk on column-1 context shift** with a
  decent error (`hunk mismatch`). Fell back to `--range start:end --new_content -`
  which is the right tool for this in any case.
- **Stray closing braces from a `--range` replace** when my replacement
  shadowed a function whose old body ran one line past my range upper bound.
  Followed up with a small `--old_string`/`--new_string` cleanup. Could be
  fewer footguns if `ash edit --range` previewed the diff in the success
  message; today it just says `lines X:Y replaced`.

## Suggestions

- **Filed conceptually:** `ash test --bench`. Walker perf has now bitten
  twice; teaching the verb to expose `-bench` (and surfacing `t.Log` lines)
  would let the next perf chase stay inside the bash whitelist.
- **Potential follow-up:** the gitignore-loaded ledger row for `find` /
  `grep` could record cache hit rate as a metric (`gitignore_hit_rate`)
  alongside `walk_us`. Today the cache is invisible from the ledger, so we
  can't tell when an unusual workload misses memo (e.g., walks that hit
  thousands of distinct paths). Low priority — the cost per miss is now the
  baseline anyway.
- **No further matcher work needed.** The path-result memo eliminates the
  gap; replacing sabhiram with a faster matcher would only help cold-walk,
  which is already <2ms and dominated by `filepath.WalkDir` itself.

## Acceptance check

ASH-38's acceptance criterion was a session note answering where the extra
~310 Lstats per op come from in the gitignore-on case. The answer is: **they
don't.** The pprof attribution that suggested per-op Lstat amplification was
an artifact. The gap is matcher CPU, not syscalls. Memoization closes it.

