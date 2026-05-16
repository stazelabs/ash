# Encoding results — measured token costs & shipped decisions

Companion to [docs/encodings.md](encodings.md) (the forward-looking plan).
This doc captures what we *measured* and *decided*, so future exploration
doesn't redo finished work.

## Token cost landscape (where the tokens go)

Per-verb `tokens_out / KiB` (40-call session, 11.6k tokens out):

| verb   | tok/KiB | dominant cost |
|--------|--------:|---|
| stat   | 338 | mtime (~9 tok) + mode (~2) per row |
| test   | 326 | structured failure rows + file:line |
| read   | 309 | body content (compressible only via projection) |
| write  | 279 | already lean |
| grep   | 268 | match text (data, not envelope) |
| edit   | 215 | already lean |
| git    |  65 | already lean |
| report |  56 | meta-call; arg-distribution dump heavy |
| hook   |  53 | already minimal |

The compressible surface is the *envelope* (headers, dividers, truncation
hints), not the body. `read` header alone carries ~15 tokens of
`mtime`+`encoding` chrome that's opt-in via `--with_meta`. `stat` rows
carry ~10 tokens of `mtime`+`mode` similarly suppressible.

## Non-ASCII substitution — abandoned

Scanned 67 Unicode ranges (~46,000 codepoints) for sub-1-token runes;
**none exist**. Every single-token Unicode rune (1313 found, including
549 common CJK ideographs and 129 Hangul syllables) is exactly 1 token —
same as a single ASCII char. Emojis are 2-3 tokens each.

The only wins are replacing **multi-token ASCII strings** with shorter
ASCII or a 1-token sentinel glyph. Short ASCII rewrites consistently
beat CJK substitutions in measurement (e.g. `errors_ascii` > `errors_cjk`
by +0.03pp aggregate; `not_found → 無` *regresses* by 5.9% on the
`err-not-found` corpus). Single-token decorative glyphs worth knowing:
`…`, `•`, `—`, `→`, `§`, `●`, `■`, `✔` (U+2714). The intuitive ones
(`✓`=2, `✗`=2, `⊘`=3, `▸`=2) are *worse* than their ASCII alternatives.

**Conclusion:** non-ASCII substitution is not an independent angle.
Future token-saving work focuses on structural shortening (metric keys,
headers, hint bodies).

## cl100k vs Claude calibration

The ledger's `tokens_out` uses `cl100k_base` as a local proxy. Cross-checked
against `count_tokens` for `claude-sonnet-4-5` on a 16-file corpus
(19,122 cl100k → 22,784 Claude tokens):

- **Claude is ~+19% more expensive in absolute terms** than cl100k_base.
- **Direction of every substitution agreed** across tokenizers on the
  live rule set (zero `✗` rows in the validation table).

Operational implication: ledger `tokens_out` *understates* real cost by
a fifth. Multiply by ~1.2 for budget conversations; trust ratios within
the ledger directly. `make validate-check` exists for this reason — it
parses `testdata/validate_results.md` for `✗` rows (sign disagreements)
and fails when a substitution regresses in Claude.

## Truncation hint — final shape

Two probes converged on the current production form:

**Glyph (ASH-120):** `…` (U+2026), +4 Claude tokens vs `TRUNCATED`/
`[truncation:`. Beat `✂`, `▶`, `▢`, `□`, `◊`, `●`, `›`, `空`, `切`,
`断`, `終` and others on `encexplore glyphsweep`. Same glyph already
used inline by `grep`/`bench`/`workspace` for long-line truncation —
one glyph, one meaning.

**Body shape (ASH-121) — `compact_keep_raise`, +13-14 Claude tokens additional:**

| call site | shape |
|---|---|
| below-cap | `hit <limit>. --flag1/--flag2/--flagN.` |
| hard-cap  | `hit hard cap. --flag1/--flag2 — --flagN cannot go higher.` |

Dropped: `narrow with ` verb (redundant with the flag list), the
` (max N)` parenthetical (agents can `ash help` for ceilings).

Kept: the raise-flag in the slash list, so agents know which flag lifts
the cap. Cheaper variants that lost it (`drop_raise_clause`,
`compact_no_raise`) saved 2-3 more tokens but risked agents *lowering*
`--max` when they meant to raise it — not worth the wire-cost saving.

**Combined per-truncated-call savings vs original prose: ~17-18 Claude
tokens.** Frequency-dependent; heavy `find`/`grep` sessions hitting
50+ truncation events see ~600+ Claude tokens shaved.

## Measurement tooling — `cmd/encexplore/`

Throwaway-grade Go binary, not part of the verb surface. Subcommands:

- `atlas` → `testdata/single_token_runes.txt` (1313 entries).
- `corpus` → `testdata/corpus/` (representative pretty responses).
- `measure` → `testdata/measure_results.md` (cl100k Δ per rule per corpus).
- `validate` → `testdata/validate_results.md` (count_tokens for Claude;
  needs `ANTHROPIC_API_KEY`).
- `glyphsweep` → probe candidate glyphs for a substitution rule.
- `truncbody` → probe candidate body shapes for a hint.

`make validate` regenerates the table; `make validate-check` gates it.
Run after non-trivial header/footer/error-string changes —
see [CLAUDE.md §Token cross-validation](../CLAUDE.md#token-cross-validation).

The tool can be deleted once the substitution layer no longer needs
re-probing; the testdata artifacts hold the durable value.

## Open follow-ups

- **Structural metric-key shortening** (`exec_us=` → `x=` → bare `x`,
  `regex_compile_us=` → `R`) shows +16-20% on metrics-heavy calls
  (`metrics-last` corpus: 124 cl100k tokens / 167 Claude tokens saved).
  Tracked in [docs/cli-tokens.md](cli-tokens.md).
- **`tokens_out_claude` column** — optional Claude-tokenized output
  column for spot-validation against the ledger. Tracked as ASH-99.
- **Dead error codes** — vocab inventory could cross-reference against
  ledger rows to surface never-fired codes.
  See [docs/vocab/design.md](vocab/design.md).
