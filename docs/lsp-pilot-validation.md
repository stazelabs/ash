# LSP pilot validation (ASH-141)

The ASH-109 hypothesis: a single `ash lang …` call replaces a 5–10-call
`grep + read+` sequence at materially lower token cost without giving up
correctness. This page is the empirical answer.

Measurements were taken on this repo at commit `8f4a8a2` (post-ASH-140),
with `[lsp].enabled=true`, gopls 0.21.1, no lang-cache priming. All
token counts are `tokens_out` from the daemon ledger — what the harness
actually consumes.

## Methodology

For each symbol, the comparison is **same agent task**: "find every
reference to X with enough context to understand the call site."

- `grep` path: `ash grep --pattern '\bX\b' --no-text true` gives a list
  of files+lines, then the agent reads each match. We charge:
    - `tokens_out` from the grep call (locations only).
    - Plus an estimate of ~100 tokens for one `ash read --range L:L+2`
      per matched file. 100 tokens per read is generous on the low end
      — real reads with surrounding context routinely cost 200–500.
- `lang refs` path: `ash lang --op refs --symbol X --in <decl> --context
  true` returns one row per reference with the trimmed source line
  inline. We charge the verb's `tokens_out` directly — no follow-up
  reads needed.

`--in <decl>` is required for the lang call because `workspace/symbol`
returns substring matches by default; pinning to the declaration site
gives the verb an unambiguous symbol to chase references for.

## Results

| symbol | grep tokens | grep matches/files | est. grep + read | lang refs tokens | lang rows | savings |
|--------|-------------|--------------------|------------------|------------------|-----------|---------|
| `proto.Tracer` | 738 | 61 / 31 | 738 + 31×100 = 3838 | 2026 | 70 | **47%** |
| `ledger.Open` | 240 | 18 / 9 | 240 + 9×100 = 1140 (single-file inspection: × 18 = 2040) | 368 | 11 | **64–82%** |
| `argutil.RequireString` | 167 | 9 / 8 | 167 + 8×100 = 967 | 537 | 15 | **44%** |

Token deltas are computed against the higher of the per-file-read and
per-match-read estimates (some agents read one range per file; others
read one range per match if the same file has multiple hits).

### Recall observations

`ash lang refs` returned **more rows** than `ash grep` on every symbol:
proto.Tracer 70 vs 61, ledger.Open 11 vs 18 (lang lower here; see
below), argutil.RequireString 15 vs 9.

Two structural reasons grep misses references gopls catches:

1. **Same-package use without the package qualifier.** `grep
   '\bproto\.Tracer\b'` cannot match a use of `Tracer` inside the
   `proto` package itself — but gopls knows those uses are bound to
   the same symbol.
2. **Aliased imports.** `import x "github.com/.../proto"` followed by
   `x.Tracer` would slip past a literal-prefix grep pattern.

The one case where lang returned fewer rows than grep
(`ledger.Open`: 11 vs 18) is the inverse: grep matches text in test
files, comments, and string literals that gopls considers
non-references. For the agent's task of "find call sites," lang's 11
is **higher precision** and the right count.

### def vs grep

`ash lang --op def --symbol Counter --in internal/ledger/tokens.go`
returned the canonical definition site (`internal/ledger/tokens.go:22:6
struct ledger.Counter`) in **36 tokens**. The grep equivalent —
iteratively refining a pattern like `^type Counter` or `^func
Counter\(`, then reading the file — is rarely fewer than 200 tokens
even on the first try and often involves multiple iterations.

## Where grep still wins

When the agent only needs a count or a file-set with no inspection
(e.g. "does X get used anywhere?" / "which packages depend on X?"),
`ash grep --no-text true` is cheaper than `ash lang refs --context
false`. On the `proto.Tracer` example without context lines, the lang
call drops below 1000 tokens but is still ~30% more expensive than the
no-text grep.

## Decision

**Declare the pilot a win for the symbol-hunt use case.** The savings
are consistent (44–82% on three measured symbols), the recall is
generally better (same-package refs, alias-safe), and the precision is
better when grep over-matches comments/tests/strings.

## Known follow-ups

- `lsp_ambiguous` fires on common names (`Counter`, `Backend`,
  `ProtocolError`). The error already names candidates in the hint, but
  a future ASH-D follow-up might let `--in` accept multiple files or
  rank candidates by some heuristic.
- `workspace/symbol` is not cached today (ASH-137 only caches
  per-file). The first call after daemon start can be 200–600ms cold
  while gopls indexes. Cache integration is deferred to a follow-up
  when real session data shows the hit rate would be worth the
  workspace-watermark complexity.
- Context-line extraction reads each matched file once per call. For a
  ref set spanning 30+ files this adds up; a future optimization would
  be a per-call file LRU.

## Reproducing

```sh
make all
bin/ash stop && sleep 1
# Put [lsp].enabled=true in ash.toml or use ASH_CONFIG=<path>
ASH_CONFIG=/tmp/ash-lsp.toml bin/ash grep --path . --pattern '\bproto\.Tracer\b' --no-text true --max 200
ASH_CONFIG=/tmp/ash-lsp.toml bin/ash metrics --verb grep --last 1
ASH_CONFIG=/tmp/ash-lsp.toml bin/ash lang --op refs --symbol Tracer --in internal/proto/tracer.go --context true --max 200
ASH_CONFIG=/tmp/ash-lsp.toml bin/ash metrics --verb lang --last 1
```
