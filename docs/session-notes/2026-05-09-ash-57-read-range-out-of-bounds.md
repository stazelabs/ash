# ASH-57: read applyRange — range_out_of_bounds instead of malformed range_returned

**Task.** Fix `applyRange` returning `"5:4"` (start > end, violating the verb's own input contract) when the requested start is past the end of the file. Replace with `range_out_of_bounds` error, consistent with `ash edit`.

**Verbs used.** `ash read`, `ash edit`, `ash grep`, `ash find`, `ash test`

**Changes.**

`internal/verbs/read/read.go`:
- Bytes mode: `start > len(body)` now returns `&proto.Error{Code: "range_out_of_bounds", ...}` instead of `body[:0], "start:start-1"`.
- Lines mode: `lineStart < 0` now returns the same error.

`internal/verbs/read/read_test.go`:
- `TestApplyRange_LinesPastEnd` — flipped to expect `range_out_of_bounds` error.
- `TestApplyRange_Bytes` — removed the `"past end"` table case.
- New `TestApplyRange_BytesPastEnd` — expects `range_out_of_bounds` error.

**Friction.** None — mechanical change.

**Instrumentation.**
```
verb    n  ok%   p50_exec
read    2  100%  ~290ms (file IO)
test    2  100%  ~220ms
```
