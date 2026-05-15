# ASH-120 — encexplore glyph search for a Claude-cheap truncation-hint sentinel

## Task

ASH-117 dropped `truncation_compact` after the original `✂` (U+2702)
proved to save cl100k tokens but zero Claude tokens (cl Δ=+3, claude Δ=+0
on `grep-common`). The underlying question — *is there a single-codepoint
glyph Claude tokenizes cheaper than the multi-byte prose?* — was filed as
ASH-120 backlog. This session ran the probe.

## Approach

Added `encexplore glyphsweep` ([cmd/encexplore/glyphsweep.go](../../cmd/encexplore/glyphsweep.go))
— a subcommand that applies `TRUNCATED→<g>` and `[truncation:→[<g>` to a
corpus, then reports cl100k and Claude deltas per candidate against the
Anthropic `count_tokens` endpoint (`claude-sonnet-4-5`). Glyphs probed:

```
✂ … ▢ □ ◊ ▶ › ◌ ◆ ● ◯ ◎ 空 絶 斬 切 終 断
```

Run against both corpora that contain truncation hints
(`grep-common.txt`, `git-log.txt` — one occurrence of each rule on each).

## Results

| glyph | codepoint | cl Δ | claude Δ | notes |
|---|---|---:|---:|---|
| `…` | U+2026 | +6 | **+4** | already in-line truncation marker in grep/bench/workspace |
| `›` | U+203A | +5 | +3 | unused |
| `●` | U+25CF | +5 | +3 | unused |
| `□` | U+25A1 | +3 | +3 | unused; cleanest semantic match |
| `空` `切` `断` `終` | — | +2..+4 | +2 | CJK alternatives |
| `✂` `▢` `◊` `▶` `◌` `◆` `◯` `◎` `絶` `斬` | — | +2..+3 | **+0** | sign disagreement (✗) |

Deltas were identical on both corpora — the choice generalizes.

## Decision

Re-introduced `truncation_compact` with `…` (U+2026).

- **+4 Claude tokens** per truncated call — the best result in the sweep.
- **Already the canonical "truncated" glyph** in ash output: `grep`'s
  long-line cap, `bench`'s output cap, and `workspace`'s path-trim all
  append `…`. Reusing it for the verb-level truncation sentinel keeps the
  agent reading one glyph for one meaning rather than two distinct
  truncation cues (`✂` for verb-level, `…` for inline).
- Carried `[truncation: → […` for the detail-prefix rule. The original
  ASH-117 lowercase `truncated→…` rule had zero occurrences on every
  corpus — dropped, since a non-firing rule earns no place in the set.

## What changed

- **[cmd/encexplore/glyphsweep.go](../../cmd/encexplore/glyphsweep.go)** — new
  subcommand. Kept (not deleted) so future glyph searches can reuse it;
  encexplore is already a long-lived throwaway-grade tool, and this is a
  natural extension. Wired into [cmd/encexplore/main.go](../../cmd/encexplore/main.go).
- **[cmd/encexplore/subs.go](../../cmd/encexplore/subs.go)** — re-introduced
  `truncation_compact` with `TRUNCATED→…` and `[truncation:→[…`. Replaced
  the "dropped" comment block with a block referencing ASH-120.
- **[cmd/encexplore/validate.go](../../cmd/encexplore/validate.go)** — added
  `truncation_compact` to the default `--sets` so `validate-check` exercises
  it explicitly, not just transitively through `combined_aggressive`.
- **[testdata/validate_results.md](../../testdata/validate_results.md)** —
  regenerated. New `truncation_compact` rows: `grep-common cl Δ=+6 / claude
  Δ=+4 ✓`, `git-log cl Δ=+6 / claude Δ=+4 ✓`, all other corpora `—`. The
  `combined_aggressive` aggregate now saves 25 cl100k tokens and 31 Claude
  tokens (up from 13/23). Zero `✗` rows.
- **[testdata/measure_results.md](../../testdata/measure_results.md)** —
  regenerated. 144 rows (was 128 after the ASH-117 drop).

## Verbs used

`ash read`, `ash grep`, `ash find`, `ash edit` (`--old @stdin`/`--old`
inline + `--new -` heredoc), `ash write` (subcommand + session note),
`ash test`. `make validate` (one run, ~10s after API caching); `make
validate-check`, `make vocab-check`, `make schema-check` — all green.

## Friction

- **`grep` / `tail` are hook-denied even inside `make validate` pipelines.**
  Tried piping `make validate 2>&1 | grep …` and `make validate 2>&1 | tail
  …` to filter long output — both denied at the bash level by the
  PreToolUse hook. The fix was to redirect to a tmpfile and read it via
  `ash grep`. Reasonable behavior given the hook is path-based, but worth
  noting that bash pipeline conveniences are dead in this repo even when
  the upstream command is `make`. Workaround was fine; no design change
  warranted.
- **`ash test` doesn't surface a useful pretty summary line when output is
  truncated by the pretty header's own line-cap** (the `[ash bi=… bo=…]`
  line appears mid-string in the captured stdout). Not a bug — just a
  visual artifact when the test pkg list is huge. Filed mentally; not
  worth a ticket.

## Verification

- `make validate` — 96 rows; zero `✗` markers.
- `make validate-check` — `validate-check: ok`.
- `make vocab-check` — `vocab check: ok` (no live-output strings touched).
- `make schema-check` — `schema check: ok`.
- `bin/ash test` — 43/43 packages pass.
- Build green for all five binaries: `bin/ash`, `bin/ashd`, `bin/ashmcp`,
  `bin/ashvocab`, `bin/ashschema`, `bin/encexplore`.

## Suggestions

- The `…` glyph savings are unlocked but not *applied* — `truncation_compact`
  is a measurement-stack candidate, not a live-output rewrite. If a future
  pass decides to actually emit `…` instead of `TRUNCATED` / `[truncation:`
  in `internal/verbs/*` pretty renderers, expect 4 Claude tokens saved per
  truncated call. Frequency depends on how often agents hit truncation
  limits — heavy `find` / `grep` sessions could see 50+ such hints, so the
  unit savings compound.
- The glyph sweep tool is general. Future "find a Claude-cheap glyph for
  X" questions can reuse `encexplore glyphsweep --files <corpus> --glyphs
  <list>` with a custom rule. The current rules are hardcoded for the
  `truncation_compact` shape — if reused for other rules, the substitution
  pattern would need to become a CLI flag.
