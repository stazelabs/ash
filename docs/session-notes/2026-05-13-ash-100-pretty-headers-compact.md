# ASH-100 — collapse `=== ash <verb>: … ===` to `§<verb>: …`

## Task

Resolve ASH-100: replace the leading `=== ash ` and trailing ` ===` in every pretty header with a single `§` (U+00A7) sentinel. Measured payoff: ~3 tokens per call (cl100k) across every verb response.

## What changed

- **26 verb header literals** in [internal/verbs/](internal/verbs/) — every `Fprintf`/`WriteString` site that started with `=== ash <verb>:`. The builder pattern (find/grep/git/metrics/stat: prefix in one call, ` ===\n` trailer in another) was handled per-file.
- **Vocab classifier + normalizer** in [internal/vocab/literals.go](internal/vocab/literals.go) — `classifyLiteral` now buckets on `strings.HasPrefix(s, "§")` instead of `Contains(s, "=== ash")`; `normalizeHeader` no longer trims trailing `===`.
- **8 test expectations** in edit/find/write/stat tests.
- **11 corpus snapshots** in [testdata/corpus/](testdata/corpus/) that captured the old header form.
- **4 doc files** ([encodings.md](docs/encodings.md), [bench.md](docs/bench.md), [path-prefixes.md](docs/path-prefixes.md), [report.md](docs/report.md)) where the old header appeared in prose or example output.
- **Vocab inventory** regenerated via `make vocab`.

## Verbs used

`ash grep` (locate every site, 6+ scans), `ash edit` (~50 in-place replacements, both `--old/--new` and one `--range` line-deletion), `ash read`, `ash test`, `ash find`, `ash help`, `ash stop`, `ash git --op log`.

## Friction

- **`make vocab` silently runs a stale binary.** The Makefile rule for `bin/ashvocab` had no source-file prereqs, so changes to [internal/vocab/literals.go](internal/vocab/literals.go) didn't trigger a rebuild. After the first `make vocab` the headers section in the inventory shrank to 1 entry (the legacy `[ash WARNING …]` footer) — the classifier change had been compiled out. Discovered by inspection; fixed by adding `bin/ashvocab: $(shell find cmd/ashvocab internal -name '*.go')` to match the `bin/ash` / `bin/ashd` pattern. Worth checking the rest of the Makefile for similar gaps. **Suggestion:** lint for `bin/%:` rules without prereqs, or just rebuild unconditionally.
- **`ash edit` shell-quoting on multi-line `--old`.** A multi-line `--old "$(cat <<EOF…EOF)"` containing both backticks and a literal `}` triggered a zsh parse error near the brace. Worked around by switching to `--range` line replacement piped via heredoc-stdin. **Suggestion:** ash edit could accept `--old -` to read the search text from stdin, mirroring `--new -` (already supported for `--patch -`).

## Workarounds

None beyond the above. The hook-redirect path (`harness Read → ash read`, `harness Edit → ash edit`) handled itself once I stopped reaching for harness tools out of habit.

## Verification

- `bin/ash test` — 36/36 packages pass (after fixing the `stat` test's substring assertion, which was `Contains(out, "ash stat: 1 path(s)")` — the old form was substring-permissive, so my initial run failed loudly and pointed straight at the missing test update).
- `make vocab-check` — `vocab check: ok` after rebuilding ashvocab.
- Smoke: `bin/ash help --verb find` now emits `§help: 1 verb(s)`; `bin/ash stop` emits `§stop: stopped (51ms)`.

## Instrumentation

Inventory delta (cl100k tokens, headers section):

| header shape | before | after | Δ |
| -- | --: | --: | --: |
| `§find:` (was `=== ash find:`) | 4 | 3 | -1 |
| `§grep:` | 4 | 3 | -1 |
| `§git status:` | 5 | 4 | -1 |
| `§bench --diff-micro: vs baseline` | 10 | 9 | -1 |
| `§help:` | 4 | 3 | -1 |
| (all 28 header shapes — same pattern) | — | — | — |

The leading-prefix savings is 1 token per header (4-tok `=== ash <verb>` → 2-tok `§<verb>` for short verbs; the formatter also drops the 2-tok trailing ` ===` that lived in `WriteString(" ===\n")` calls, which the inventory doesn't index as a separate entry but encexplore measured at +0.18% cl100k / +0.21% Claude aggregate across the 16-case corpus.

The encexplore `headers_compact` substitution rule in [cmd/encexplore/subs.go](cmd/encexplore/subs.go) stays. After this change it's a no-op against the new corpus — which is the point. The rule documents what's been applied.

## Suggestions

- Makefile: rebuild rules for every `bin/*` should declare their source-file prereqs. The `bin/ashvocab` gap was load-bearing here and easy to repeat.
- `ash edit`: accept `--old -` (stdin) for multi-line patterns. Heredoc + `--range` works, but you have to re-read the file to know the line numbers.
- Consider whether [internal/verbs/read/read.go](internal/verbs/read/read.go)'s `=== <path> [<size>B, <lines>L] ===` header deserves the same treatment under a follow-up ticket. ASH-100 scoped specifically to `=== ash <verb>:`, so read's header was deliberately untouched.
