# Performance baselines

Measured numbers and decisions from the profile-driven optimization work.
Reference for future tuning so we don't re-profile what's already
characterized.

All numbers on darwin/arm64, M3 Pro, Go 1.26.x. Where they're load-bearing
to a sizing decision, the bench source path is named so the run can be
reproduced.

## `ash git` — subprocess startup floor

Direct timing of raw git matches the ledger almost exactly:

| op | wall |
|---|---:|
| `git --version` (no work) | 10-30 ms |
| `git status --porcelain=v2 --branch` | 30-40 ms |
| `git log --max-count=20` | ~30 ms |

`git` is the only ash verb that fork+exec's a subprocess; every other
live verb runs in-process inside the daemon. The 30 ms floor is the
macOS fork+exec + dyld + `git init` tax — not wrapper overhead, not
tunable inside `internal/runner`. ASH-35 explored a pure-Go `go-git`
swap as a way out; deferred because the current cost is tolerable, but
worth revisiting if `test`/`build` ship and inherit the same problem.

## `internal/walker` — syscall-bound, not CPU-bound

Bench: [internal/walker/walker_bench_test.go](../internal/walker/walker_bench_test.go).

| case | ns/op | allocs |
|---|---:|---:|
| `NoFilters` (no glob, no gitignore) | 564k | 980 |
| `NoGitignore` (`**/*.go`, gitignore off) | 529k | 800 |
| `GoGlob` (`**/*.go` + gitignore) | **1500k** | **4012** |
| `NoGlob` (no glob + gitignore) | 1568k | 4183 |

CPU profile of `GoGlob`: **91.87% in `syscall.rawsyscalln`**, 2.71% in the
matcher (doublestar + gitignore regex). Pure-CPU optimizations are **not
the lever** — replacing `filepath.Rel`, fast-path `*.ext` globs, or
skipping `filepath.Base` allocs would each move <3%.

**The real lever for gitignore-on (the 3× cost):** matching itself is
only 100 ms / 2.7% of CPU. The ~970 µs/op delta between gitignore-off
and -on is **syscall overhead from `LoadFromDir` recompiling the rule
set every `Walk`** (`os.ReadFile` + per-pattern `regexp.Compile`). The
daemon is long-lived; this is wasted work. ASH-36 (the per-path memo
cache) targets this; ASH-38 shipped the cache and eliminated the
regex-loop overhead.

**`d.Info()` is a real but smaller cost:**

| case | with `Info()` | without | Δ |
|---|---:|---:|---:|
| `NoFilters` | 564k | 459k | **-19%** |
| `NoGitignore` | 529k | 502k | -5% |
| `GoGlob` | 1500k | 1446k | -4% |

`find` legitimately needs `Info` for metadata; `grep` doesn't. ASH-37
tracks the opt-in `walker.Options.WantInfo` switch.

**pprof pitfall:** cumulative columns mislead. `Lstat` showed 40%
cumulative in `GoGlob` but only ~5% real impact when stubbed out —
cumulative overcounts because the same `rawsyscalln` time gets credited
to multiple call paths. **Trust `flat` + diff-bench experiments over
cumulative %.**

## `ash hook` — client-side hot path

`hook` fires on every harness `PreToolUse`, so its fixed overhead matters
disproportionately. Daemon-side p50 is 14 µs; client-side was doing
several layers of work the ledger never measured.

Bench: [cmd/ash/hook_bench_test.go](../cmd/ash/hook_bench_test.go).

**End-to-end wall time** (`bin/ash hook < payload`, M3 Pro):

| state | pre-fix | post-fix |
|---|---:|---:|
| daemon up | ~12-13 ms | 9-12 ms |
| daemon down | ~12-13 ms | **7-8 ms** |

Go startup dominates when the daemon is up; the pre-fix daemon-down path
was eating a full 5 ms `net.DialTimeout` on every call.

**Phase breakdown:**

| phase | savings | how |
|---|---|---|
| 2 — thread `cwd`/`root` once | invisible to bench | one fewer `os.Getwd` + one fewer `session.Root` walk per call (lives inside `fireHookLedger`) |
| 3 — lazy config load on deny only | **-17%** on `allow_bash` | `config.Load`'s 2× `os.Stat` + `Defaults()` alloc skipped on the (common) allow path. 9 fewer allocs, 1.3 µs saved. Deny paths unchanged (config still loads for `exclude_verbs`) |
| 4 — stat-before-dial | -5 ms when daemon down | swap blind `net.DialTimeout` for an `os.Stat` of the socket path first |

`hook.Decide` itself was untouched — same rules, same MatchedRule strings,
same wire output. Pure work-skipping wins.

**Design limit worth knowing:** the bash segmenter doesn't introspect
`bash -c '…'`, `eval`, or `xargs sh -c` — it sees `prog=bash`,
`args[0]=-c`, and stops. This is a *valid escape hatch* (the hook is
agent-steering, not security); documented in
[docs/PreToolUse.md](PreToolUse.md).

## `internal/diff` — LCS cap sizing

Bench profile at four sizes, both worst-case (every line differs) and
realistic (~1% differ, contiguous):

| n | wall | alloc |
|---:|---:|---:|
| 2000  | ~9 ms   | 7 MiB |
| 4000  | ~37-39 ms | 30 MiB |
| 8000  | ~155 ms | 122 MiB |
| 16000 | ~605-657 ms | **489 MiB** |

Worst-case and realistic are essentially identical — the DP table
dominates regardless of how much ends up on the LCS path. Memory scales
cleanly O(n²) (uint16 table, ~8 B/cell with overhead).

**Shipped cap: 4000 lines.** Fits the "few hundred ms / 64 MiB" envelope
with margin; covers ~2× more real-world files than the original 2000.
8000+ crosses the memory envelope; 16000 is out of scope for any envelope
we'd accept.

**Skipped:** swap to Myers / `sergi/go-diff`. The cap bump alone covers
the realistic distribution; adding a dep for the long tail isn't worth
it at this stage. `Lines()` was refactored into a one-line public
cap-check + an internal `linesLCS` helper so a Hirschberg-style
`--stat` fast path (O(min(m,n)) space) has a clean seam if it becomes
worth shipping later.

## Methodology notes

- **Microbenches for hot-path verbs.** Token-comparison bench (the
  `internal/verbs/bench/` corpus) won't catch a 50 µs regression on a
  hot function. A latency-only mode for hot-path verbs (`hook`,
  `walker`) would pin allocs/ns-per-op. Sketched, not built.
- **`ash test --bench` would close the loop.** Right now bench runs need
  `bash -c 'go test -bench=…'` to bypass the PreToolUse hook (which
  routes `go test` to `ash test`, which has no `--bench` flag). Filing
  candidate: extend `ash test` with `--bench:string` and `--benchmem:bool`.
- **`ash test` swallows `t.Logf` output even on PASS** with `--verbose
  true`. To capture profile numbers from a test, write to a tmpfile
  inside the test and `ash read` it back. A `--show-logs` toggle would
  remove the dance.
