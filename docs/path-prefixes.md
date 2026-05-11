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

## Phase 5 — Prefix aliasing for `[jail].allow_paths` (ASH-85)

- [internal/jail/relpath.go](../internal/jail/relpath.go) — new `PrefixAliasTable` type: built from `AllowedRoots()[1:]` (the non-project-root allow_paths entries). `Apply(p)` rewrites a path to `@N/<tail>` form if it falls under alias N; handles the macOS `/private`-prefix variant. `Header()` emits the `@N = <path>` table block. `Empty()` is a no-op guard when allow_paths is unconfigured.
- [internal/verbs/find/find.go](../internal/verbs/find/find.go) — `PrettyResponse` creates a `PrefixAliasTable`, prepends its `Header()` after the `=== ===` line when non-empty, and applies `Apply()` to each record path before rendering. Suppressed when `--absolute true`.
- [internal/verbs/grep/grep.go](../internal/verbs/grep/grep.go) — same treatment in `PrettyResponse` (path groups) and `prettyFilesOnly`. Suppressed when `--absolute true`.

**Decision note.** The ticket gated this on measurement: if the path-prefix tax from `allow_paths` entries showed a material fraction of `tokens_out_no_prefix`, build it; otherwise the gain is theoretical. The host repo has no `allow_paths` configured, so a direct measurement was not available. The feature was implemented unconditionally (pretty-only, zero cost when `allow_paths` is empty — the `Empty()` guard is a no-op). This matches the ticket's "existing repo-root-only behavior is unchanged" acceptance criterion: with `allow_paths` empty the output is byte-for-byte identical to before.

Wire-side data (`Result.Records`, `Result.Matches`) is **unchanged** — only pretty rendering applies aliases. JSON/msgpack consumers see the same absolute paths as before.

## Phase 6 — Wire-side path rewrite for single-path verbs (ASH-86)

- [internal/verbs/read/read.go](../internal/verbs/read/read.go), [stat/stat.go](../internal/verbs/stat/stat.go), [diff/diff.go](../internal/verbs/diff/diff.go), [write/write.go](../internal/verbs/write/write.go), [edit/edit.go](../internal/verbs/edit/edit.go) — `Args.Absolute` (default false) added to all five verbs. When false, `Run` rewrites `Result.Path` (or `PathA`/`PathB` for diff, each `Entry.Path` for stat) through `jail.NewProjectRelativizer` before returning. Pretty renderers drop the now-redundant `jail.PrettyPath` call; the value already arrives relative from the daemon. Pass `--absolute true` to opt back into the legacy absolute form.

**Migration note:** JSON/msgpack callers that scrape `Result.Path` expecting absolute form will now receive the bare repo-relative form by default. Pass `--absolute true` to restore the prior behavior.

## ASH-87 audit — `git` verb pretty headers

Audited all four ops with an absolute `--path`/`--pathspec`:

```
ash git --op status   --path     /Users/.../ash           → "=== ash git status: on main -> origin/main ==="
ash git --op log      --pathspec /Users/.../ash/cmd       → "=== ash git log: 0 commits ==="
ash git --op diff     --pathspec /Users/.../ash/cmd       → "=== ash git diff: 0 file(s) +0 -0 ==="
ash git --op show     --ref HEAD --pathspec .../ash/cmd   → "=== ash git show: <hash> — <message> ==="
```

**Result: nothing to strip.** Headers surface branch name, commit count, diff stats, and commit ref/message — none echo the input `--path` or `--pathspec`. Result data already uses repo-root-relative paths (noted in CLAUDE.md). No code change needed; closed as "nothing to do."

## Phase 7 — Path-bearing error messages (ASH-88)

- [internal/verbs/find/find.go](../internal/verbs/find/find.go), [grep/grep.go](../internal/verbs/grep/grep.go), [read/read.go](../internal/verbs/read/read.go), [diff/diff.go](../internal/verbs/diff/diff.go), [edit/edit.go](../internal/verbs/edit/edit.go), [write/write.go](../internal/verbs/write/write.go) — ergonomic error paths (`not_found`, `not_dir`, `is_dir`, `exists`, `no_parent`) now route through `jail.PrettyPath` before formatting. `path_denied` errors are excluded (keep absolute for security signal).
- [internal/verbs/git/git.go](../internal/verbs/git/git.go), [gogit_common.go](../internal/verbs/git/gogit_common.go), [show.go](../internal/verbs/git/show.go), [log.go](../internal/verbs/git/log.go) — `not_a_repo` error helpers (`gitRunError`, `repoOpenError`, `showRunError`, `runLogShellout`) apply the same strip.
- Each touched file has a new test asserting `perr.Msg` does not contain the project-root prefix.

**Decision:** `path_denied` errors (jail enforcement) retain the absolute path — the resolved canonical path is the security signal, telling the agent exactly what was rejected. Everything else is ergonomic and benefits from stripping.

## Out of scope (low priority)
- **Diff patch text paths.** When `--absolute false` (default), `idiff.Unified` is called with the relativized `pathAOut`/`pathBOut`, so `---`/`+++` headers in the patch text also carry the relative form. If callers need the absolute form in the patch text, pass `--absolute true`.
