# 2026-05-13 — vocab design (ASH-101)

> **Status note (post-ASH-102):** This is a frozen design record. The spike artifacts referenced below — `cmd/ashvocab/errors.go` and `testdata/vocab_spike.md` — were deleted as planned when ASH-102 superseded them; their content lives on in [internal/vocab/](../../internal/vocab/) and [docs/vocab/inventory.md](../vocab/inventory.md). Inline links to those paths are historical references and will 404.

## Task

Explore + design a generated inventory of ash's stable agent-facing
vocabulary. Decide extraction strategy, artifact format, and tooling
shape; build a single-category spike (error codes) to validate the
choice; produce a ticket-ready plan for ASH-102 (implementation).

## Verbs used

- `ash grep` / `ash find` / `ash read` to map the error-code surface and
  read the referenced session note.
- `ash write` for all in-repo file work (new spike code + this note).
- `go build` / `go vet` for the spike toolchain — not yet an ash verb.
- New throwaway tool `cmd/ashvocab/` (subcommand `errors`) — Go binary
  reusing `internal/ledger.NewCounter` (cl100k_base) and `go/parser` +
  `go/ast` for extraction.

## What the spike found

`bin/ashvocab errors` walked `internal/verbs/`, `internal/runner/`,
`internal/jail/` and emitted [testdata/vocab_spike.md](../../testdata/vocab_spike.md):

- **51 distinct error codes across 213 source sites** — substantially more
  than the ticket's "~15" estimate. The vocabulary has drifted further
  than visual inspection suggested. Confirms the premise.
- **One construction pattern is universal**: every code is built via the
  `&proto.Error{Code: "...", Msg: ..., Hint: ...}` composite literal.
  `grep` for `proto.NewError(` returns zero matches — no factory exists.
  AST extraction is straightforward.
- **Two non-literal exceptions, both in [internal/runner/runner.go](../../internal/runner/runner.go)**:
  `prog + "_failed"` (4 sites) and `prog + "_not_found"` (1 site). The
  callers pass `prog="go"` (verbs/test) or `prog="git"` (verbs/git),
  yielding the concrete codes `go_failed`, `go_not_found`, `git_failed`,
  `git_not_found`. The spike retains the computed form *and* expands to
  the known callers. **`git_failed` is the single most scattered code:
  34 sites.**
- **Hints (post-ASH-84) are inline strings**: 9 codes carry hints today.
  Capturing them inline alongside the code (as the spike does) is the
  right shape — they are tokenized too and the legibility tradeoff sits
  next to the code they ride with.
- **`Msg:` fields are usually `"static prefix" + err.Error()`**. The
  spike records the prefix only; the dynamic tail is data, not protocol,
  and out of scope.
- **Per-code cl100k cost is small but non-trivial in aggregate**: 114
  tokens total for the unique code strings; multi-token codes
  (`range_out_of_bounds=4`, `<prog>+_failed=6`) anchor the cost. On
  any given response only one code fires, so the per-call ceiling is
  ~7 tokens — confirms the [2026-05-13 substitution measurement](2026-05-13-encoding-substitution-measurement.md)
  finding that error-code rewrites are <0.05% aggregate, +18% on
  error responses only.

Spike code: [cmd/ashvocab/main.go](../../cmd/ashvocab/main.go),
[cmd/ashvocab/errors.go](../../cmd/ashvocab/errors.go). ~280 lines, no new deps.

## Decisions

### 1. Extraction strategy: **AST walk, no refactor**

The spike proved AST extraction is straightforward and complete for
codes. The hypothetical "convention refactor first" (route all
construction through `proto.NewError(...)`) is **not worth the upfront
cost**: there is no live problem with the composite-literal pattern,
and the AST walk handles even the runner's `prog + suffix` shape
cleanly. The single concession the AST needs is a small case for
binary-add expressions on `Code:`, which the spike already implements.

The hybrid option (centralize codes in `internal/proto/vocab.go` and
import via reflection) was tempting but premature: the codes are not
*used* from a central place today; they are constructed and read on the
wire. A reflection-based generator would force a wholesale refactor for
zero observable agent-side benefit. **Defer until a concrete consumer
appears** (e.g. a renderer that wants to map code→hint canonically).

### 2. Artifact format: **Markdown + JSON, both checked in**

The spike is markdown-only. ASH-102 should additionally emit a JSON
sibling so downstream tools (encoding substitution measurement, future
schema-dictionary work) can consume the inventory programmatically. Both
land under `docs/vocab/`:

- `docs/vocab/inventory.md` — human review surface; sorted by category
  then cl100k cost.
