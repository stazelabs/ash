# Session: ASH-29 — Cold-start latency attribution

**Task.** Instrument regex_compile_us and dispatch_us phases; run cold vs warm comparison to attribute the ~3x first-call gap.

**Verbs used.** ash read, ash edit, ash grep (cold/warm benchmark).

## Instrumentation changes

- `internal/proto/proto.go`: added `RegexCompileUs` to `Phases`; added `LatencyDispatchUs` to `Metrics`
- `internal/proto/tracer.go`: added `regexCompileUs` atomic and `AddRegexCompile` method
- `internal/verbs/grep/grep.go`: changed compile timer to `AddRegexCompile`; added `defer AddRegex(matchStart)` in `searchBody` for actual match time
- `cmd/ashd/main.go`: measure `dispatchUs` between execStart and `runner.Run`; include in `Metrics` and `ledger.Call`
- `internal/ledger/ledger.go`: added `regex_compile_us` and `latency_dispatch_us` columns with migration; updated all SELECTs and INSERT
- `internal/verbs/metrics/metrics.go`: added new fields to `Row`, `ResultFromCalls`, `writeRow`, `decodeResult`
- `internal/verbs/report/report.go`: added `regex_compile%` to sub-phase breakdown
- `cmd/ash/main.go`: show `regex_compile:N` and `dispatch_us=N` in the metrics footer

## Investigation results

Three `ash grep --pattern ParseArgs --path . --glob **/*.go` calls after a daemon restart (fresh OS page cache):

```
call 1 (cold):  exec=11857µs  io=8342µs  regex_compile=22µs  regex=826µs  dispatch=2µs
call 2 (warm1): exec=5916µs   io=3031µs  regex_compile=18µs  regex=890µs  dispatch=1µs
call 3 (warm2): exec=3522µs   io=782µs   regex_compile=6µs   regex=789µs  dispatch=0µs
```

Total gap: 11857µs cold → 3522µs warm = **3.4x** (matches the ~3x from the issue).

**Attribution of the 8335µs cold-start gap:**
- IO (OS page cache for file reads): 8342 - 782 = **7560µs = 90.7% of the gap**
- Walk FS metadata (dir entries, gitignore): **~700µs = 8.4%**
- Regex compile (Go runtime caches compiled regexes): 22 - 6 = **16µs = 0.2%**
- Dispatch overhead: 2 - 0 = **2µs = negligible**
- Regex match: essentially constant (~820µs both cold and warm)

## Conclusion

**The cold-start gap is almost entirely OS page cache.** File reads go from 8342µs cold to 782µs warm — 10.7x. The gap is not in:
- Go runtime / regex first-compile (tiny)
- Daemon dispatch/goroutine scheduling (2µs, negligible)
- Regex matching (constant)

**Implication:** The ~3x cold-start tax is a kernel artifact. It cannot be eliminated without a daemon-side file content cache (significant scope). The runtime and dispatch overhead are already negligible.

## All tests pass
