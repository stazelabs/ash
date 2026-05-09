# Session note: ASH-60 — ash edit --patch mode

## Task

Implement `ash edit --patch -` and `--patch <file>`: accept a unified diff from stdin or a named file and apply it to the target file, eliminating the shell-quoting escape hatch documented in CLAUDE.md.

## Verbs used

`ash read`, `ash grep`, `ash find`, `ash write`, `ash edit`, `ash diff`, `ash test`, `ash git`

## What shipped

- `internal/verbs/edit/edit.go`: third edit mode (`patch`), exclusively with `old_string`/`range`; pure-Go unified diff parser (`parsePatch`, `parseHunkHeader`) and applier (`applyPatch`); no external dependency
- `cmd/ash/main.go`: `resolvePatchFile` function resolves `--patch <filepath>` client-side; call inserted after `resolveStdin`
- `internal/verbs/help/help.go`: `patch` arg added to edit schema
- 56 tests in edit package, all pass; 29 total packages pass

## Error codes

- `patch_parse_error`: malformed diff (empty, no hunks, bad hunk header, unknown prefix)
- `patch_failed`: hunk mismatch (context or delete line doesn't match file content)

## Friction

- The hook blocks the harness `Read` tool, so the `Edit` tool's "must read before edit" check fires even after reading via `ash read`. Used `ash write` for whole-file writes when changes were too large for `ash edit --range`. 
- `ash edit --range N:N --new_content -` with heredoc was used for targeted insertions.

## Instrumentation

Ledger records patch-mode calls identically to other edit modes. Occurrences field carries hunk count (useful: 1 hunk vs N hunks applied).

## Suggestions

None beyond what shipped. The `ash diff | ash edit --patch -` composition works as designed.
