# ASH-58: OptionalPosInt rejects explicit 0 — report --last and test --count

**Task.** Fix `ash report --last 0` erroring and unwind the `test.go` count-zero workaround by switching both to `OptionalNonNegInt` (which already existed).

**Verbs used.** `ash read`, `ash edit`, `ash grep`, `ash find`, `ash test`

**Changes.**

`internal/verbs/report/report.go`:
- `last`: `OptionalPosInt` → `OptionalNonNegInt`; removed the stale workaround comment.

`internal/verbs/test/test.go`:
- `count`: replaced the 15-line try/error/recheck block with a single `OptionalNonNegInt` call. `TestParseArgs_CountZeroAllowed` already covered the desired behavior; it now passes through the clean path.

`internal/verbs/report/report_test.go`:
- New `TestParseArgs_LastZeroAllowed`: verifies `last=0` is accepted and produces `a.Last=0`.

**Friction.** None — `OptionalNonNegInt` was already in argutil with its own tests. Pure mechanical swap.

**Instrumentation.**
```
verb    n  ok%   p50_exec
test    2  100%  ~350ms
```
