# Vocab generator — design decisions

Companion to [docs/vocab/inventory.md](inventory.md) (the generated
artifact). This doc captures why the generator looks the way it does.

## Why AST walk over construction refactor

Every error code today is built via `&proto.Error{Code: "...", Msg: ...,
Hint: ...}` composite literal. No factory exists — `grep proto.NewError(`
returns zero matches. A "route all construction through
`proto.NewError(...)`" refactor up front would gate the generator on a
wholesale rewrite for zero observable benefit.

The AST walk handles the one non-literal exception cleanly:
[internal/runner/runner.go](../../internal/runner/runner.go) builds two
codes computationally via `prog + "_failed"` and `prog + "_not_found"`
(callers pass `prog="go"` or `prog="git"`, yielding `go_failed`,
`go_not_found`, `git_failed`, `git_not_found`). The extractor expands
these to the known callers. `git_failed` is the single most-scattered
code (34 sites).

The hybrid option (centralize codes in `internal/proto/vocab.go` and
import via reflection) was tempting but premature — no consumer needs
canonical access today. Defer until a renderer wants to map
`code → hint` live.

## Why both markdown and JSON

- `docs/vocab/inventory.md` — human review surface, sorted by category
  then cl100k cost. Source of truth for review.
- `docs/vocab/inventory.json` — `{generated_at, tokenizer, categories:
  {<name>: [{literal, cl100k_tokens, sites?, hints?}]}}`. Consumers:
  `cmd/encexplore` substitution measurement; future schema/dictionary
  tools.

Both check in. CI diffs both via `make vocab-check`.

## Why a cmd binary, not a verb

- **Regeneration, not introspection.** Output is static (changes with
  code, not per call). A verb would burden every session with code that
  runs only on a `go generate` cadence.
- **Throwaway risk acceptable.** `cmd/` binaries are rm-friendly. Verbs
  carry a deprecation tax.
- **Mirrors `cmd/encexplore/` precedent.** Exploration binaries stay in
  `cmd/`; the verb surface stays small.

When a live consumer eventually appears (e.g. `ash report --vocab-hot`),
it imports `internal/vocab.Inventory()` — populated from the same
extractor logic. The generator binary stays thin (entry point + flag
parsing); the extractors live under `internal/vocab/`.

## CI pattern

```
make vocab        # regenerate docs/vocab/inventory.{md,json}
make vocab-check  # regenerate to a temp dir, diff against checked-in
```

Same shape as `make schema`/`make schema-check` and `make validate`/
`make validate-check`. `vocab-check` is the CI drift gate.

## Categories covered

| category | source | extractor |
|---|---|---|
| verb names | `internal/verbs/verbs.go` `verbMap` keys | direct import |
| flag names | `internal/verbs/help/help.go` `verbSchemas` | direct import |
| flag value enums | `internal/verbs/*/*.go` `argutil.OneOf` | AST walk |
| error codes | `internal/verbs/`, `internal/runner/`, `internal/jail/` | AST walk |
| status enums | `internal/verbs/test/test.go` + envelope `OK` | AST walk + struct tags |
| headers/dividers/footers | per-verb `PrettyResponse` | AST walk on format strings |
| pretty labels | per-verb `PrettyResponse` | AST walk + allowlist filter |

**Initial spike found 51 codes / 213 sites** — substantially more than
the original "~15" estimate. The vocabulary had drifted further than
visual inspection suggested.

`_test.go` files are filtered: test files contain string literals that
look like codes but aren't part of the production vocabulary.

## Decisions deferred

- **Variant-shape categories** (paths, line:col, sizes, SHAs, branches).
  Cost depends on real values, not fixed protocol literals. Belongs in
  a measurement-driven follow-up using the substitution corpus, not
  this static-vocabulary generator.
- **`Msg:` static prefixes.** Vary too much to aggregate (one per site).
  Kept inline at each site, not in the category table.
- **`tokens_out_claude` column** on the inventory. Tracked under ASH-99.

## Open follow-ups

- **Dead-code detection.** Cross-reference inventory against ledger rows
  to surface error codes that have never fired.
- **Naming-drift cleanup.** Inventory exposed: `not_a_repo` vs
  `not_a_dir` vs `not_dir`; `gitignore_read` vs `guidance_read` vs
  `settings_read`; bare-noun mixing (`walk`, `read`, `stat` as codes).
  A canonicalization pass could land separately.
- **Hint-trimming pass.** 9 codes carry hints today (5-15 tokens each).
  On error-path responses, hints sometimes outweigh the code itself.
