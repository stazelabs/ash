# ASH-121 — shorten the `[truncation: …]` hint body without losing verb/limit context

## Task

ASH-120 unlocked the `[truncation:` → `[…` and `TRUNCATED` → `…` glyph
compaction (+4 Claude tokens per truncated call). ASH-121 was the
follow-up: the hint *body* — the suggestion list, the embedded ceiling,
the prose framing — had not been measured for token cost vs. agent
utility. Pick a shorter shape, confirm via `count_tokens` cross-check,
and apply to the live verbs (`find`, `grep`, `read`, `git diff`,
`git log`).

## Approach

Added [`encexplore truncbody`](../../cmd/encexplore/truncbody.go) — a
new subcommand that probes candidate body-shape rewrites against the
Anthropic `count_tokens` endpoint, layered on top of the ASH-120 glyph
compaction. Candidates tested against both corpora that carry a real
truncation hint (`grep-common.txt`, `git-log.txt`):

| candidate | grep claude Δ | git-log claude Δ | over baseline |
|---|---:|---:|---:|
| baseline (glyph only) | +4 | +4 | 0 |
| `drop_ceiling_only` (just strip `" (max N)"`) | +10 | +9 | +5–6 |
| `clump_flags` (`/` instead of `, `) | +6 | +6 | +2 |
| `drop_narrow` (drop `"narrow with "` verb) | +6 | +6 | +2 |
| `compact` (clump + drop narrow, keep ceiling) | +8 | +8 | +4 |
| **`compact_keep_raise`** (clump + drop narrow + drop ceiling) | **+14** | **+13** | **+9–10** |
| `drop_raise_clause` (drop whole `, or raise --X (max N)` tail) | +15 | +14 | +10–11 |
| `compact_no_raise` (clump + drop narrow + drop raise) | +17 | +16 | +12–13 |

All candidates agreed in sign between cl100k and Claude (no `✗` rows).

## Decision

Shipped **`compact_keep_raise`**: drop `"narrow with "`, clump flags with
`/`, drop the `" (max N)"` parenthetical, keep the raise-flag (`--max`,
`--limit`, etc.) in the trailing position of the slash-list so the agent
still sees the "you can raise this" cue.

- **Why not the cheaper variants** (`drop_raise_clause`, `compact_no_raise`):
  both lose the raise-flag entirely. An agent who hits `--max=256` on
  `ash grep` would no longer see that `--max` is a flag they can lift —
  the only flags listed would be narrowing ones, which is misleading.
  Saving 2–3 more Claude tokens isn't worth the risk of agents lowering
  `--max` when they meant to raise it.
- **Why not the more conservative variants** (`drop_ceiling_only`):
  `"narrow with"` is redundant with the comma-separated flag list that
  follows it; the verb prose isn't carrying meaning that the flag list
  doesn't already convey. The ticket warned about "muscle memory" but
  the prose isn't load-bearing — drop it.
- **Hard-cap hints** keep `"— --X cannot go higher."` intact, because
  that clause carries semantically-critical info (the agent shouldn't
  try to raise the flag). Only `clump_flags` + `drop_narrow` apply.
- **Diff hint** collapses three actions (narrow, summarize, raise) into
  one slash-list `--pathspec/--stat/--limit_bytes`. The `--stat true`
  value-hint goes; the agent can recover it from `ash help`.

Final shapes (one per call site):

| verb | before | after |
|---|---|---|
| `find` (below cap) | `hit limit of 256 records. narrow with --glob, --type, --depth, or --exclude; or raise --limit (max 4096).` | `hit limit of 256 records. --glob/--type/--depth/--exclude/--limit.` |
| `find` (hard cap) | `hit hard cap of %d records. narrow with --glob, --type, --depth, or --exclude — --limit cannot go higher.` | `hit hard cap of %d records. --glob/--type/--depth/--exclude — --limit cannot go higher.` |
| `grep` (below cap, matches) | `hit max=256. narrow with --glob, --mpf, --exclude, or raise --max (max 4096).` | `hit max=256. --glob/--mpf/--exclude/--max.` |
| `grep` (below cap, files-only) | `hit max=N distinct files. narrow with --glob, --exclude, or raise --max (max M).` | `hit max=N distinct files. --glob/--exclude/--max.` |
| `grep` (hard cap, files-only) | `hit hard cap of N distinct files. narrow with --glob or --exclude — --max cannot go higher.` | `hit hard cap of N distinct files. --glob/--exclude — --max cannot go higher.` |
| `grep` (hard cap, matches) | `hit hard cap of N match records. narrow with --glob, --mpf, or --exclude — --max cannot go higher.` | `hit hard cap of N match records. --glob/--mpf/--exclude — --max cannot go higher.` |
| `read` | `bytes=N hit. narrow with --range or raise --bytes (max M)` | `bytes=N hit. --range/--bytes.` |
| `git diff` | `patch output exceeded N bytes. narrow with --pathspec, use --stat true for a summary, or raise --limit_bytes (max M).` | `patch output exceeded N bytes. --pathspec/--stat/--limit_bytes.` |
| `git log` (below cap) | `hit limit of N commits. narrow with --range/--author/--since/--pathspec, or raise --limit (max M).` | `hit limit of N commits. --range/--author/--since/--pathspec/--limit.` |
| `git log` (hard cap) | `hit hard cap of N commits. narrow with --range/--author/--since/--pathspec — --limit cannot go higher.` | `hit hard cap of N commits. --range/--author/--since/--pathspec — --limit cannot go higher.` |

