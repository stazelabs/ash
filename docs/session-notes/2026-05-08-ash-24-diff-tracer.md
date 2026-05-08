# Session: ASH-24 — ash diff IO phase instrumentation

**Task.** Wire proto.Tracer through ash diff so file reads show up as io_us in ash report.

**Verbs used.** ash grep, ash read, ash edit, ash diff (smoke test).

**Changes.**
- `internal/verbs/diff/diff.go`: changed `_ *proto.Tracer` to `tr *proto.Tracer`; added `time` import; wrapped `os.ReadFile(a.Path)` with `t0 := time.Now()` / `tr.AddIO(time.Since(t0))`; same for `os.ReadFile(a.Other)` in the two-file branch. LCS computation not bucketed (falls into `other%` in report, per issue spec).

**Friction.** None — pattern was clear from tracer.go and how the daemon wires things. The tracer nil-safe receiver means no test changes needed.

**Instrumentation.**
- Smoke: `ash diff --format json` now shows `"phases": {"io_us": 124}` instead of absent phases.
- All existing diff tests pass unchanged.
