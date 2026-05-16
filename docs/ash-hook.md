# `ash hook` — trim fixed overhead on the highest-frequency verb

## Context

`ash hook` is invoked on nearly every harness tool call (PreToolUse). The daemon-side ledger shows it's already cheap on the wire (p50 14µs, p95 32µs over the last 24h × 141 calls), but those numbers measure *only the daemon's view of the in-process decision* — they do not include the **client-side fixed overhead** that runs before stdout is flushed (which is what the harness actually blocks on).

Reading [cmd/ash/hook.go](../cmd/ash/hook.go) end-to-end reveals avoidable work on the critical path that the ledger can't see:

1. **Redundant filesystem work.** `runHook` calls `os.Getwd()` + `session.Root()` to load config; then `fireHookLedger` calls *both again*. Each `session.Root()` walk does up to N `os.Stat()`s climbing toward `.git`/`go.mod` (see [internal/session/paths.go:13-32](../internal/session/paths.go#L13-L32)). On agents that cd into subdirs, this is the dominant filesystem cost.

2. **Config load on the allow-path.** Every invocation calls `config.Load()` (see [internal/config/load.go:38-66](../internal/config/load.go#L38-L66)) — two `os.Stat` calls plus, if either file exists, a full TOML decode of the entire config schema — solely to extract `[hook].exclude_verbs`. `exclude_verbs` only matters when the decision would otherwise be **deny**; the (common) allow path never reads it.

3. **5ms wasted timeout when the daemon is down.** `fireHookLedger` always calls `net.DialTimeout(unix, sock, 5ms)`. If the daemon is stopped (post-`ash stop`, fresh clone, between sessions), every hook eats the full 5ms before falling through. A `Stat` of the socket file is ~1µs and can short-circuit the dial.

4. **No micro-benchmark.** There are zero `Benchmark*` functions in the repo (grep across `*_test.go` is empty). The `bench/` system is the higher-level cases harness — useful for end-to-end, but not for catching a regression that adds 50µs to the hook hot path. Without a microbench we can't measure these changes, and future drift will go unnoticed.

The point of this work is not to chase microseconds for their own sake; it's that the hook runs on the agent's critical path and the fixed costs that *don't depend on what the hook decided* are pure tax. Strip them so the verb's overhead is bounded by Go process startup + decision logic, not by incidental filesystem walks.

## Plan

### Phase 1 — Microbench (lock in the baseline before touching anything)

Add `BenchmarkRunHook` in a new `cmd/ash/hook_bench_test.go` that:

- Constructs a representative `PreToolUse` payload in-memory (one for the deny-Grep case, one for the allow-Bash-`ls` case, one for the heredoc-Bash deny case).
- Calls a refactored `runHookFromReader(r io.Reader, w io.Writer)` so the benchmark doesn't need real stdin/stdout. Minimal refactor: extract the body of `runHook()` to take `io.Reader`/`io.Writer`, leave `runHook()` as a thin wrapper passing `os.Stdin`/`os.Stdout`.
- Reports `ns/op` and `allocs/op`.
- Optionally also benchmark `hook.Decide` on its own to separate decision cost from I/O + config cost.

This must land first. Every subsequent change reports a before/after `benchstat`.

### Phase 2 — Thread cwd/root through, eliminate redundant filesystem work

In [cmd/ash/hook.go](../cmd/ash/hook.go):

- Compute `cwd, _ := os.Getwd()` and `root, _ := session.Root(cwd)` once at the top of `runHook` (after stdin read, before config decision).
- Change `fireHookLedger(wireArgs map[string]any)` → `fireHookLedger(root string, wireArgs map[string]any)`. Caller passes the already-resolved root (empty string means "skip, we couldn't resolve").
- Drop the `os.Getwd()` + `session.Root()` calls inside `fireHookLedger`.

Net effect: at most one `os.Getwd` + one root walk per invocation, regardless of allow/deny.

### Phase 3 — Lazy config load (skip the allow-path tax)

The current order is: load config → decide. Invert it to: decide → if deny, then load config → re-check exclusion.

Concretely in `runHook()`:

1. Call `hook.Decide(args)` immediately with `args.ExcludeVerbs` left empty.
2. If `result` is allow → emit nothing → done. Skip `config.Load` entirely.
3. If `result` is deny → call `config.Load(root)`; if `len(cfg.Hook.ExcludeVerbs) > 0` and `result.SuggestedVerb` is in the list, downgrade to allow; otherwise emit the deny JSON.

This requires that `hook.Decide`'s `Result` (in [internal/verbs/hook/hook.go](../internal/verbs/hook/hook.go)) exposes the matched verb name so the exclusion check can run after the fact. Inspect the existing `allowedByExclusion` call site at hook.go:203-247 and pull the verb-name resolution out of `Decide` (or have `Decide` always return the candidate verb and let the caller apply exclusion). The decision rules themselves do not change.

Tradeoff: deny path now pays config load instead of every path. Deny is rarer than allow in a typical session (most harness tools the hook doesn't intercept fall through to allow), and even for denies, the config load is the same cost — we're not adding work, just moving it.

Optional in this phase (low-cost): also drop the global-config check in this codepath. `[hook].exclude_verbs` is a per-project knob — there's little reason to honor a global `~/.config/ash/config.toml` for it. Either inline a project-only loader, or skip if global-only is the only file present. **Recommendation:** keep it for now, accept the one extra `os.Stat`; revisit if `config.Load` shows up in the bench.

### Phase 4 — Stat-before-dial in `fireHookLedger`

Before `net.DialTimeout`, call `os.Stat(sock)`. If the file doesn't exist, return immediately — no 5ms timeout. If it exists, dial as today (the daemon could still be hung or starting; the 5ms timeout is the safety net).

This is a one-syscall pre-check that turns the daemon-down case from "5ms wasted" to "~1µs wasted." Daemon-up case is unchanged (UDS dial is sub-ms on success).

### Phase 5 — Verify

- `go test ./internal/verbs/hook/...` — existing decision unit tests must all pass.
- `go test ./cmd/ash/...` — new benchmarks compile.
- `go test -bench=BenchmarkRunHook -benchmem ./cmd/ash/...` — capture numbers before vs after (run on baseline first, then after each phase).
- Manual: `echo '{"tool_name":"Bash","tool_input":{"command":"grep foo ."}}' | bin/ash hook` should still return the deny JSON unchanged. Same for an allow case (`{"tool_name":"Task","tool_input":{}}` or similar).
- With daemon up: `ash report --verb hook --since 1h` should still record rows after a few synthetic hook calls.
- With daemon down (after `ash stop`): `time bin/ash hook < payload.json` should drop measurably (5ms → sub-ms).
- Promote before/after bench numbers and a one-liner summary of which phases moved the needle into [docs/performance-baselines.md](performance-baselines.md).

## Files touched

- [cmd/ash/hook.go](../cmd/ash/hook.go) — refactor `runHook` + `fireHookLedger`, thread `cwd`/`root`, invert config-load ordering, add `os.Stat` pre-dial check.
- [cmd/ash/hook_bench_test.go](../cmd/ash/hook_bench_test.go) — **new file.** `BenchmarkRunHook` and helper to construct in-memory payloads.
- [internal/verbs/hook/hook.go](../internal/verbs/hook/hook.go) — possibly small surface change to `Result` / `Decide` to expose the suggested verb name independently of exclusion evaluation. Touch only the public-API seam needed for phase 3.

## Explicitly out of scope

- Replacing `encoding/json` with a hand-rolled parser. Payloads are <1KB; the win wouldn't justify the maintenance.
- Sidecar JSON cache (`.ash/hook-config.json`) for resolved config. Worth considering only if `config.Load` still dominates after phase 3 — defer.
- Detaching `fireHookLedger` to a forked subprocess. Fork+exec is more expensive than the worst case it avoids.
- Caching `session.Root()` across invocations via env var or sidecar. Each `ash hook` is a fresh process; cross-invocation cache requires a substrate that doesn't exist today.
- Hot-reloading config in the daemon (unrelated to the hook's client-only path).
