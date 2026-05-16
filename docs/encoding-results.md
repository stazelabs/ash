# Encoding results — measured token costs & shipped decisions

This doc captures the constraints, options considered, and outcomes of
the token-efficiency exploration so future work doesn't redo finished
ground. Live status for ongoing token-saving items is in
[cli-tokens.md](cli-tokens.md).

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

## Constraints that shaped the option set

Four invariants kept the option search honest:

1. **One result per line (greppable).** Each record renders on its own
   line. Rules out tree-form / path-prefix-grouped output.
2. **`tokens_in` honesty.** Aliases, positionals, or interning must
   tokenize against what the agent literally typed, not the
   post-expansion canonical form.
3. **Headers stay on by default.** The `§<verb>: …` header is part of
   the contract. Compact mode is opt-in only.
4. **Wire parity.** Anything dropped from the pretty form must still
   ride the wire (JSON / msgpack). Only the agent-facing rendering
   changes.

## Options considered

The original Phase-2 option menu, with current status. ✓ shipped,
✗ ruled out, ↻ deferred / tracked elsewhere.

### Output-side

| ID | Option | Status | Notes |
|---|---|---|---|
| O1 | `--fields a,b,c` projection | ↻ | Generalizes `--with_meta`/`--stat`/`--files_only`; no live consumer yet |
| O2 | `--compact true` global | ↻ | Headers stay on by default (constraint #3); opt-in mode unbuilt |
| O3 | Path-prefix compression (tree form) | ✗ | Ruled out by constraint #1 |
| O4 | Sharpen grep lean form | ↻ | Marginal; not prioritized |
| O5 | Per-record column elision | ✓ | `find --with_meta=false` and `read` header trim shipped (ASH-114) |
| O6 | `--format` extensions (`tsv`, `lean`, `fields:…`) | ↻ | Subsumes O1+O2; deferred |
| O7 | Tokenizer-aware micro-opts | ✓ | Header compaction + truncation glyphs shipped (ASH-100, ASH-120, ASH-121) |
| O8 | `--max_tokens N` budget knob | ↻ | Speculative; no ledger-driven motivation |

### Input-side

| ID | Option | Status | Notes |
|---|---|---|---|
| I0 | Argv-honesty rework (literal argv on `proto.Request`) | ↻ | Prerequisite for I1-I4; not started |
| I1 | Positional dominant arg | ✓ | `ash git log` etc. shipped; broader rollout partial |
| I2 | Argv path interning (`@root`, `@cwd`) | ↻ | Deferred until I0 lands |
| I3 | Short flag aliases (`-p`, `-g`) | ✗ | Conflict risk; rolled into Tier 2 rename pass |
| I4 | Short verb aliases (`f`/`g`/`r`/…) | ✗ | Surface pollution; ruled out |
| I5 | Stop sorting keys in `PrettyRequest` | ✗ | Negligible win, determinism loss |
| I6 | Drop `ash` prefix in `PrettyRequest` | ✗ | Agents type it; removing under-reports cost |

### Cross-cutting

| ID | Option | Status | Notes |
|---|---|---|---|
| X1 | `--encoding` global / encoding registry | ↻ | Architectural home for O1+O2+O4+O6; defer until two Tier-A wins land |
| X2 | Flip defaults to compact | ✗ | Ruled out by constraint #3 |

Items overlap with [cli-tokens.md](cli-tokens.md) (Tier 1-4); that doc
is the live status surface for ongoing token-saving work.

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