- `docs/vocab/inventory.json` — machine-readable; per-entry
  `{category, literal, cl100k_tokens, sites: [{file, line}], hints?}`.

The markdown stays the source of truth for human review. The JSON is
small enough (estimated <50 KB) to commit; CI lint diffs both.

### 3. Tooling shape: **`cmd/ashvocab/` binary, not a verb**

The spike is already a binary. ASH-102 should keep it that way:

- **It is regeneration, not agent-facing introspection.** The output is
  static (changes when code changes, not per-call). A verb would burden
  every session with code that runs only on a `go generate`-like cadence.
- **Throwaway risk is acceptable.** If the inventory stops mattering, a
  binary is rm-friendly. A verb would require a deprecation lap.
- **Mirrors the `cmd/encexplore/` precedent.** Throwaway exploration
  binaries live in `cmd/`, the verb surface stays small.

Caveat: when *another* verb wants vocab data live (e.g. `ash report
--vocab-hot` to show which codes the session hit most), that future verb
imports `internal/vocab.Inventory()` populated from the same generator's
internals — not from re-running `cmd/ashvocab`. ASH-102 should structure
the generator so most logic lives in `internal/vocab/`, with
`cmd/ashvocab/` as a thin entry point. The spike's `cmd/ashvocab/errors.go`
is mostly extraction logic; moving it to `internal/vocab/extract.go`
during ASH-102 is the natural seam.

### 4. CI lint: **regenerate-and-diff, standard `go generate` pattern**

```
make vocab        # regenerate docs/vocab/inventory.{md,json}
make vocab-check  # regenerate to a temp dir, diff against checked-in, exit 1 on drift
```

`go vet`-equivalent. CI runs `make vocab-check`. Local agent flow: edit
an error code → `make vocab` → commit both the code change and the
regenerated artifact.

### 5. Variant-shape categorization: **defer, validate later**

The ticket proposes a table of "variant shapes" (paths, line:col,
sizes, timestamps, SHAs, branches) with typical cl100k cost. The
spike's findings argue for **deferring this to a separate ticket after
the static-vocabulary generator ships**:

- Variant-shape cost depends on *what value the agent actually sees*,
  which is corpus-dependent. The [2026-05-13 measurement corpus](../../testdata/corpus/)
  already contains real values for paths, line numbers, sizes; that
  corpus, not synthetic examples, should anchor the shape estimates.
- The static categories (verb names, flag names, flag value enums,
  error codes, status enums, headers, pretty labels) are the
  fixed-protocol surface and have a single source of truth in the
  code. Variant shapes are *operational* vocabulary and want a
  measurement-driven approach.

Recommendation: variant-shape table → follow-up ticket "vocab —
measure variant-shape token costs from corpus" after ASH-102 ships.

### 6. Categories to cover in ASH-102

Beyond error codes (this spike), the static-vocabulary categories are:

| category | source | extraction | sites estimate |
|---|---|---|---|
| verb names | `internal/verbs/verbs.go` `verbMap` keys | direct import (already a map) | 17 |
| flag names | `internal/verbs/help/help.go` `verbSchemas` | direct import | ~80 |
| flag value enums | `internal/verbs/<verb>/*.go` `argutil.OneOf` calls | AST walk for `argutil.OneOf(... []string{...})` | ~10 |
| error codes | `internal/verbs/`, `internal/runner/`, `internal/jail/` | AST walk for `proto.Error{Code:...}` (this spike) | 51 codes / 213 sites |
| status enums | `internal/verbs/test/test.go` `Test.Status` constants; envelope `OK` | AST walk + reflection on `Result` structs for `msgpack:"...,omitempty"` enum-shape fields | ~10 |
| headers/dividers/footers | `internal/proto/pretty.go`, per-verb `PrettyResponse` | AST walk for format-string literals inside `PrettyResponse` functions; regex-bound to `=== …`, `[ash …]`, `[truncation: …]` | ~30 |
| pretty labels | per-verb `PrettyResponse` | AST walk for non-data string literals in `Fprintf` calls; **hardest category** — see open question below | ~50 |

## Open questions for ASH-102

1. **Pretty-label noise floor.** A naive AST walk over format-string
   literals in `PrettyResponse` will catch a lot of glue (`": "`, `"\n"`,
   `"  "`) that isn't vocabulary. ASH-102 needs a small allowlist /
   minimum-length filter; sketch in implementation.

2. **Should the inventory carry the `Msg:` static prefix?** The spike
   omits Msg from the codes table but records it inline at each site.
   Decision: **keep it inline only**. Msg prefixes vary too much to
   aggregate (one prefix per site) and the agent rarely sees the same
   one twice — they're per-site context, not vocabulary.

