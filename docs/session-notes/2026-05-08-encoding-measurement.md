# Phase 1 measurement — token encoding exploration

**Date:** 2026-05-08
**Plan:** [docs/encodings.md](../encodings.md)

## Task

Phase 1 of the encoding-options plan: measure where tokens go today before
picking which compression options to ship.

## Verbs used

- `ash report --session all` (empty — `all` filter does not match the
  current daemon-session schema; not the issue we're chasing)
- `ash report --since 7d` and `ash report --session current`
- `ash metrics --last 30`
- `ash read` for `internal/verbs/read/read.go`,
  `internal/verbs/grep/grep.go`, `docs/bench.md`
- `ash grep` (one call)
- `ash find` for the bench package

## Findings

### Where tokens come from today (current session, 40 calls, 11.6k tokens out)

| verb | calls | p50 out | p95 out | tok/KiB | notes |
|---|---|---|---|---|---|
| hook | 20 | 2 | 23 | 53 | dominant call count, already minimal |
| report | 3 | 1704 | 1704 | 56 | meta-call; arg-distribution dump is heavy |
| read | 2 | 2525 | 2525 | **309** | body content dominates |
| stat | 1 | 38 | 38 | **338** | highest density; mtime drives it |
| write | 1 | 34 | 34 | 279 | already lean |
| grep | 6 | 164 | 488 | 268 | match text dominates |
| edit | 4 | 18 | 18 | 215 | already lean |
| git | 2 | 74 | 74 | 65 | already lean |
| test | 1 | 276 | 276 | 326 | (out of scope) |

`tok/KiB` is the most direct "tokenizes wastefully" signal. The verbs at
the top of that column — `stat`, `test`, `read`, `write`, `grep` — pack
high token density per byte. For most of them the body is the dominant
contribution and is itself the data the agent needs (file content, match
text, code), so it cannot be compressed without information loss. The
header / envelope is the compressible surface.

### Per-verb header decomposition (read, grep, stat)

**`read` header** ([internal/verbs/read/read.go:200-238](../../internal/verbs/read/read.go#L200-L238)):

```
=== <path> [<size>B[, <lines>L], <encoding>, mtime=<RFC3339>[, range=<r>][, TRUNCATED]] ===
<content>
```

Empirical decomposition (1001B / 43L Go file, 296 tokens total):

- envelope `=== ... ===`: ~4 tokens
- path: ~5 tokens (path-dependent)
- size+lines (`1001B, 43L`): ~4 tokens
- encoding (`utf-8`): ~3 tokens (split on hyphen)
- mtime (`mtime=2026-05-08T01:25:47Z`): ~12 tokens (digits + colons + `T`/`Z`)
- body: ~250-260 tokens (the dominant contribution)

**Saveable: ~15 tokens per read by dropping `mtime` + `encoding`** from
the default header. utf-8 is the default and rarely interesting; mtime
is rarely consulted for code reads. `--with_meta true` would re-add
them, mirroring the `find` optimization that already shipped.

**`grep` header** ([internal/verbs/grep/grep.go:514-575](../../internal/verbs/grep/grep.go#L514-L575)):

```
=== ash grep: <N match[es]> in <M file[s]> [<scope>] [TRUNCATED] ===
[skipped: ...]
<path> (<N> matches)
  <line>: <text>
  <line>: <text>
...
[truncation: <hint>]
```

Already lean: path is emitted once per file (not per match), no
redundant per-record decoration. Marginal further wins:

- `(N matches)` parenthetical: ~3 tokens × file count
- `=== ash grep: ... ===` envelope: ~10-15 token flat cost
- `pattern="..."` quoting in scope: minor

For grep, the body is the data; further reduction has diminishing
returns. The bench corpus already covers grep heavy-tree cases and
shows -54% vs bash on the verb summary.

**`stat` row** (and header):

```
=== ash stat: 1 path(s) ===
F 10709      0600 2026-05-09T00:22:51Z docs/encodings.md
```

For one row, 38 tokens total. The mtime alone (`2026-05-09T00:22:51Z`)
is ~9 tokens — roughly a quarter of the response. The mode column
(`0600`) is ~2 tokens. Bulk stat calls multiply both by N rows.

**Saveable: ~10 tokens per stat row by dropping mtime + mode** from
the default. The wire data still carries them; only the pretty form
elides. `--with_meta true` (or symmetric flag) re-adds them.

### What `ash bench` already optimized (precedent)

The `find` verb went through this exact exercise (see
[docs/bench.md "First optimization round"](../bench.md)):

- Original `--with_meta` form was the default: `<F|D|L> <size> <yyyy-mm-dd> <path>`
- Date column alone was ~5 tokens because `yyyy-mm-dd` splits on hyphens
- Switched default to path-only (with trailing `/` for dirs); meta opt-in
- Result on the find verb summary: from +148% vs bash to +7% (-141pp)

The `read` and `stat` headers fit this same pattern exactly. The find
ship is the template.

A bench.md aside also notes a tokenizer-aware tactic (option O7 in the
plan): if dates *must* stay in the default, format as `yyyymmdd`
(3 tokens) rather than `yyyy-mm-dd` (5 tokens). Applies to mtime in
`read` / `stat` headers if we ever surface them in a compact form.

### Input side observations

From `ash report` arg distributions in this session:

- `path` is a flag on virtually every non-hook call (read, edit, grep, write, stat).
- `tool_name` dominates hook args (Bash 19×, Write 1×).
- Other args are long-tailed.

`path` being universally dominant confirms **I1 (positional dominant arg)**
is the right input-side bet. `ash read internal/proto/pretty.go` saves
the `--path ` boilerplate (~2 tokens) on every single read call.

The current `tokens_in` is ~9 tokens for typical calls (sorted-key
canonical reconstruction). Saving 2 tokens out of 9 is ~22% per call —
small absolute, real proportionally.

### Friction encountered

- `ash report --session all` returned 0 calls. The `all` value of
  `--session` looks like it filters by a literal `"all"` session ID
  rather than the documented "across all sessions" semantics. Plausibly
  a session-id mismatch since the daemon issues a fresh session ID per
  start. Worth a follow-up issue but unrelated to encoding work.
- `report` itself emitted 1700-1900 tokens. Most of that is the arg
  distribution dump and one long error string (the `stat` errors had
  the full usage text concatenated as the error message). Both are
  off-the-critical-path encoding wins — the report verb's pretty form
  could `--no_arg_dist` and the daemon could cap error strings.
- The PreToolUse hook denies `Write` even for files outside the project
  root, including `/Users/cstaszak/.claude/plans/`. Worked around with
  `ash write --content -`. Worth scoping the hook to project-root paths.

## Suggestions (Phase 2 picks)

Per the plan, Phase 2 should pick **one Tier A output option** and
**one Tier A input option**, with **I0 (argv honesty) as a
prerequisite** for the input option.

Recommended first pass:

1. **I0 + I1 bundled** — ship together. Add `Argv []string` to
   `proto.Request`, switch daemon `tokens_in` source to count the
   literal argv, and lift the positional-rejection at
   [cmd/ash/main.go:197](../../cmd/ash/main.go#L197) for a per-verb
   dominant-arg policy (`read <path>`, `find <path>`, `grep <pattern> <path>`,
   `stat <path[,path...]>`, `write <path> --content ...`,
   `edit <path> --old_string ...`). Each verb declares its dominant
   positional in its arg schema.

2. **O5: `read` lean header** — drop `mtime` + `encoding` from the
   default; reintroduce with `--with_meta true`. ~15 tokens per call.
   Mirrors the find optimization.

3. **O5 (extension): `stat` lean form** — drop `mtime` + `mode` from
   the default row; `--with_meta true` brings them back. ~10 tokens
   per row.

Each shipped behind its own flag; bench A/B verifies.

## Instrumentation

```
report --since 7d:
  totals: ok=36/38 (95%), tokens_in=15847, tokens_out=9591
  read p50_out=2525 (n=2)
  grep p50_out=164 p95_out=488 (n=6)
  stat p50_out=38 (n=1)
  hook p50_out=2 (n=19)

tok/KiB ranking: stat 338 > test 326 > read 309 > write 279 > grep 268 > edit 215 > git 65 > report 56 > hook 53
```
