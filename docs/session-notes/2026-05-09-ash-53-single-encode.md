# 2026-05-09 — ASH-53: eliminate double-encode in daemon handle()

## Task
Remove the double `proto.EncodeResponse` call in `cmd/ashd/main.go` and make
`LatencySerializeUs` / `bytes_out` honest in the ledger.

## Verbs used
`ash read`, `ash edit`, `ash test`, `ash metrics`, `ash git --op status`

## What changed

**`internal/ledger/ledger.go`**
- `Record()` now returns `(int64, error)` — the auto-increment row ID.
- New `UpdateSerializeStats(rowID, bytesOut, serializeUs)` patches the row
  with accurate post-encode values via a single SQL UPDATE.

**`cmd/ashd/main.go`**
- Removed the first `proto.EncodeResponse` call (previously used only to
  learn `bytes_out` before metrics were attached).
- `BytesOut` and `LatencySerializeUs` in the initial `Record()` call are
  now 0 (placeholder); `UpdateSerializeStats` patches them after the single
  final encode. LedgerError is still set before encoding so it appears on
  the wire — that ordering is preserved.

**`cmd/ash/main.go`**
- `rsp.Metrics.BytesOut` is now computed from `len(respBuf)` (actual wire
  frame size) rather than from the wire metrics field (which is 0).
- Serialize latency removed from the stderr footer (`latency_us=parse/exec`
  instead of `parse/exec/serialize`). The accurate value is in the ledger.

## Trade-offs accepted
- Two DB ops per call (INSERT + UPDATE) instead of one. SQLite WAL, small
  rows — sub-millisecond and not observable in practice.
- `bytes_out` in the ledger now = total wire frame size (data + metrics
  envelope), not just the data portion. ~100–200 bytes larger than before;
  `TokPerKiB` in reports stays meaningful.
- Serialize latency no longer in the stderr footer; query `ash metrics` for
  it. This is strictly more honest (old value was `serFirstUs * 2`, a lie).

## Ledger verification
```
sqlite3 .ash/ledger.db "SELECT verb, bytes_out, latency_serialize_us FROM calls ORDER BY id DESC LIMIT 5"
hook|251|10
metrics|765|33
hook|221|10
git|449|39
hook|221|11
```
Both columns non-zero and plausible. ✓

## Test result
29/29 packages pass, including `TestHandle_LedgerErrorOnWire` and
`TestHandle_NoLedgerErrorOnHealthyDB`.
