# 2026-05-09 — ASH-52: extract writeAtomic into shared helper

## Task
Refactor the four duplicated / functions (write, edit, initverb, uninit) into a single  package.

## Verbs used
ash read, ash grep, ash edit, ash write, ash test, ash help

## What changed
- Created `internal/atomicwrite/atomicwrite.go` with `Write(path, data, Options)`.
- `Options.PreserveMode` — explicit flag for mode preservation (was implicit in edit only).
- `Options.TmpPrefix` — per-verb temp-file prefix for debuggability.
- **Removed the silent fallback** that both write and edit had: on tempfile-create or rename failure they fell back to `os.WriteFile(path, data, 0o644)`, silently sacrificing atomicity and clobbering the original file mode. The shared helper surfaces errors instead.
- Fixed a latent bug in uninit's `atomicWrite`: if `os.Rename` failed, the temp file was never removed. The shared helper cleans up on rename failure.
- All four callers (write, edit, initverb, uninit) now import the shared package.

## Friction
None significant. `ash edit` handled all the in-place replacements cleanly. The shell-quoting escape hatch (apostrophe in comment) came up once — worked around with double-quoting on the outer shell call.

## Instrumentation
Full test suite: 30/30 pass. No regressions.
