# Session: ASH-23 — ash diff --stat mode

**Task.** Add `--stat true|false` (default false) to `ash diff` — omit patch, return counts only.

**Verbs used.** `ash find`, `ash grep`, `ash read`, `ash edit`, `ash write`.

**Changes.**
- `internal/verbs/diff/diff.go`: added `Stat bool` to `Args`; `ParseArgs` reads `--stat` via `argutil.OptionalBool`; `Run` returns early (no `idiff.Unified` call) when `Stat=true`; `PrettyResponse` handles patch-less result as a single-line header (no trailing newline or patch block).
- `internal/verbs/diff/diff_test.go`: five new tests covering ParseArgs flag/default, stat-mode changed, stat-mode identical, and pretty single-line output.
- `internal/verbs/help/help.go`: added `stat` arg to diff schema.
- `CLAUDE.md`: updated both the switch-criteria bullet and the live-verb entry.

**Friction.** None.

**Instrumentation.**
- All tests pass: `go test ./internal/verbs/diff/...` green.
- Mirrors existing `ash git --op diff --stat true` — consistent API surface.
