# 2026-05-13 — encoding-substitution measurement

## Task

Revisit the deferred Tier C item in [docs/encodings.md](../encodings.md):
*"Tokenizer-aware micro-opts (favor sequences that merge to single cl100k
tokens, ASCII separators) — Real but small (1-5%). Only worth it if Phase 1
highlights a hot encoding choice."* The exploration question: can non-ASCII
Unicode characters (CJK, Hangul, emoji, math symbols, etc.) reduce the
per-call `tokens_out` measured by the ledger? Approved plan:
`~/.claude/plans/i-d-like-to-explore-lazy-gadget.md`.

## Verbs used

- `ash read` / `ash write` / `ash edit` for all in-repo file work.
- `ash help`, `ash find`, `ash grep`, `ash git --op log`, `ash git --op
  status`, `ash metrics`, `ash report` to populate the corpus.
- New throwaway tool `cmd/encexplore/` (atlas / corpus / measure / validate
  subcommands) — Go binary, reuses `internal/ledger.NewCounter` (cl100k_base)
  and `https://api.anthropic.com/v1/messages/count_tokens` (claude-sonnet-4-5).

## Friction

- The hook correctly forces ash for all file ops; minor friction was
  remembering that `ash edit` uses `--old/--new` not `--old_string/--new_string`
  (CLAUDE.md gotcha "ash help can lag code" did not bite this time — the help
  text is correct, I just typed the wrong flag name from memory).
- `ANTHROPIC_API_KEY` is not visible to the Claude Code Bash environment; the
  user had to run `validate` themselves. Worth flagging if more cross-tokenizer
  work happens — the script is in `cmd/encexplore/validate.go`.

## Atlas summary (Phase A)

Scanned 67 Unicode ranges for runes that tokenize to exactly 1 cl100k_base
token. Total: **1313 single-token runes** out of ~46,000 codepoints scanned.

Densest pools:

| Range | Single-token / total | Notes |
|---|---|---|
| ASCII printable | 95 / 95 | every printable ASCII is 1 token |
| Latin-1 Supplement | 77 / 96 | every common diacritic |
| Cyrillic | 58 / 256 | uppercase А Б В Г Д all 1 tok |
| CJK Unified Ideographs (common) | **549 / 20992** | 美 好 中 失 成 功 正 止 1 tok each |
| Hangul Syllables (common Korean) | **129 / 11184** | 성 공 1 tok |
| Hiragana / Katakana | 47 + 51 | half of each block |
| Arabic | 42 / 256 | |
| Thai | 41 / 128 | |
| Greek and Coptic | 27 / 144 | lowercase α β γ 1 tok; Δ uppercase is 2 |

Zero single-token runes in these blocks:

- **All emoji blocks** (Emoticons, Misc Symbols & Pictographs, Transport/Map,
  Supp Symbols & Pictographs): 0 / 1232 — every emoji is 2-3 tokens.
- **Private Use Area sample** (U+E000..U+E0FF): 0 / 256 — confirms that
  arbitrary unmerged bytes always tokenize to ≥2 tokens.
- **Mathematical Alphanumeric Symbols** (𝐀 𝟏 etc.): 0 / 1024 — surprising,
  these are 3 tokens each.
- **Supplemental Arrows A/B, Supp Math Operators, CJK Unified Ext A**: 0 —
  rare characters aren't in the BPE merge table.

Key takeaway: **every single-token non-ASCII rune is exactly 1 token, same
as a single ASCII character.** There is no Unicode block where chars
tokenize to *less than* 1. The only win is replacing *multi-token* ASCII
strings with a single-token rune.

## Corpus summary (Phase B)

Captured 16 representative pretty responses (16,925 B max, 17 B min):
read × 3 sizes, find × 2, grep × 2, git log+status, metrics, report,
help × 3, plus 2 error-path calls. Total cl100k tokens: **19,122**.
Total Claude (claude-sonnet-4-5) tokens: **22,784** — Claude is **+19%
more expensive in absolute terms** than the cl100k_base proxy on this
corpus. Direction of every substitution agreed across tokenizers; the
ledger is a faithful directional proxy but the absolute numbers in
`tokens_out` undercount what agents actually pay by roughly a fifth.

