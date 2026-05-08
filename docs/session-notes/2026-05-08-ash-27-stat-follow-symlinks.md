# Session: ASH-27 — stat --follow_symlinks

**Task.** Implement `--follow_symlinks true|false` (default false) for `ash stat`.

**Verbs used.** `ash find`, `ash grep`, `ash read`, `ash edit`, `ash stat` (smoke test).

**Changes.**
- `internal/verbs/stat/stat.go`: added `FollowSymlinks bool` to `Args`; `ParseArgs` reads `--follow_symlinks` via `argutil.OptionalBool`; `statOne` takes the flag and, when true and entry is symlink, calls `os.Stat` to merge target metadata; broken links → `error=broken_symlink`, type stays `symlink`, `link_target` preserved.
- `internal/verbs/stat/stat_test.go`: four new tests (follow resolves file, broken_symlink error, ParseArgs true, ParseArgs default).
- `internal/verbs/help/help.go`: documented `follow_symlinks` arg in stat schema.
- `CLAUDE.md`: updated live verb entry to include `--follow_symlinks` and `broken_symlink` error code.

**Friction.** None — straightforward feature addition. `ash edit` handled all file mutations cleanly.

**Instrumentation.**
- All 4 new tests pass; full `go test ./internal/verbs/stat/...` green.
- Smoke test: plain symlink = `L`, followed symlink = `F` with target size, broken followed = `[broken_symlink]`.
