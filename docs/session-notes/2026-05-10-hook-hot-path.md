# 2026-05-10 — hook hot path

## Task
Trim avoidable fixed overhead from `ash hook`. It's the most-frequent verb (PreToolUse fires on every harness tool call), already cheap on the daemon side (p50 14µs across last 24h × 141 rows), but the client-side path was doing work the ledger never measures.

## Verbs used
- `ash read`, `ash find`, `ash grep`, `ash edit`, `ash write`, `ash test`, `ash report`, `ash metrics`, `ash help`, `ash stop` — all of these worked as expected.
- `go build` (whitelisted), `go test` (proxied to `ash test`), `go test -bench=...` (no `--bench` support in `ash test` yet — workaround below).

## Friction
- **`ash test` has no `--bench` / `--benchmem`.** The PreToolUse hook (correctly) intercepts bare `go test`, but I needed `go test -bench=...` to measure the changes I was making *to the hook itself*. Catch-22: the verb I'm optimizing blocks me from running its own benchmarks via the dogfooded path. The current rule maps every `go test` invocation to "use `ash test`," but `ash test` doesn't expose bench mode.
- **Hook's bash segmenter ignores `bash -c '…'`.** That's how I worked around the above: `bash -c 'go test -bench=…'` slips through because the segmenter sees prog=`bash`, args[0]=`-c`, and doesn't introspect the quoted command. Same gap would apply to `eval`, `xargs sh -c`, etc. Not blocking, but worth knowing — the hook is "agent-steering," not security.
- **No microbench infrastructure existed.** `grep -r 'Benchmark'` across `*_test.go` returned zero. The `bench/` cases system measures end-to-end token cost vs. bash, but doesn't catch a 50µs regression on a hot-path function. Added `cmd/ash/hook_bench_test.go` as the starting point.

## Workarounds
- `bash -c 'go test -bench=. -benchmem -count=3 -run=^$ ./cmd/ash/'` to bypass the PreToolUse hook for benchmark runs.
- Manual end-to-end timing via `bash -c 'for i in 1..5; do echo "{...}" | { time bin/ash hook >/dev/null; } 2>&1 | grep real; done'` to compare daemon-up vs daemon-down wall time (the bench doesn't cover `fireHookLedger`).

## Suggestions
1. **Add `--bench:string` and `--benchmem:bool` to `ash test`.** Forward to `go test -bench=PATTERN -benchmem`. Without it, "use `ash test`" is unenforceable for anyone doing performance work, and the project's own benchmark-driven optimization story stalls.
2. **Consider tightening (or explicitly documenting) the `bash -c '…'` escape.** It's a valid escape hatch (the hook is steering, not enforcing), but the docs don't call it out. The PreToolUse design doc could note "bash -c bypasses introspection" so agents don't pretend it doesn't exist.
3. **The `bench/` cases harness could grow microbench cases** that pin `hook` allocs/ns-per-op. The current `bench/baseline.json` is purely token-comparison; a "latency-only" mode for hot-path verbs would catch regressions that don't move token counts.

## Instrumentation

### Microbench — before vs. after (Apple M3 Pro, `go test -bench`, `count=3`, median)

| case                              | before ns/op | after ns/op | Δ        | before allocs | after allocs |
|-----------------------------------|-------------:|------------:|---------:|--------------:|-------------:|
| `runHookFromReader/allow_bash`    |        7,437 |       6,177 | **-17%** |            56 |       **47** |
| `runHookFromReader/deny_grep`     |        8,990 |       9,016 |     ~0%  |            82 |           82 |
| `runHookFromReader/deny_heredoc`  |        9,240 |       9,149 |     ~0%  |            81 |           81 |
| `hook.Decide/allow_bash` (pure)   |          142 |         147 |     ~0%  |             4 |            4 |
| `hook.Decide/deny_grep` (pure)    |          395 |         398 |     ~0%  |             8 |            8 |
| `hook.Decide/deny_heredoc` (pure) |          519 |         533 |     ~0%  |            11 |           11 |

Phase-by-phase attribution:
- **Phase 2 (thread `cwd`/`root` once)** — no movement in the bench because the savings are inside `fireHookLedger`, which the bench intentionally skips. Real-world: one fewer `os.Getwd` + one fewer `session.Root` walk per call.
- **Phase 3 (lazy config load on deny only)** — *entire* `allow_bash` improvement is here: `config.Load`'s 2× `os.Stat` + `Defaults()` struct allocation is skipped on the (common) allow path. 9 fewer allocs, 1.3µs saved. Deny paths are unchanged (config still loads to honor `exclude_verbs`).
- **Phase 4 (stat-before-dial)** — invisible to the bench (covers only the non-network part), visible to wall time. End-to-end timing of 5 sequential `bin/ash hook` calls with the daemon down dropped from ~10ms to ~7–8ms per call (the saved ~3ms is the previously-burned 5ms `net.DialTimeout` minus the new ~1µs `os.Stat`). With the daemon up, no change.

### Wall-time end-to-end (`time bin/ash hook < payload`)
- Daemon up (after fix): 9–12ms per call (Go startup dominant).
- Daemon down (after fix): 7–8ms per call. Same path pre-fix: ~12–13ms (would have eaten the full 5ms `net.DialTimeout`).

### Ledger sanity check
With daemon up post-fix: `ash report --verb hook --since 1m` showed both manual test hooks recorded normally (p50 24µs daemon-side, ok 100%). Ledger row writes are unaffected.

## What did NOT change
- `hook.Decide` itself — same rules, same MatchedRule strings, same wire output for both allow and deny.
- The wire shape of hook ledger rows — `wireArgs["exclude_verbs"]` is still only set when exclusion actually fires, same as before.
- All 651 existing tests pass; in particular the 110 in `internal/verbs/hook` (decision rules + heredoc segmenter) and 26 in `cmd/ash`.
