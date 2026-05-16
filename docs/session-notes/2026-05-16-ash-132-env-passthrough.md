# ASH-132: `ash test` should propagate client env to the `go test` subprocess

## Task

Forward the client shell's environment variables to the `go test` subprocess
spawned by `ash test`. Pre-fix, the subprocess inherited `ashd`'s env (frozen
at daemon startup), so per-call vars like `UPDATE_GOLDEN` set in the agent's
shell silently never reached the test.

## Verbs used

- `ash test`, `ash edit`, `ash write`, `ash stat`, `ash grep`, `ash find`,
  `ash help`, `ash git --op status|diff`
- bash: `make all`, `make vocab(-check)`, `make schema-check`, `make
  validate-check` — all gated by the hook, which redirected correctly when
  used.

## Implementation

Tracer is the seam: it already carries per-request context to verbs; teaching
it `Env []string` keeps the runner abstraction unchanged.

- `proto.Request` gains an `Env []string` wire field (msgpack `omitempty`).
- `cmd/ash/main.go` populates `req.Env = os.Environ()` only for `verb == "test"`
  (other verbs don't shell out; sending env unconditionally would inflate the
  request and surface client secrets needlessly).
- `cmd/ashd/main.go` calls `tracer.SetEnv(req.Env)` once per dispatch.
- `internal/verbs/test/test.go` reads `tr.Env()` and sets `cmd.Env` when
  non-nil; nil keeps the legacy implicit-inherit path so older clients are
  unchanged.
- `internal/proto/tracer.go` adds `SetEnv` / `Env()` methods (nil-receiver
  safe, matching the existing `SetContext` / `Context` shape).

Env is intentionally NOT persisted to the ledger: the existing `argsBlob()`
encodes only `req.Args`, so secrets in `req.Env` can't leak into
`args_msgpack`.

## Verification

- New `cmd/ashd/env_test.go` drives the real wire path against the new
  `internal/envprobe` fixture package. The fixture's `TestEnvProbe` skips
  when `ASH_132_PROBE` is unset (keeps `go test ./...` green) and asserts on
  the exact value otherwise. The integration test sends two requests with
  different `ASH_132_PROBE` values via `req.Env`; passing one and failing
  the other proves the *specific* value forwarded reaches the subprocess.
- `internal/proto/tracer_test.go` adds a unit test for the `SetEnv`/`Env`
  round-trip and nil-receiver safety.
- Manual repro from the bug report works:

  ```
  UPDATE_GOLDEN=1 ash test --packages cmd/ash --run TestRenderUsage_Golden
  ```

  The `cmd/ash/testdata/usage_golden.txt` mtime advances (subprocess saw
  the env var); content is byte-identical so `git status` stays clean
  unless the source actually drifted.
- Full suite: `ash test` → 44 pkgs, 0 fail.
- `make vocab` regenerates `docs/vocab/inventory.json`: 37 line-number
  shifts from the comment block added to `internal/verbs/test/test.go`,
  no actual surface change.

## Friction

Two macOS-specific snags caught in the integration test:

1. `t.TempDir()` lands under `/var/folders/...`, which busts the 104-byte
   `SUN_PATH` cap once you append `ash.sock`. Used `os.MkdirTemp("/tmp",
   "ash132-")` instead with manual cleanup.
2. `go test <abs-path>/...` errors with "directory prefix ... does not
   contain main module" when the path is outside the active module. The
   integration test's CWD defaults to `cmd/ashd/` where
   `./internal/envprobe` doesn't exist; the existing
   `TestIntegration_AllVerbs` "test" case papers over this (it only
   asserts on verb-level OK, not Result.OK), but a load-bearing assertion
   has to `os.Chdir(repoRoot)` first.

Neither warrants a follow-up — both are local quirks of the daemon-in-
process test harness rather than verb design issues.

## Suggestions

None. The fix is small and local; the Tracer-as-seam choice means future
verbs that need to shell out (`run`, `exec` per the README roadmap) can
opt into env passthrough by calling `tr.Env()` with no protocol change.

## Instrumentation

UPDATE_GOLDEN repro before/after the fix:

| run | mtime (golden_file)        | rebuilt? |
|-----|-----------------------------|----------|
| before fix (ASH-112 session note) | unchanged                   | no       |
| after fix (this session)          | 1778937020 (mtime advanced) | yes      |
