# ASH-148 — wirecmp `find --meta` fixture (and the bug it found)

**Task.** Add `find --meta true` as a wirecmp fixture so the linear-scaling prediction in [docs/mcp/wire-cost.md](../mcp/wire-cost.md) — "list-of-records verbs pay tax 2 linearly with record count" — has a real data point beyond the single-record `stat` fixture. Originally scoped as a 30-minute fixture-add; ended up uncovering and fixing a long-standing wirecmp bug that invalidated every prior find/grep number in this doc.

**Verbs used.** `ash read`, `ash find`, `ash grep`, `ash git --op log/status`, `ash edit @PATH`, `ash write`, `ash test --packages …`, plus `go build -o bin/wirecmp` and one `bin/wirecmp -claude` run. Also a short-lived `cmd/wirecmpdebug` Go program for one-shot wire inspection (deleted before commit).

## The fixture

```go
{"find **/*.go --meta (20)", "find", map[string]any{
    "path": ".", "glob": "**/*.go", "limit": "20", "meta": "true",
}},
```

Sits right after the existing `find **/*.go (20)` fixture so both rows walk the same 20 records and any delta isolates the `meta` shape difference.

## The bug it found

First run produced suspicious numbers: `find` and `find --meta` reported **identical** 41-byte CLI renders. That's structurally impossible — `--meta` adds `<F|D|L> <size> <yyyy-mm-dd> ` before each path, so the renders should differ by at least ~20 bytes per record.

Spun up a tiny debug program (`cmd/wirecmpdebug/main.go`) that mirrored wirecmp's `roundtripOnce` and printed the rendered text. Output:

```
rsp.OK=false err=&{Code:args Msg:limit must be a positive integer Hint:} len(Data)=0
len(pretty)=41
pretty="err args\nlimit must be a positive integer"
```

So `find` was hitting an `args` error and the 41-byte render was the *error envelope*, not real verb output. Same for `grep` with `--max 20`.

**Why.** wirecmp's fixtures built `map[string]any{"limit": 20}` with a Go-native `int`. msgpack encodes positive ints 0–127 as a single-byte positive fixint; the daemon decodes that into `uint8(20)` when target is `any`. `argutil.ToInt` only matches `int`, `int64`, `uint64`, `float64`, `string` — no `uint8`. So the limit was rejected; the daemon returned `args: limit must be a positive integer`; the pretty renderer fell through to `proto.PrettyResponseHeader(rsp)` which emits `err args\n<msg>` = 41 bytes.