## Substitution results

Eight sub-sets tested; full per-corpus detail in
[testdata/measure_results.md](../../testdata/measure_results.md) and
[testdata/validate_results.md](../../testdata/validate_results.md).

### Aggregate (cl100k → Claude)

| sub-set | surface | cl Δ% | claude Δ% | verdict |
|---|---|---:|---:|---|
| `metrics_no_equals` | metrics keys | +0.68% | **+0.78%** | best aggregate |
| `combined_aggressive` | all surfaces | +0.49% | +0.65% | dominated by metrics |
| `metrics_short_ascii` | metrics keys | +0.22% | +0.38% | |
| `headers_compact` | headers/dividers | +0.18% | +0.21% | universal small win |
| `errors_ascii` | error codes | +0.04% | +0.04% | only on error calls |
| `truncation_compact` | truncation hint | +0.02% | n/a | net-negative on some files |
| `errors_cjk` | error codes | +0.01% | n/a | **worse than ASCII rewrite** |
| `status_cjk` | status enums | 0.00% | n/a | no win possible — already 1 tok |

### Per-call peaks

| corpus | sub-set | cl Δ% | claude Δ% |
|---|---|---:|---:|
| `metrics-last` | `metrics_no_equals` | **+16.40%** | **+20.19%** |
| `err-not-found` | `errors_ascii` | +17.65% | +13.79% |
| `metrics-last` | `metrics_short_ascii` | +5.69% | **+10.40%** |
| `err-bad-range` | `errors_ascii` | +11.76% | +18.52% |
| `help-find` | `headers_compact` | +1.49% | +1.21% |

## Legibility assessment for top candidates

| candidate | substance | legibility |
|---|---|---|
| `metrics_no_equals` (`exec_us=` → `x`) | drop `_us=` suffix and `=` separator, rely on column whitespace | **arbitrary** — requires the agent (and any downstream parser) to know that column 1 is exec, 2 is io, etc. Equivalent to a position-only contract. |
| `metrics_short_ascii` (`exec_us=` → `x=`) | drop `_us` suffix only | **intuitive** — single-letter ASCII labels are stable across tokenizers and self-documenting in a header row |
| `headers_compact` (`=== ash <verb>: ... ===` → `§<verb>: ...`) | replace ASCII fence with single sentinel glyph | **intuitive** — `§` reads as "section start" |
| `errors_ascii` (`path_denied` → `denied`, `no such file` → `missing`) | shorter ASCII synonyms | **intuitive** — straightforward English rewrite |
| `errors_cjk` (`not_found` → `無`) | CJK opaque sentinel | **arbitrary** — requires legend; **and actively loses tokens** on `err-not-found` (-5.88% — i.e. *worse* than original ASCII) |
| `status_cjk` (`pass` → 好, etc.) | CJK opaque sentinel | **arbitrary**; zero token benefit because originals are already 1 token |

## Suggestions (Linear-ticket-ready findings)

### Recommendation: **abandon** non-ASCII substitution as an independent angle

The exploration confirms the deferred Tier C designation. After scanning 67
Unicode ranges (1313 single-token runes including 549 common CJK ideographs,
129 Hangul syllables, full Hiragana/Katakana), and measuring against 16
real pretty responses:

- **No single-token Unicode rune tokenizes to less than 1 token** — same as
  short ASCII. The hypothesis that exotic characters might offer sub-token
  encoding is falsified.
- **The only token wins are on multi-token ASCII strings** (`build_failed`,
  `path_denied`, `range_out_of_bounds`, `exec_us=`). For these, **short
  ASCII rewrites are at least as good and usually better** than Unicode
  substitution (e.g. `errors_ascii` beats `errors_cjk` by +0.03 percentage
  points aggregate; on `err-not-found` the CJK variant *regresses* by 5.9%).