## What changed

- **[internal/verbs/find/find.go](../../internal/verbs/find/find.go)** —
  `findTruncHint` rewritten; below-cap form no longer references
  `ti.Max`.
- **[internal/verbs/grep/grep.go](../../internal/verbs/grep/grep.go)** —
  all four `grepTruncHint` shapes rewritten; below-cap forms no longer
  reference `ti.Max`.
- **[internal/verbs/read/read.go](../../internal/verbs/read/read.go)** —
  inline truncation hint compacted; no longer formats `r.TruncInfo.Max`.
- **[internal/verbs/git/diff.go](../../internal/verbs/git/diff.go)** —
  `diffTruncHint` rewritten; no longer references `ti.Max`.
- **[internal/verbs/git/log.go](../../internal/verbs/git/log.go)** —
  `logTruncHint` below-cap form rewritten; hard-cap form keeps its
  semantic tail.
- **[internal/verbs/find/find_test.go](../../internal/verbs/find/find_test.go)**,
  **[internal/verbs/grep/grep_test.go](../../internal/verbs/grep/grep_test.go)**,
  **[internal/verbs/git/log_test.go](../../internal/verbs/git/log_test.go)** —
  hard-cap assertions updated: `strings.Contains(hint, "narrow")` → check
  for a specific flag name (`--glob`/`--range`) instead.
- **[testdata/corpus/grep-common.txt](../../testdata/corpus/grep-common.txt)**,
  **[testdata/corpus/git-log.txt](../../testdata/corpus/git-log.txt)** —
  inline `[truncation: …]` lines updated to the new shape. The
  read-medium.txt and cli-tokens.md references to the *old* shape are
  historical design-doc snapshots and were left alone.
- **[cmd/encexplore/truncbody.go](../../cmd/encexplore/truncbody.go)** —
  new probe subcommand; wired into
  [`cmd/encexplore/main.go`](../../cmd/encexplore/main.go). Kept
  (not deleted) so future body-shape probes can extend it; encexplore
  is already a long-lived throwaway-grade tool, mirroring the ASH-120
  precedent for `glyphsweep`.
- **[testdata/validate_results.md](../../testdata/validate_results.md)** —
  regenerated. Corpus aggregate dropped 18040 → 18019 cl100k tokens
  (–21) and 21557 → 21538 Claude tokens (–19) — that's the body-shape
  savings, now baked into the corpus. Zero ✗ rows.

## Verbs used

`ash read`, `ash grep`, `ash find`, `ash edit` (`--old`/`--new` inline
with `$'…'` shell-quoting for multi-line content), `ash write`
(subcommand + session note), `ash test`. `make all`, `make vocab-check`,
`make schema-check`, `make validate`, `make validate-check` — all green.

## Friction

- **Multi-line edits via `ash edit` with `--old $'…'`/`--new $'…'` are
  brittle for big blocks.** When the before/after blocks span ~10
  lines each with tabs, escapes, and embedded quotes, the `$'…'`
  shell-quoting form has to handle every literal special character
  manually. Worked but felt heavy — same friction noted in the ASH-117
  session. The ASH-117 suggestion of `--old @file`/`--new @file` would
  have helped here. Still not enough pain on its own to file a ticket.
- **The harness's built-in `Edit` tool requires a prior harness `Read`,
  but `Read` is hook-denied in this repo.** When `Edit` is the only
  option (e.g. the user-injected workflow), the path is dead end —
  fell back to `ash edit` via Bash, which is the documented path
  anyway. Worth confirming the hook deny-message for `Edit` points at
  `ash edit` (it does).
- **`make validate 2>&1 | tail/grep …` is still hook-denied** even
  inside a pipeline whose head is `make`. Workaround: redirect to a
  tmpfile and `ash read` the tail. (Same workaround as ASH-120; not
  worth a code change.)

## Verification

- `make all` — clean build of all five binaries (`ash`, `ashd`,
  `ashmcp`, plus `ashvocab`, `ashschema`, `encexplore` on demand).
- `bin/ash test --timeout 300s` — 43/43 packages pass.
- `make vocab-check` — `vocab check: ok` (truncation hint bodies aren't
  tracked in the inventory; expected no drift).
- `make schema-check` — `schema check: ok` (no schema impact).
- `make validate` — 96 rows; zero `✗` markers. Corpus aggregate shrinks
  by –21 cl100k / –19 Claude tokens (vs. the pre-change corpus).
- `make validate-check` — `validate-check: ok`.

## Suggestions

- **Per-truncated-call savings are now ~13–14 Claude tokens** for the
  common below-cap case (grep/log corpora), and ~4–6 Claude tokens for
  the hard-cap and read/diff variants (estimated from the deltas — not
  separately probed). For a heavy `find`/`grep` session with 50+
  truncation hits, that's ~600+ Claude tokens shaved. Compounds with
  the ASH-120 `truncation_compact` rule (+4) for a combined ~17–18
  Claude tokens per truncated call.
- **`encexplore truncbody` is parameterizable in principle** but ships
  with hardcoded candidates for ASH-121. If a future ticket wants to
  re-probe — say, after introducing new flags or renaming `--mpf` — the
  candidate list lives at the top of `runTruncBody`; extending it is
  straightforward.
- **The `--stat true` value cue on `git diff` is now gone.** If session
  notes start showing agents calling `ash git --op diff --stat` (bool
  flag set to the default `false`) when they meant `--stat=true`, that
  would be a measurable regression — flag it.