This has been broken since [ASH-123](https://linear.app/stazelabs/issue/ASH-123) (the original wirecmp fixture set). Every "find / grep" row in the post-ASH-123, post-ASH-124, and post-ASH-147 snapshots of [docs/mcp/wire-cost.md](../mcp/wire-cost.md) was measuring this error envelope, not real verb work.

**Why production was fine.** The two production paths into the daemon — CLI `parseFlags` and ashmcp's `decodeArgs` — never produce raw Go `int`:

- CLI: `parseFlags` produces `map[string]any{"limit": "20"}` (string). `argutil.ToInt`'s string arm handles it.
- ashmcp: `decodeArgs` calls `json.Unmarshal` into `map[string]any`. JSON numbers decode to `float64`. `argutil.ToInt`'s float64 arm handles it.

So only callers that build requests programmatically in Go and pass to msgpack are exposed. That's wirecmp; it could also be future internal tools, replay sessions decoding ledger args, or any agent that talks msgpack directly.

## The fix (this commit)

Two pieces:

1. **Add the `find --meta` fixture.** As scoped.
2. **Pass numeric args as strings in all wirecmp fixtures.** Matches the CLI's `parseFlags` shape, sidesteps the msgpack→uint8 trap. Documented inline in `cmd/wirecmp/main.go` with a pointer to [ASH-148](https://linear.app/stazelabs/issue/ASH-148) / [ASH-149](https://linear.app/stazelabs/issue/ASH-149).

The underlying `argutil.ToInt` widening is filed as **[ASH-149](https://linear.app/stazelabs/issue/ASH-149)**: add `int8`/`int16`/`int32`/`uint`/`uint8`/`uint16`/`uint32`/`float32` arms, add round-trip tests through msgpack, then revert the wirecmp workaround. That's the right fix; ASH-148 is the immediate measurement unblocker.

## The numbers (post-fix, real this time)

| fixture | CLI claude | MCP claude | Δ vs CLI |
|---|---:|---:|---:|
| read README:1-60 | 1221 | 1317 | +8% |
| find (20) | 250 | 733 | **+193%** |
| find --meta (20) | 490 | 733 | **+50%** |
| grep ^func Run | 941 | 1072 | **+14%** |
| stat README.md | 26 | 48 | +85% |
| git status | 65 | 80 | +23% |
| help | 463 | 4696 | +914% |

Aggregate Δ vs CLI: **+151%** (was reported as +243% on the pre-fix snapshot, because find+grep were error rendering on both sides which is close to parity). Excluding help: **+33%** (was reported as +10%; same reason).

The +33% read-side aggregate is the real number to anchor on — it's what an MCP harness pays vs the CLI pretty render across a typical session mix, with `help` excluded because it's a schema dump, not a typical call.

## Linear scaling — the prediction held

Comparing the single-record `stat` row to the 20-record `find` rows:

| | stat (1) | find --meta (20) | per-record |
|---|---:|---:|---:|
| MCP claude − CLI claude | +22 | +243 | **~12 claude/record** |

| | stat (1) | find no-meta (20) | per-record |
|---|---:|---:|---:|
| MCP claude − CLI claude | +22 | +483 | **~24 claude/record** |

`find --meta` has the smaller per-record cost because the CLI side is also rendering meta — both transports ship ~the same density of data, so MCP only pays the JSON framing overhead. Without meta, the CLI is leaner (single-line records) and MCP's structured cost shows up more sharply.

[ASH-146](https://linear.app/stazelabs/issue/ASH-146) (tax 2 closure) now has a concrete design target: ~12 Claude tokens per record. The two candidate shapes in that ticket — `--format pretty|json` knob and the structured-pretty tuple form — both attack this number directly.

## Surprises

- The "MCP is cheaper than CLI for terse responses!" claim in earlier wire-cost narrative was completely wrong. Both transports were rendering the same `args` error envelope, which happened to encode shorter over JSON than over the CLI's `err <code>\n<msg>` form by a single token. That was the source of the "-6% Δ vs CLI" numbers that suggested MCP had a structural advantage on small payloads. It does not.
- The fixture bug went undetected through three wire-cost.md updates (ASH-123, ASH-124, ASH-147) because the find/grep rows always came back small and the wirecmp summary line in stderr (`cli=41B/9t mcp=38B/8t`) looked plausible for "small response, small envelope." Nobody (including me three commits ago) bothered to print the actual rendered text.
- ASH-124's per-fixture deltas (find went +294% → -6% pre→post) looked like a real protocol win and shaped how the doc framed ASH-124's success. Post-ASH-148 we know that was the wrapper-removed error envelope vs the wrapped error envelope. The real ASH-124 win was on `stat` (one structured record, real data, +246% → +85%) and on `help` (real data, scope-creep-noted) — and on read (which always worked because the path arg never hit ToInt). The wins are still there; just smaller and more specific than the previous narrative claimed.

## Friction

- **`ash git --op diff` byte cap on JSON blobs.** Same pattern as the ASH-147 inventory.json case. Two encounters in two sessions; worth a verb tweak to truncate context rather than drop the whole patch when the file is JSON or one long line.
- **Go's `go run` can't reach internal packages from `/tmp`.** Workaround: put the debug program inside the repo at `cmd/wirecmpdebug/main.go`, run, remove. Mildly annoying.
- **The hook's harness-Edit denial requires the `ash edit @PATH` round-trip pattern** (`ash write` two tmpfiles, then `ash edit --old @… --new @…`). Four invocations per multi-line replacement. Worked, but it adds up across an editing session. Probably the right shape until something like `ash patch` lands.

## Files changed

- `cmd/wirecmp/main.go` — added `find **/*.go --meta (20)` fixture; switched numeric args (`limit`, `max`) to string-typed values; added explanatory comment with ASH-148/ASH-149 pointers.
- `docs/mcp/wire-cost.md` — full rewrite with post-ASH-148 snapshot, correction notice at top, linear-scaling sub-table, and a "Pre-vs-post ASH-148 corrections" section that retracts the prior find/grep numbers.
- `docs/session-notes/2026-05-15-ash-148-wirecmp-find-meta-fixture.md` — this note.

[ASH-149](https://linear.app/stazelabs/issue/ASH-149) filed as the underlying-fix follow-up.

`vocab-check`, `schema-check`, `validate-check` all green; 43/43 packages pass.
