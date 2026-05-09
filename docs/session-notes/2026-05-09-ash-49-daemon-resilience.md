# ASH-49: daemon resilience — read deadlines, graceful shutdown, optional concurrency cap

**Task.** Wire the `[daemon]` config section that ASH-61 landed: per-frame socket read deadlines (so an idle/abandoned client connection doesn't pin a goroutine forever), a `WaitGroup`-tracked graceful shutdown (so SIGTERM doesn't drop in-flight ledger writes), and an optional concurrency cap (so a runaway producer doesn't spawn unbounded goroutines).

**Verbs used.** `ash read`, `ash edit`, `ash write`, `ash grep`, `ash test`, `ash git --op {status,log,diff}`.

**Defaults picked.** With ASH-61 in place, the question was what `Defaults()` should set when no `ash.toml` exists.

- `max_concurrent_handlers = 0` — unlimited. The original ticket called the cap "optional"; making it on-by-default risks breaking agents that fan out parallel work, with no obvious right number to pick. Opt-in.
- `read_deadline = 30s` — generous enough that any real client pause is fine, aggressive enough that dead/half-closed connections clean up promptly. The agent's typical pattern is connect → write → read → close in milliseconds, so 30s is far past any legit case.
- `shutdown_grace = 5s` — long enough for any verb in the current surface to commit its ledger row before exit. Long-running verbs (`test`, `bench`) are best killed with SIGKILL if the user wants them gone.

These show up in `internal/config/config.go::Defaults()` and `DefaultReadDeadline` / `DefaultShutdownGrace` constants so a future tuning pass has one obvious place to land.

**Implementation.** Three pieces in [cmd/ashd/main.go](../../cmd/ashd/main.go):

1. `handle()` gained a `readDeadline time.Duration` parameter. When > 0, `conn.SetReadDeadline(time.Now().Add(readDeadline))` runs before each `proto.ReadFrame`. A `net.Error` with `Timeout() == true` returns cleanly (not logged as an error) — this is the expected close-path for abandoned connections.

2. The accept loop is now `acceptLoop(ln, sem, &wg, handler)`:
   - `wg.Add(1)` per dispatched goroutine; `defer wg.Done()` inside.
   - When `sem != nil`, acquire a slot before incrementing wg. Backpressure shows up as queued connections at the kernel UDS backlog rather than goroutine growth.
   - Returns when `ln.Accept()` errors (canonical shutdown path: signal handler closes the listener).

3. After the accept loop returns, `drainHandlers(&wg, grace)` waits up to `shutdown_grace` for in-flight handlers to commit. Logs loudly if grace expires; still exits so tests/supervisors aren't stuck.

**Tests.** [cmd/ashd/resilience_test.go](../../cmd/ashd/resilience_test.go):

- `TestHandle_ReadDeadlineExpires` — net.Pipe with a 50ms deadline, never send a frame, verify handle() returns within 2s. Without the fix this hangs forever.
- `TestDrainHandlers_CleanReturn` / `_GraceExceeded` / `_ZeroGrace` — happy path, loud-failure path, and the no-grace-configured semantic.
- `TestAcceptLoop_GracefulDrain` — real UDS listener, 3 slow handlers (80ms each), close listener, verify `drainHandlers(2s)` returns true and all completed.
- `TestAcceptLoop_SemaphoreCap` — cap=1, 4 dialers, verify max observed concurrent handlers stays at 1.

**Friction.**

- I tried to verify "the deadline resets on each successful frame" with a test that sent frame 1, slept past the deadline, and sent frame 2. That fails not because the code is wrong but because the test expectation was wrong: if the deadline IS 200ms and I sleep 300ms, the daemon timed out correctly. Dropped the test rather than rewrite around fragile timing — the existing happy-path tests (which all do legit request-response) prove the deadline doesn't fire on real traffic.
- The daemon was running with an old jail policy (loaded during the ASH-61 smoke test) and denied a `/tmp/...` Python writer write. Fixed with `pkill ashd` + retry. Worth noting that "daemon state survives across iterations of editor work" is a sharper trap now that jail is enforced; the doc tells users to restart after editing `ash.toml`, but in practice the trap also bites mid-development when tooling assumes `/tmp` access.

**Suggestions.**

- A `kill` verb (`ash kill` to stop the project daemon cleanly via the PID file) would be much nicer than `pkill ashd`. The CLAUDE.md verification block already pretends it exists. Quick win.
- Hot-reload of `ash.toml` would have prevented the friction above. Defer until pain is consistent.
- ASH-35 is now the last `[*]` schema section without enforcement. The selector wiring is small (read `cfg.Git.Backend` from a closure in `verbs.Runners`); the spike content (does `go-git` actually work for our repo states?) is the open question.

**Instrumentation (this session).**

```
verb     n    ok%   p50_exec   p95_exec
write    ~10  100%  ~330us     ~600us
edit     ~12  100%  ~500us     ~800us
test     ~8   100%  ~470ms     ~660ms (full repo, 32 pkgs)
read     ~25  100%  ~50us      ~500us
```

End state: 32 packages pass, ASH-49's three pieces enforced with sensible defaults, docs/config.md/CLAUDE.md/ash.toml.example updated to reflect that `[daemon]` is now live.