- **Emojis are 2-3 tokens each in cl100k_base** (and remain so in Claude per
  spot-checks). Substituting `✓` for `pass` would cost +1 token per status.
- **The intuitive symbol glyphs we'd reach for are worse than the ASCII
  originals**: `✓` = 2 tok, `✗` = 2 tok, `⊘` = 3 tok, `▸` = 2 tok. Only
  `✔` (U+2714), `•`, `—`, `→`, `§`, `●`, `■` are 1 token.

### Recommendation: **ship** the structural metric-key shortening

`metrics_no_equals` shows +16-20% on metrics-heavy verbs (cl100k +16.40%,
Claude +20.19% on the `metrics-last` call). This is **not** a non-ASCII
substitution finding — it's confirmation of the structural reform already
proposed in [docs/cli-tokens.md §1.4](../cli-tokens.md) ("Short metric field
names, proto v2 negotiation"). The numbers here strengthen the case for
that ticket: the per-call wins are bigger in Claude than the cl100k-based
estimate suggests.

Concrete deltas this exploration adds to that existing ticket:

- `exec_us=` (3 cl100k toks) → `x=` (2) → `x` (1): -2 per occurrence.
- `regex_compile_us=` (4 toks) → `R=` (2) → `R` (1): -3 per occurrence.
- `metrics --last 20` aggregate savings: 124 cl100k tokens (16.4%), 167
  Claude tokens (20.2%) — ~1 standard short tool response avoided per call.

### Recommendation: **ship** the trivial header tightening

`headers_compact` (drop trailing ` ===`, replace `=== ash <verb>` with
`§<verb>`) saves a universal +0.2-1.5% per call. Tiny, but cheap and
applies to every verb. Worth bundling with the metric-key ticket.

### Notable side finding: the ledger undercounts by ~19%

The cross-validation showed Claude (claude-sonnet-4-5) tokenizes our corpus
at 22,784 tokens vs cl100k's 19,122 — Claude is **+19% more expensive** in
absolute terms. The ledger's `tokens_out` column is therefore a
**conservative** estimate of what agents actually pay. Directional
comparisons remain honest, but if any future analysis quotes absolute
token-budget figures (e.g. "this session cost N tokens"), they should be
multiplied by ~1.2 to approximate Claude reality, or the ledger should be
extended with a `tokens_out_claude` column for spot-validation.

Worth a Linear ticket on its own — possibly "ASH-NN: document cl100k → Claude
absolute-token gap; consider opt-in Claude tokenizer column on the ledger."

## Instrumentation

Atlas: `bin/encexplore atlas` → [testdata/single_token_runes.txt](../../testdata/single_token_runes.txt)
(48 KB, 1313 entries).

Corpus: `bin/encexplore corpus` → [testdata/corpus/](../../testdata/corpus/)
(16 files, manifest).

Measure: `bin/encexplore measure` →
[testdata/measure_results.md](../../testdata/measure_results.md).

Validate: `bin/encexplore validate` (needs `ANTHROPIC_API_KEY`) →
[testdata/validate_results.md](../../testdata/validate_results.md).

Throwaway nature: the `cmd/encexplore/` tree (~17 KB Go across 7 files) is
not part of the verb surface and depends only on existing imports. Can be
deleted after the recommendations above are filed as tickets; the session
note + the testdata artifacts hold all the value going forward.

## Out-of-scope items surfaced during the exploration

- **The truncation-hint sentinel idea backfires.** `[truncation: ...]` is
  cheap; replacing `truncated` with `✂` regressed on `metrics-last` and
  `report-session`. Stick with prose for now (or structured `{trunc:1,
  limit:N, max:N}` as already specced in cli-tokens.md §1.6).
- **Mathematical Alphanumeric Symbols (𝐀 𝟏) are NOT free in cl100k.** Each
  is 3 tokens. If any future work considers using them as decorative
  highlights, this is a no-op-or-loss.
- **Variation Selectors (U+FE00..FE0F)** are 1/16 single-token — interesting
  for steganographic-shaped optimizations but no obvious application here.
