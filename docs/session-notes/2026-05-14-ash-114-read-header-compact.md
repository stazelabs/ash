# ASH-114 — collapse `=== <path> [<size>B, <lines>L] ===` to `§<path> <size>B <lines>L`

## Task

Resolve ASH-114: drop the fence + bracket framing on the `ash read` file-header
that ASH-100 deliberately skipped. ASH-100 collapsed `=== ash <verb>: … ===`
to `§<verb>: …` for every verb section; the per-file frame emitted by
[internal/verbs/read/read.go](../../internal/verbs/read/read.go) was the one
header it left alone because it does not start with `=== ash`.

## Design choice

| form | basic | range | range+TRUNC | base64 |
|------|------:|------:|------:|------:|
| `=== <path> [<size>B, <lines>L, …] ===` (pre-ASH-114) | 13 | 19 | 23 | 18 |
| `§<path> [<size>B, <lines>L, …]`                       | 12 | 18 | 22 | 17 |
| `§<path> [<size>B,<lines>L,…]`                         | 11 | — | — | — |
| `§<path> <size>B <lines>L [<key=value>]*` (chosen)     | 10 | 15 | 18 | 14 |

Saves ~3-5 cl100k tokens per call vs the old form. The aggressive form keeps
named keys (`encoding=base64`, `range=10:20`, `mtime=…`) so the agent can still
parse positional fields when only some are present, and the `B`/`L` suffixes
keep the size/line numbers self-typing. Brackets and commas are pure structure
the agent does not need.

**Glyph: reuse `§`.** Verb sections end in `:`; file frames end with `B[L]`.
One sentinel = one classifier path in [internal/vocab/literals.go](../../internal/vocab/literals.go) — no change needed.

## What changed

- **[internal/verbs/read/read.go](../../internal/verbs/read/read.go)** — `PrettyResponse` now emits `§<path> <size>B[ <lines>L][ encoding=…][ range=…][ TRUNCATED]` (lean) / `§<path> <size>B <lines>L <encoding> mtime=<t>` (--meta=true). Docstring updated with the new format + history line.
- **[internal/verbs/read/read_test.go](../../internal/verbs/read/read_test.go)** — two `HasPrefix` goldens updated; one `Contains(", 3L")` substring assertion updated to `Contains(" 3L")` (the separator changed comma → space).
- **[cmd/encexplore/subs.go](../../cmd/encexplore/subs.go)** — new `read_header_compact` rule. Measures the file-frame leading-fence rewrite (`=== ` → `§`), the size/lines separator rewrite (`B, ` → `B `), and the close-bracket+trailing-fence drop (`L] ===` → `L`).
- **[testdata/corpus/read-*.txt](../../testdata/corpus/)** + all other corpus snapshots — regenerated via `bin/encexplore corpus`. read-small/medium/large now show `§<path> <size>B <lines>L`.
- **[docs/vocab/inventory.{md,json}](../../docs/vocab/inventory.md)** — regenerated via `make vocab`. New 1-token `§` entry appears (read.go writes the sentinel as a bare literal, so the classifier indexes it on its own); the old `=== ` literals from read.go are gone.
- **[docs/bench.md](../../docs/bench.md)** — line 218 referenced `=== <path> [<size>B] ===` as the irreducible structural cost. Updated to `§<path> <size>B` (post-ASH-114) with the revised ~6-token framing floor.
- **[docs/path-prefixes.md](../../docs/path-prefixes.md)** — one example line updated to the new shape.

## Verbs used

`ash grep`, `ash read`, `ash edit` (--old/--new in shell-quoted form for the
multi-line builder block, plus a botched --range edit I had to revert via
git), `ash test`, `ash find`, `ash help`, `ash stop`, `ash git --op diff`.
`bin/encexplore probe` for token-count spot checks; `bin/encexplore measure`
for corpus-wide deltas; `bin/encexplore corpus` to regenerate snapshots.

## Friction

- **`ash edit --range` is unsafe for multi-line replacement-block insertion.** First attempt at updating the `PrettyResponse` builder body used `--range 234:298 --content -`. The new content went in, but the function header (`func PrettyResponse(…) string {`) and the closing braces of the function ended up *outside* the replacement window — the file shrank from 9128B to 6995B with a half-cut function. I recovered with `git checkout internal/verbs/read/read.go` and redid the edit with `--old/--new` against the exact text blocks. **Suggestion:** ash edit could refuse `--range` replacements that would leave unbalanced braces in the resulting Go source, or at least surface a brace-balance warning. Even simpler: when `--range` is used on a `.go` file, optionally run `gofmt -e` on the result and report drift.
- **`ash edit --range 109:122 --content -`** silently *removed* lines 109:122 from cmd/encexplore/subs.go without inserting the replacement content — even though `--content -` was piped. Verified by re-reading the file post-edit. The success message said "lines 109:122 replaced" but only the deletion landed. I recovered by re-adding the block with `--old/--new`. Need to write this up as its own ticket — `ash edit --range … --content -` should insert exactly what stdin produces, or fail loudly.

## Workarounds

None beyond reverting via git and switching to `--old/--new`.

## Verification

- `bin/ash test` — 36/36 packages pass.
- `make vocab-check` — `vocab check: ok` (after `make vocab` regen).
- `bin/encexplore probe` on raw read-small snapshot: 792 toks (pre) → 789 toks (post) = -3 toks per call. Matches the table above for the basic header.
- `bin/encexplore measure` aggregate (full corpus): read_header_compact is now a no-op (corpus already shipped in the compact form), confirming the change landed.
- Smoke: `bin/ash read --path go.mod` emits `§go.mod 1967B 47L\n<body>`.

## Suggestions

- File the two `ash edit` issues above. Both are likely root-caused in the
  range-mode + stdin code path.
- Consider whether `b.WriteString("§")` (a bare 1-token sentinel) should be
  combined with the path write into a single `Fprintf` so the vocab inventory
  captures the header *shape* (`§%s `) instead of just the sentinel glyph.
  Trade-off: prettier inventory entries vs. less-readable builder code.
