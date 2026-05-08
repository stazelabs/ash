# 2026-05-07 — Ship 1 bringup

## Task

Build the Phase 1 Ship 1 vertical slice: `ash read` end-to-end. Daemon, client, MessagePack envelope over UDS, SQLite ledger with real cl100k token counts, auto-start lifecycle. From an empty Go module to first round-trip.

## Verbs used

`ash read` (the verb being built). All other operations were bash + the harness Read/Write/Edit tools — `ash` itself can only read at this stage, and even that became viable only at the end of the session.

## Friction

Three concrete bugs surfaced during smoke-testing:

1. **Pretty-printer type-blindness.** `read.PrettyResponse` decoded `Response.Data` as `map[string]any` only. The wire-side client receives that shape after msgpack-unmarshal, but the *daemon* path passes a typed `*read.Result` straight in. Result: daemon-side token measurement fell back to the `<unrecognized read result>` placeholder, and `tokens_out` reported 8 instead of ~110 for a 5-line README slice. Found by the metrics line itself looking implausibly small. Fix: handle both `*read.Result` and `map[string]any` in the decoder.

   **Lesson:** any helper that runs on both sides of the wire (here: the daemon for token measurement, the client for display) needs to handle both the typed and the loosely-decoded form. Worth a comment in the helper.

2. **`uint64` request IDs vs `database/sql`.** `crypto/rand`-derived `uint64` IDs collide with `database/sql`'s rule that `uint64` values with the high bit set are not accepted as `driver.Value`. ~50% of random IDs fail. Symptom: silent — daemon log showed `ledger record: sql: converting argument $2 type: uint64 values with high bit set are not supported`, but the response had already been sent, so the client never saw a problem. Fix: cast to `int64` at the insert site (bit pattern preserved; reads cast back).

   **Lesson:** ledger-record failures need to be loud, not log-only. A future verb that fails to record is a verb whose metrics evaporate — exactly the failure mode that undermines the whole instrumentation thesis. Worth either failing the response or surfacing in metrics.

3. **Doubled log lines.** The daemon was using `log.SetOutput(io.MultiWriter(os.Stderr, logF))` to tee output. But the client redirects the daemon's stderr to that same `logF` when it auto-spawns. So every log line landed in the file twice. Cosmetic, found by inspection. Fix: drop the MultiWriter; write only to `logF`.

Plus one minor ergonomic friction: rebuilding after a fix requires two `go build` invocations (`./cmd/ash` and `./cmd/ashd`). `go build ./...` works but doesn't put binaries in `bin/`. Worth a tiny Makefile next round.

## Workarounds

None. All three bugs were fixed in flight before declaring Ship 1 done.

## Suggestions

In rough priority order for what `ash` itself should grow:

- **Loud ledger failures.** A verb that returns a successful response but failed to record should mark that fact — either in `metrics` (a `ledger_recorded: false` flag) or by failing the call. Quiet ledger failures are the worst possible outcome for a project that lives or dies by its instrumentation.
- **`metrics` (or `ledger`) verb.** Mid-session self-querying without shelling out to `sqlite3`. "Show me the last 10 calls" should be one `ash` call, not a context switch. Especially valuable when the ledger is the substrate for the entire iterative-development thesis.
- **`help` verb.** Returns the schema for any verb. The client args parser is currently lossy (every value is a string until the verb's `ParseArgs` coerces); a structured schema makes the contract explicit and lets future tooling validate up-front.
- **Auto-start error surfacing.** If `ashd` fails to start (binary missing, port collision, etc.), the client currently only sees a 2-second poll timeout and reports "daemon did not come up." The actual stderr from the failed `Start()` exec is in the log file but not surfaced. A `--verbose` or unconditional log-tail on auto-start failure would save a lot of head-scratching.
- **Pre-warm tiktoken.** First call after daemon start showed exec=801µs; warm calls showed 38µs. The tiktoken offline-loader initializes lazily on first encode. Pre-warming in `ledger.NewCounter` would put that latency on daemon boot instead of in the first user-visible request.
- **`--format json` (or `--format msgpack`) on the client.** Pretty is the only mode now. A pipe-friendly format opens up scripting and benchmarking.
- **Tiny Makefile.** `make` builds both binaries to `bin/`. Trivial, eliminates a small papercut.

## Instrumentation

Three calls in `.ash/ledger.db`, session `1625f5847ff015d9`:

| call | verb | ok | tokens_in | tokens_out | latency_exec_us |
|---|---|---|---|---|---|
| 1 | read README.md range=1:3 | yes | 12 | 61 | 801 |
| 2 | read go.mod | yes | 6 | 331 | 38 |
| 3 | read no-such-file.txt | no (not_found) | 9 | 13 | 4 |

Two observations:

- **Cold-start tax.** First call's 801µs is dominated by tiktoken-go's lazy BPE-table init. Once warm, exec latency dropped 21x. Worth pre-warming.
- **Token cost per response is dominated by the body, not the envelope.** A 21-line `go.mod` returns 331 tokens via `ash read`'s pretty form. The harness `Read` tool, by comparison, prefixes every line with a number — adding a few tokens per line. We don't have a clean comparison datapoint yet because the harness's tokenization is opaque to us, but this is the kind of thing the ledger will let us answer once `find` and `grep` ship and we can run the same queries through both.

## Next session

- Ship 2: `ash find`. Path/glob/type/size filters, structured records, default limits with truncation hints, ledger continues from same DB.
- Before that, address suggestion #1 (loud ledger failures) — it's a methodology-load-bearing fix, not a feature.
