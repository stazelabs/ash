# ASH-71 — Token optimization for path prefixes

## Context

`ash` tokenizes every pretty-rendered response with `cl100k_base` and stores `tokens_out` in the ledger. Path-heavy verbs carry the same prefix on every line — for `find --path /Users/.../ash`, the absolute repo root appears in every result. ASH-71 sized this and removed it across the surface.

## What shipped

### Phase 1 — Ledger instrumentation

- [internal/ledger/ledger.go](../internal/ledger/ledger.go) — new `tokens_out_no_prefix` column on `calls`, with an idempotent `ALTER TABLE` migration so existing ledgers backfill without losing rows. `Call.TokensOutNoPrefix` plumbs through `Record`, `QueryWindow`, `QueryRecent`.
- [internal/ledger/tokens.go](../internal/ledger/tokens.go) — `StripPrefixes(s, prefixes)` runs longest-first to avoid sub-prefix collisions.
- [cmd/ashd/main.go](../cmd/ashd/main.go) — second tokenize pass against `ledger.StripPrefixes(prettyRsp, jail.PathPrefixes())`.

### Phase 2 — `ash report` surfaces the tax

- [internal/verbs/report/report.go](../internal/verbs/report/report.go) — `Totals.TokensOutNoPrefix` and `VerbStats.TokensOutNoPrefix` populated from the ledger; pretty totals append `path-prefix tax: <N> tokens (<P>% of out)` when material; suppressed when zero (so pre-migration rows backfilled to 0 don't show a misleading 100% tax).

### Phase 3 — `find` defaults to bare repo-relative

- [internal/verbs/find/find.go](../internal/verbs/find/find.go) — `Args.Absolute`, default rewrites `Record.Path` via `jail.NewProjectRelativizer`. macOS-style `/var/folders` to `/private/var/folders` symlink resolved via EvalSymlinks.
- [internal/verbs/find/find_test.go](../internal/verbs/find/find_test.go) — covers default-relative, `--absolute` opt-out, no-policy fallback, paths outside project root.

### Phase 4 — Path-emitting verbs sweep

- [internal/verbs/grep/grep.go](../internal/verbs/grep/grep.go) — same treatment as `find`: `Match.Path` and `Files` flow through `jail.NewProjectRelativizer`; `--absolute true` opts back. Tests mirror find's.
- [internal/verbs/read/read.go](../internal/verbs/read/read.go), [stat/stat.go](../internal/verbs/stat/stat.go), [diff/diff.go](../internal/verbs/diff/diff.go), [write/write.go](../internal/verbs/write/write.go), [edit/edit.go](../internal/verbs/edit/edit.go) — pretty headers render `jail.PrettyPath(path)` so exact-root renders as `.` and sub-paths render bare. Wire-side `Result.Path` is untouched; JSON consumers see the original input echo.
- [internal/verbs/report/report.go](../internal/verbs/report/report.go) — `collectArgDists` and `decodeArgsSummary` run string values through a `prettyArgValue` helper combining mid-string `StripPrefixes` (catches embedded paths in hook commands, comma-separated `--paths` lists) with exact-match collapsing to ".".

### Shared helpers in `jail`

- [internal/jail/relpath.go](../internal/jail/relpath.go) — `ProjectRelativizer` (data-side rewrite for find/grep) and `PrettyPath` (cosmetic strip for pretty-only callers). Both pure string operations once constructed; safe to call thousands of times.
- [internal/jail/policy.go](../internal/jail/policy.go) — `AllowedRoots()` (canonical only) for callers that need the project root; `PathPrefixes()` (longest-first list including lexical-abs duplicates) for prefix-stripping callers.

### Client-side wiring

The pretty renderers also run on the client (the verb registry is shared between `cmd/ash` and `cmd/ashd`). [cmd/ash/main.go](../cmd/ash/main.go) now calls `jail.SetPolicy(jail.FromConfig(false, root, …))` so `jail.PathPrefixes` returns the project root on the client too. Without this, client-side rendering would skip the strip and the agent would see absolute paths in headers despite the daemon ledger having already counted them as stripped.

## Verified live

After all phases land on this repo:

- `ash find --path /Users/.../ash --glob '**/*.go' --limit 8` — bare repo-relative paths, ~40% fewer tokens than `--absolute true`.
- `ash grep --pattern Foo --path /Users/.../ash/internal/verbs/find` — header scope shows `path=internal/verbs/find`; match rows are bare relative.
- `ash read --path /Users/.../ash/ash.toml.example` — header shows `=== ash.toml.example [...] ===`.
- `ash stat --paths /Users/.../ash/ash.toml.example,/Users/.../ash/Makefile` — both rows bare.
- `ash diff --path /Users/.../ash/cmd/ash/main.go --other /Users/.../ash/cmd/ash/hook.go` — header `cmd/ash/main.go vs cmd/ash/hook.go`.
- `ash report --since 5m` — no `path-prefix tax` line emitted across these verbs (residual tax is 0). Arg distributions show `path: .`, `path: cmd/ash/main.go`, hook commands with embedded paths stripped to relative.
- Full test suite: 33/33 packages pass.

## Deferred

- **ASH-71d prefix aliasing for `[jail].allow_paths`.** With `allow_paths` empty in the host repo (`PathPrefixes` returns just the project root), the existing strip already collapses everything to bare. Revisit when measurements on a config that actually uses `allow_paths` show residual tax in `tokens_out_no_prefix` deltas.

## Out of scope (low priority)

- **Wire-side path rewrite for single-path verbs.** `read`/`stat`/`diff`/`write`/`edit` still emit absolute `Result.Path` in JSON even though pretty renders bare. A `--absolute` flag mirroring find/grep would cover JSON callers that want compactness; defer until asked.
- **`git` verb headers.** Result data already uses repo-root-relative paths. Pretty headers may still echo input `--path` arg unstripped; not audited.
- **Path-bearing error messages.** Errors like `not_found: /Users/.../foo: no such path` bypass the cosmetic strip. Low frequency; defer.