3. **`tokens_out_claude` column.** The 2026-05-13 measurement found
   cl100k_base undercounts Claude by ~19% on real corpora. The vocab
   inventory should record cl100k for now (consistent with the ledger);
   the Claude column is a follow-up ([ASH-99](https://linear.app/stazelabs/issue/ASH-99)).

## Ticket-ready plan for ASH-102

**Title.** `vocab — implement generator + CI lint; emit checked-in vocabulary inventory`

**Scope (in).**

- Move spike extraction logic from `cmd/ashvocab/errors.go` to
  `internal/vocab/extract.go`.
- Add extractors for the remaining six categories listed in the table
  above. Verb-name and flag-name extractors import directly; the others
  AST-walk.
- Emit `docs/vocab/inventory.md` and `docs/vocab/inventory.json`. JSON
  schema is `{generated_at, tokenizer, categories: {<name>: [entry...]}}`
  where each entry is `{literal, cl100k_tokens, sites?: [{file,line}], hints?: []}`.
- `make vocab` target and `make vocab-check` CI lint that regenerates
  to a temp dir and diffs. Wire `vocab-check` into CI alongside `go vet`.
- Delete `cmd/ashvocab/errors.go`; keep `cmd/ashvocab/` as a thin entry
  point dispatching to the `internal/vocab` package. Rename the
  `errors` subcommand to `gen` and drop the per-category subcommands —
  the generator emits all categories in one pass.
- Delete `testdata/vocab_spike.md`; the spike is superseded by
  `docs/vocab/inventory.md`.

**Scope (out).**

- Variant-shape table (paths/sizes/SHAs/timestamps). Separate ticket.
- Convention refactor of error-code construction. No live problem.
- Any change to user-visible wire format or pretty rendering.
- `tokens_out_claude` column. Tracked under ASH-99.

**Risks / unknowns to call out in the ticket body.**

- The pretty-label extractor is the riskiest piece — noisy literals and
  format-string fragments will require filtering judgment.
- The status-enum extractor needs a decision: scan const blocks
  (`const StatusPass = "pass"` pattern) or struct-tag-walk + scan
  Fprintf arguments. The spike did not exercise this category.
- Aggregate token counts will tempt premature "shrink the surface"
  tickets. The 2026-05-13 measurement showed structural changes
  (`metrics_no_equals`, `headers_compact`) dominate; error-code and
  status-enum rewrites barely register. **Ship the inventory first,
  then debate substitutions with data.**

**Acceptance criteria for ASH-102.**

- `make vocab` runs offline (no network), <2 s.
- `docs/vocab/inventory.md` covers all seven categories.
- `docs/vocab/inventory.json` is valid JSON, schema documented in the
  generator package.
- `make vocab-check` fails when the inventory drifts from the code.
- CI runs `make vocab-check`.
- Cross-reference: every error code in the inventory appears in at
  least one source site that still exists (catches dead codes).

## Friction

- The PreToolUse hook caught one bash `head` invocation while inspecting
  the spike output; redirected to `ash read --range` cleanly. The hook
  also caught `ls cmd/ashvocab/` once — the spike used `ash find`
  instead. No friction in the spike's actual codepath; the AST walk
  was bash-only as expected (Go tooling is on the bash whitelist).
- One Go-toolchain edge: `parser.ParseFile` on `_test.go` files emits
  test-only code, which the spike filters with a suffix check. ASH-102
  should keep the same filter — test files contain string literals that
  look like codes but aren't part of the production vocabulary.

## Instrumentation

```
$ bin/ashvocab errors
wrote testdata/vocab_spike.md (51 codes, 213 sites)
```

[testdata/vocab_spike.md](../../testdata/vocab_spike.md) — 24 KB, 51 codes
sorted by cl100k cost (costliest first), per-code site lists with
clickable repo-relative line links and hint capture.

## Out-of-scope items surfaced

- **Dead-code detection.** Cross-referencing the inventory against
  ledger rows would surface error codes that have *never fired*. Out
  of scope for ASH-102 but a natural follow-up.
- **Error-code naming consistency.** The inventory exposes drift:
  `not_a_repo` vs `not_a_dir` vs `not_dir`; `gitignore_read` vs
  `guidance_read` vs `settings_read`; `walk` vs `read` vs `stat` as
  bare nouns. A separate cleanup ticket could canonicalize. Not for
  ASH-102.
- **Per-call hint ridealong cost.** 9 codes carry hints today; hints
  are 5-15 tokens each. On the error-path responses in the
  substitution corpus, hints sometimes outweigh the code itself. A
  hint-trimming pass is a candidate ticket after the inventory ships.
