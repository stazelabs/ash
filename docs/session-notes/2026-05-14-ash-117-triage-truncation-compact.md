# ASH-117 — triage `grep-common / combined_aggressive` ✗ row in validate_results.md

## Task

The first run of `make validate` after ASH-115 wired up the cl100k↔Claude
cross-check produced exactly one sign-disagreement row in
[testdata/validate_results.md](../../testdata/validate_results.md):

```
| grep-common | combined_aggressive | +3 | +0.06% | +0 | +0.00% | ✗ |
```

cl100k said the combined substitution stack saved 3 tokens on the grep-common
corpus; the Anthropic `count_tokens` endpoint said it saved 0. ASH-117 was
filed to triage whether this was real signal (a substitution that helps the
proxy but not Claude), endpoint variance, or a fixture artifact — and then to
either drop the offending rule or document the row as known-borderline.
`make validate-check` (added in commit `756851a`) was failing on this single
row.

## Investigation

**Stability check (3 runs).** Ran `make validate` three times. The grep-common
row was bit-for-bit identical across all three: `cl Δ=+3, claude Δ=+0`. This
ruled out endpoint variance — the disagreement is deterministic.

**Sub-set isolation.** `combined_aggressive` concatenates *every* set in
[cmd/encexplore/subs.go](../../cmd/encexplore/subs.go), including four that
are not in the default `--sets` flag list: `errors_cjk`, `status_cjk`,
`read_header_compact`, `truncation_compact`. Re-ran validate with
`--sets errors_cjk,status_cjk,read_header_compact,truncation_compact` against
the full corpus. Result on grep-common:

```
errors_cjk          cl Δ=+0  claude Δ=+0
status_cjk          cl Δ=+0  claude Δ=+0
read_header_compact cl Δ=+0  claude Δ=+0
truncation_compact  cl Δ=+3  claude Δ=+0   ← culprit
```

**Per-rule isolation.** `truncation_compact` defined three rules:
`TRUNCATED→✂`, `[truncation:→[✂`, `truncated→✂`. Wrote a one-off probe
([cmd/probetrunc](#) — since deleted) that applies each rule independently
against the grep-common body and counts both cl100k and Claude tokens:

```
trunc_uppercase  occ=1  cl Δ=+1   claude Δ=+0
trunc_bracket    occ=1  cl Δ=+2   claude Δ=+0
trunc_lowercase  occ=0  cl Δ=+0   claude Δ=+0   (does not fire on this corpus)
```

Both firing rules save cl100k tokens but zero Claude tokens. The `✂` glyph
(U+2702) is cheaper than `TRUNCATED` / `[truncation:` in cl100k but Claude
tokenizes it at parity. The earlier [encoding measurement session note](2026-05-13-encoding-substitution-measurement.md)
had already flagged `truncation_compact` as "net-negative on some files" on
the cl100k side (read-medium and report-session show -1 each because `✂` is
3 UTF-8 bytes and inflates other context); the cross-check now confirms there
is no Claude-side payoff even on the corpora where cl100k claims a win.

## Decision

Per ticket scope option (a): **drop `truncation_compact` from `subSets`**.
The rule has no real Claude-side payoff on any corpus, and on cl100k it
ranges from marginal (+3 on grep-common, +3 on git-log) to slightly negative
(-1 on read-medium, -1 on report-session). The substitution does not earn
its place.

Option (b) — documenting the row as known-borderline in
[docs/vocab/inventory.md](../../docs/vocab/inventory.md) — would have kept a
✗ in the checked-in artifact and forced `make validate-check` to learn a
suppression mechanism. Not worth the complexity for a rule the data says
isn't pulling its weight.

## What changed

- **[cmd/encexplore/subs.go](../../cmd/encexplore/subs.go)** — deleted the
  `truncation_compact` sub-set, replaced with a short comment block
  recording the drop and the reason. `combinedSubs()` automatically excludes
  it because it iterates `subSets`.
- **[testdata/measure_results.md](../../testdata/measure_results.md)** —
  regenerated via `bin/encexplore measure`. Drops the `truncation_compact`
  rows (was 144 rows, now 128) and the `combined_aggressive` aggregate now
  reflects the slimmer rule set.
- **[testdata/validate_results.md](../../testdata/validate_results.md)** —
  regenerated via `make validate`. The grep-common `combined_aggressive`
  row is now `cl Δ=+0, claude Δ=+0, agreement=—`. Zero ✗ rows in the file.

## Verbs used

`ash read`, `ash grep`, `ash find`, `ash edit` (--old/--new with stdin for
the old block, inline `$'…'` for the new block), `ash write` (probe + session
note), `ash test`. `make validate` ×4 (3 stability runs + 1 final regen),
`make validate-check`, `make vocab-check`. Built a throwaway
`cmd/probetrunc` to call the Anthropic count_tokens endpoint with each
single-rule rewrite (couldn't be done from outside the repo because
`internal/ledger.NewCounter` is internal).

## Friction

- **`ash edit` cannot pipe both `--old` and `--new` from stdin in one call.**
  Hits `ash: only one arg can read from stdin (-); got both --old and --new`.
  For multi-line old AND multi-line new content, the workaround is to inline
  one side via `$'…'` shell-quoting and pipe the other. Worked here but felt
  brittle — the `$'…'` form does not preserve hard tabs the way a heredoc
  would, and I had to convert the leading-tab line prefixes manually.
  **Suggestion:** accept `--old @file` / `--new @file` (read from a path),
  or accept two heredoc-like separators on stdin (`<<OLD … OLD<<NEW … NEW`).
  Filed mentally as a follow-up worth a ticket.
- **`internal/ledger` is internal, so the probe had to live inside the repo.**
  A one-shot diagnostic program that wanted to use the same tokenizer the
  validate harness uses couldn't sit outside `cmd/`. Cost a couple of
  minutes of `go mod replace` experimentation before I gave up and dropped
  the probe at `cmd/probetrunc/`. Not really a friction with `ash` — but
  worth noting for future encexplore-adjacent work: write throwaway probes
  inside `cmd/` from the start.

## Verification

- `make validate` — 80 rows; zero `✗` markers.
- `make validate-check` — `validate-check: ok`.
- `bin/ash test` — 36/36 packages pass.
- `make vocab-check` — `vocab check: ok` (no inventory drift; `✂` was not
  used anywhere in live ash output, so it never made it into the vocab).
- `make` build green; `bin/ashvocab`, `bin/encexplore`, `bin/ash`,
  `bin/ashd` all compile clean after the deletion.

## Suggestions

- The `truncation_compact` glyph search is not closed — `✂` just happens to
  not save Claude tokens. A future pass could probe other single-codepoint
  glyphs (`…`, `▢`, `□`, single CJK glyphs) against `count_tokens` to find
  one Claude tokenizes cheaply. Out of scope for ASH-117.
- The `[truncation:` prefix appears verbatim in actual ash output (find /
  grep / read truncation hints). Even though substitution doesn't help on
  Claude, the prefix string itself is a known cost line in the vocab
  inventory — worth a separate look at whether the hint *body* can be
  shortened without losing the verb/limit context.
