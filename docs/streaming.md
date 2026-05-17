# Streaming protocol — design notes

User-facing behavior is summarized in [CLAUDE.md §Gotchas](../CLAUDE.md#gotchas).
The cache-shape contract is in [docs/cache-shape.md](cache-shape.md).
This doc captures *why* the streaming surface looks the way it does,
plus follow-ups that haven't shipped.

## Why streaming

`grep`, `find`, and `test` can produce large blobs with long tail latency.
Without streaming the agent sees nothing until the verb completes — for
`test` runs that's seconds to tens of seconds. Kind-tagged Chunk frames
let a progress-aware harness render partial results within tens of ms,
without changing the wire shape for non-streaming clients.

## Wire layout (post-ASH-106)

All streaming traffic rides the existing per-project UDS. The new shapes:

- `Request.Stream: true` — client opt-in. ashmcp sets it only when the
  MCP client supplied a `progressToken`; the CLI never sets it.
- **Chunk** frame — kind byte + msgpack payload of incremental results.
  Verb-specific: grep emits `Match`, find emits `Record`, test emits
  `Package` (`{path, status, elapsed}`).
- **Final** frame — kind byte + msgpack-encoded `Response`. Carries the
  cumulative result for non-progress-aware clients (or `Err.Code =
  "cancelled"` if ctx was cancelled before the verb finished, even if
  partial data was produced — the partial Result is discarded to keep
  the cancelled-vs-completed distinction sharp).
- **Cancel** frame — client → daemon, kind-byte only. Closing the conn
  has the same effect (the per-request watcher goroutine reads EOF and
  cancels ctx).

Non-streaming requests (`Stream: false` or absent) get a single
**legacy Response frame**, byte-for-byte identical to pre-ASH-106. v1
clients are unaffected.

## Batch thresholds (64 items / 50ms)

`frameEmitter` flushes when **either**:

- 64 items have accumulated, **or**
- 50 ms have elapsed since the last flush.

Both are `const` in [cmd/ashd/emitter.go](../cmd/ashd/emitter.go). They
were picked by intuition, not measurement. **No streaming-aware bench
case exists yet** — until one does, treat these as guesses. The natural
follow-up is to add a streaming case to `internal/verbs/bench/` and
measure across realistic grep/find/test workloads, then consider
exposing under `[daemon.streaming]` in `ash.toml`.

For `test`, time-to-first-chunk is gated by the **first package's
compile+run time**, not the first individual test — the streaming hook
sits on `scanEvents`' pkg-level `pass`/`fail`/`skip` events.

## Cancellation — wire-level, ctx-driven

The daemon spawns a per-request **watcher goroutine** that blocks on a
single inbound kinded frame from the client side of the conn. Cancel or
EOF triggers `ctx.Cancel()`. The streaming verb honors cancellation at
its next checkpoint:

- `grep`: the walker visitor.
- `find`: the walker visitor.
- `test`: `scanEvents` loop. `ctx` is derived from `tr.Context()` and
  passed to `exec.CommandContext`, so the `go test` subprocess is killed
  when the client disconnects.

The `Final` frame the client receives carries `Err.Code="cancelled"`
whenever ctx was cancelled before the verb finished. The verb may have
produced a partial Result; it's discarded.

## Design decision — ctx on Tracer, not first-arg to Run

The approved plan called for adding `ctx context.Context` as the first
parameter to every verb's `Run`, with a 110+ test call-site sweep.
During implementation it became clear that `Tracer` is already the
per-request handoff and adding `ctx` to it is **less invasive and more
in keeping with the existing pattern**.

Net: commit 1 of ASH-106 was ~350 LOC instead of ~1500. The trade is
that ctx is now reached via `tr.Context()` rather than a positional
arg — which is fine because `Tracer` is the canonical per-request
handle every verb already takes.

If we ever want strict ctx-first-arg conventions, the `Runner.Run`
signature can be migrated then; call sites are already centralized in
[internal/verbs/verbs.go](../internal/verbs/verbs.go).

## Ledger columns

Three columns added in ASH-106 (idempotent `ALTER TABLE`, mirrors the
ASH-71 / ASH-123 pattern):

- `streaming` — `1` if the verb emitted at least one Chunk.
- `chunks_out` — total Chunk frames emitted.
- `time_to_first_chunk_us` — µs from request received to first Chunk
  flushed. Consistently low-hundreds-of-µs on the 70-file fixture; the
  bench case to characterize this for realistic loads is the same
  follow-up as the batch-threshold tuning.

ASH-108 added two more, populator deferred:

- `tokens_cache_hit` / `tokens_cache_miss` — reserved for a future
  harness-side telemetry path (MCP `_meta` passthrough or an `ash usage`
  annotation verb). Nobody writes them yet; daemon-originated rows leave
  both at zero.

## Gotchas worth knowing

- **macOS Unix-socket path length is 104 bytes.** `t.TempDir()` produces
  long paths; combined with descriptive test names, streaming-test
  sockets blew the limit and `net.Listen("unix", ...)` returned
  `bind: invalid argument`. Streaming tests use `/tmp` + a short suffix.
  Worth knowing if a new integration test fails opaquely on darwin.
- **Cancel tests are racy with small fixtures.** A 70-file fixture
  finishes grep in ~50 ms — faster than the client can write Cancel
  and the watcher can receive it. The cancel test uses 200 files × 200
  matches and sends `Cancel` *before* reading any chunks, so the
  watcher gets it as early as possible. Tests `skip` (rather than fail)
  on the rare case where the walker still beats the cancel —
  false-negative-resistant.

## Open follow-ups

- **`_meta.ash.streaming: true` schema annotation.** Today the source
  of truth for which tools stream is `streamingVerbs` in
  [cmd/ashmcp/dispatch.go](../cmd/ashmcp/dispatch.go). The MCP spec
  allows arbitrary `_meta` keys on Tool; adding one would make the
  schema artifact self-document which tools emit progress.
- **Configurable batch thresholds.** Once a streaming bench case lands,
  consider `[daemon.streaming]` in `ash.toml`.
- **`test` verb in ashmcp surface.** `test` is currently excluded
  from `exposedVerbs` (side-effecting; ASH-161 shipped write-side
  verbs but kept orchestration CLI-only). When it joins the MCP
  surface, streaming activates with zero additional code —
  `streamingVerbs["test"] = true` is already wired.
- **Token cost of streaming chunks.** Today's `tokens_out` counts only
  the Final frame's pretty render. Chunk frames carry msgpack bytes
  that the harness renders as JSON progress notifications, which the
  harness tokenizes separately. A `tokens_chunks` ledger column would
  close the loop.
- **Empirical cache-prefix scoreboard.** With `ash replay` shipped
  (ASH-112), the proper ASH-108 verification is a replay run before
  and after a candidate change, asserting matching prefix length is
  monotonic. Today `TestResponse_CacheStableWirePrefix` is the proxy.

## Sanity check for cache-stable prefix

After any change touching `proto.Response` or `jsonResponse`:

```sh
bin/ash help --verb read --format json > /tmp/a.json
bin/ash help --verb read --format json > /tmp/b.json
bin/ash diff --path /tmp/a.json --other /tmp/b.json
```

Should produce a clean diff with changes confined to `id` and the
`metrics` block. Everything through `data` must be byte-identical.

`TestResponse_VolatileSuffixOrdering` ([internal/proto/proto_test.go](../internal/proto/proto_test.go))
pins the contract at the type level via reflection so a future struct-
field reorder fails on the type, not on the encoder.
