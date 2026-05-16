# ash CLI surface: token-efficiency evaluation

> **Status (2026-05-16):** This was the original Tier 1-4 token roadmap. Most of Tier 1 and Tier 3 has shipped — `1.5` stderr footer compaction, `1.6` structured truncation hint (ASH-121, ASH-127), `3.1` no-arg help one-liner (ASH-147), `3.2` compact per-arg help (ASH-143), `3.4` error shape via `_meta` (ASH-127). Tier 2 is partial: `2.3` positional `ash git --op` shipped; the broad `2.1` flag rename pass landed for some flags (`--old`/`--new`, `--meta`, `--max`, `--mpf`, `--bytes`) and not others. Tier 4 explicitly deferred. Treat this doc as a roadmap, not a live status surface — verify any specific item against `bin/ash help --verb <name>` and `git log --grep ASH-NN` before acting on it.

## Context

`ash` is targeted exclusively at coding agents — it has no human users. That gives us full freedom to optimize the verb/flag/response surface for token cost without needing to preserve human affordances. The recursive-development premise of this project (you are the first user, and your friction drives design) makes the dogfood signal especially clean: every token we shave off ash's surface is a token freed for actual reasoning.

Today the surface has 17 verbs, ~80 flags, an msgpack-internally / pretty-on-the-wire envelope, and a help-text format inherited from human-targeted CLI conventions (snake_case, fixed-width columns, prose descriptions, default-true booleans, repeated long field keys per row in JSON). [proto.go:5](../internal/proto/proto.go#L5) explicitly describes `Request.V` as "the seam through which schema-dictionary optimization will arrive without breaking older clients" — the proto was already designed with this work in mind.

This doc deeply evaluates the current surface and itemizes proposed improvements with token-savings estimates (cl100k_base, but the principles generalize to o200k and other BPE tokenizers — any change that removes underscore-splits or repeated long field names is a win across all of them).

**Backwards compatibility is not a constraint** at this prototype stage. Renames, breaking proto changes, and removed flags are all acceptable. Recommendations below assume a clean break, not a migration period.

## Verified baselines

Real cl100k_base tokenization measurements:

| Call | tokens_out |
|---|---|
| `ash help` (no args) | 3,703 |
| `ash help --verb find` | 309 |
| `ash report --since 1h` | 1,202 |
| `ash metrics --last 3` (pretty) | 107 |

Plus the per-call stderr footer (`[ash metrics: bytes_in=…bytes_out=…tokens_in=…tokens_out=… (real:cl100k_base) latency_us=…/… phases=walk:…/io:…/regex:…]`) costs ~25–35 tokens *every single call*.

## Surface findings (current state)

**Input side (~80 flags):** All snake_case. cl100k splits on `_`, so `respect_gitignore` ≈ 4–5 tokens; `include_hidden` ≈ 3; `context_before` ≈ 3; `latency_dispatch_us` ≈ 3. Some heavily-used verbs have no positional shortcut (e.g., `ash git --op log` instead of `ash git log` — pure overhead).

**Output side — bigger cost driver:**
- `--format json` is **broken**: `Response.Data` is `msgpack.RawMessage` tagged `json:"data,omitempty"` ([proto.go:53-58](../internal/proto/proto.go#L53)). Go's stdlib base64-encodes any `[]byte` to JSON, so the entire result payload arrives as opaque base64. Agents can't parse it. This forces everyone onto `pretty`, which then has its own cost (column headers, summary lines, separators).
- The `Metrics` envelope ([proto.go:60-84](../internal/proto/proto.go#L60)) emits `tokens_in`, `tokens_out`, `tokens_method`, `bytes_in`, `bytes_out`, `latency_parse_us`, `latency_exec_us`, `latency_serialize_us` on **every** response without `omitempty`. ~5–15 tokens of dead weight per call.
- Long snake_case JSON keys repeat per row in row-shaped responses (`metrics`, `report.by_verb`, `git --op log`, `test`, `find`, `grep`). For `metrics --last 50` that's ~14 keys × 50 rows ≈ 700+ tokens **just on keys**.
- `truncation_hint` is prose: `"hit limit of 10 records. narrow with --glob, --type, --max_depth, or --exclude; or raise --limit (max 4096)."` ≈ 30 tokens. Truncation is common in find/grep.
- Help text ([help.go:469 PrettyResponse / :489 writeArg](../internal/verbs/help/help.go#L469)) is fixed-width columnar with prose descriptions designed for humans.

## Proposed changes (prioritized)

### Tier 1 — Output format wins (highest impact, lowest risk)

**1.1. Fix `--format json` so it isn't base64 msgpack** — *unblocks 1.2 and 1.4.*
- Decode `Response.Data` to a typed result via the verb's existing `proto.UnmarshalData` ([proto.go:165](../internal/proto/proto.go#L165)) and re-marshal as JSON. Today `data` round-trips as base64-encoded msgpack.
- **Where:** [cmd/ash/main.go](../cmd/ash/main.go) JSON branch; central per-verb `Result()` factory registered alongside the verbs in [internal/verbs/verbs.go](../internal/verbs/verbs.go).
- **Token savings:** Indirect (no callers can use JSON today). This is a correctness fix that unlocks the rest.
- **Risk/cost:** Low. ~50 LOC + a verb→Result-factory registry.

**1.2. Add `--format compact` (array-of-arrays for row-shaped results)**
- Emits `{"k":["ts","verb","ok","ti","to","ex_us","bi","bo"],"r":[[...],[...]]}` — keys appear once.
- **Where:** New `internal/proto/compact.go`. Apply to row-shaped verbs only: `metrics`, `report.by_verb`, `git --op log/status/diff`, `test packages[].tests[]`, `find`, `grep`, `stat`. Other verbs fall back to JSON.
- **Token savings:** Replaces ~14 keys/row with positional values. `metrics --last 50` saves ~600–900 tokens on key overhead alone. **40–60% reduction on row-shaped responses.**
- **Format invariant (ASH-93):** `pretty` is the default for all verbs. `--format compact` is available explicitly; `--format json` emits the full envelope. The bench tokenizes `pretty` output (via `d.Pretty()`) which matches what agents receive — the measurement is honest. Never make `compact` the default: it couples the bench substrate to the on-wire format and causes silent misalignment.
- **Risk/cost:** Low. ~120 LOC + per-verb shims (~10 LOC × 7 verbs).

**1.3. Aggressive `omitempty` on Metrics + drop default-zero fields**
- Add `omitempty` to `TokensIn`, `TokensOut`, `TokensMethod`, `BytesIn`, `BytesOut`, `LatencyParseUs`, `LatencyExecUs`, `LatencySerializeUs`. Drop `tokens_method` from the wire when it equals the daemon default (`"real:cl100k_base"`).
- **Where:** [internal/proto/proto.go:60-84](../internal/proto/proto.go#L60) struct tags.
- **Token savings:** ~5–15 tokens per call on the metrics envelope. Heavy session of 100 calls: **500–1,500 tokens.**
- **Risk/cost:** Trivial. ~15 LOC of struct-tag changes + tests.

**1.4. Short metric field names (proto v2 negotiation)**
- Bump `proto.ProtocolVersion` to 2; introduce short keys: `lp` (latency_parse_us), `le`, `ls`, `ld`, `ti`, `to`, `tm`, `bi`, `bo`, `tr`, `ph` (phases) with `w`/`io`/`r`/`rc`. The seam already exists: `Request.V` is documented at [proto.go:5](../internal/proto/proto.go#L5) for exactly this purpose.
- **Where:** Metrics + Phases tags in [proto.go:60-99](../internal/proto/proto.go#L60); maybe [internal/proto/tracer.go](../internal/proto/tracer.go).
- **Token savings:** `latency_dispatch_us` is 3 tokens, `tokens_method` is 2, `latency_serialize_us` is 4 — per-call **15–25 tokens** on the metrics envelope. Aggregate **1,000–2,000** session-wide.
- **Risk/cost:** Medium. ~80 LOC. Bench tests need updating. (No bw-compat needed — bump V to 2 anyway as a cache-bust marker.)

**1.5. Compact stderr metrics summary line**
- Trim per-call footer. Current: `[ash metrics: bytes_in=N bytes_out=N tokens_in=N tokens_out=N (real:cl100k_base) latency_us=N/N phases=walk:N/io:N/regex:N]` ≈ 25–35 tokens. Compact: `[ash bi=N bo=N ti=N to=N us=N/N w=N io=N r=N]` ≈ 12–18 tokens. Drop tokenizer name unless mismatched, drop zero phases.
- **Where:** [cmd/ash/main.go](../cmd/ash/main.go) ~lines 130–155 (the stderr summary block).
- **Token savings:** **~15 tokens × every call.** 100-call session: **~1,500 tokens.**
- **Risk/cost:** Trivial. ~20 LOC.

**1.6. Replace prose `truncation_hint` with structured `{trunc:1, limit:N, max:N}`**
- The pretty formatter reconstructs the hint phrase locally; agents using JSON/compact get the structured form.
- **Where:** Each verb's Result struct (`find`, `grep`, `git`, `read`, `metrics`, `report`); pretty hooks.
- **Token savings:** **20–30 tokens per truncated call.** Truncation hits ~5–10% of calls in heavy sessions. **300–800 session-wide.**
- **Risk/cost:** Low. ~60 LOC across verbs.

### Tier 2 — Flag renames and input-side wins

**2.1. Rename snake_case multi-word flags to short forms** (direct rename — no aliases)
- `--include_hidden` → `--hidden` (or `-H`)
- `--respect_gitignore` → `--gi` ; accept `--no-gi` for negation
- `--context_before` → `--cb` ; `--context_after` → `--ca` (and add `--context N` for symmetric)
- `--fixed_string` → `--lit`
- `--files_only` → `--fo` (or `--paths-only`)
- `--no_text` → `--no-text` (kebab; no underscore split)
- `--max_matches` → `--max` (verb-specific) ; `--max_per_file` → `--mpf`
- `--max_depth` → `--depth`
- `--replace_all` → `--all`
- `--new_string`/`--old_string` → `--new`/`--old`
- `--new_content` → `--new` (canonical stays stdin)
- `--with_meta` → `--meta`
- `--follow_symlinks` → `--follow` or `-L`
- `--limit_bytes` → `--bytes`
- `--range_kind` → `--unit`
- `--create_only` → `--no-clobber` or `-n`
- `--dry_run` → `--dry`
- `--no_registry` → `--no-registry`
- `--tool_name` → `--tool` ; `--file_path` → `--file`
- `--all_roots` → `--all`
- **Where:** Per-verb `ParseArgs` in [internal/verbs/](../internal/verbs/) and [internal/verbs/argutil/argutil.go](../internal/verbs/argutil/argutil.go); add `Aliases []string` to the help registry in [internal/verbs/help/help.go](../internal/verbs/help/help.go).
- **Token savings:** **2–8 tokens per multi-flag call.** Session aggregate **300–800 tokens in.**
- **Risk/cost:** Medium. ~150 LOC + table-driven tests. Updates to help/CLAUDE.md/hook ride along in the same PR.

**2.2. `--no-foo` negation form for default-true booleans**
- `--no-gi`, `--no-untracked`, `--no-mkdir` instead of `--respect_gitignore=false` etc.
- **Where:** Flag parser in [cmd/ash/main.go](../cmd/ash/main.go); `argutil.RequireBool`.
- **Token savings:** ~3–5 tokens per use; modest, mostly QoL.
- **Risk/cost:** Low. ~20 LOC.

**2.3. More positional slots** (high-value, near-trivial)
- `git`: positional `op` (`ash git log` instead of `ash git --op log`) — saves 2 tokens **per git call.**
- `report`/`metrics`: positional `verb` filter (`ash report find`).
- `help`: positional `verb` (`ash help find` instead of `ash help --verb find`) — saves 2 tokens.
- `bench`: positional `verb`/`case`.
- **Where:** `verbPositionals` map at [cmd/ash/main.go:194](../cmd/ash/main.go#L194).
- **Token savings:** **2–3 tokens per affected call.** git is common — **100–300 tokens** session-wide.
- **Risk/cost:** Trivial. ~20 LOC.

### Tier 3 — Help-text optimization (largest single-call win)

`ash help` (no args) emits **3,703 tokens** today. This is the single biggest fixed cost — agents read help to learn the surface, often repeatedly across sessions.

**3.1. `ash help` (no args) returns one-liner per verb only**
- Today the no-arg form dumps every verb's full schema. Reduce to verb name + one-line description (~150 tokens for 17 verbs). Full schema available via `ash help <verb>`.
- **Where:** [internal/verbs/help/help.go:469 PrettyResponse](../internal/verbs/help/help.go#L469) switch on `Verb == ""`.
- **Token savings:** **~3,500 tokens saved per call.** Even 1–2 calls per session is the largest single-item win in the entire plan.
- **Risk/cost:** Low. ~30 LOC. CLAUDE.md already directs agents to `ash help --verb X` for specifics.

**3.2. Compact agent-targeted help format (one-line per arg)**
- Replace columnar layout ([help.go:489 writeArg](../internal/verbs/help/help.go#L489)) with `<key>:<type>[:!][=<default>] — <one-line>` form. Drop `optional`/`required` keywords (use `!` suffix), drop `default=` prefix, drop "Pass false to suppress" boilerplate.
- **Where:** [internal/verbs/help/help.go:469-489](../internal/verbs/help/help.go#L469).
- **Token savings:** Per-verb help drops ~50%. Combined with 3.1 brings full help corpus from 3,700 → ~1,500–2,000 tokens (45–60% reduction).
- **Risk/cost:** Low. ~80 LOC + golden-file test updates.

**3.3. Tighten descriptions; gate prose behind `--verbose`**
- Rewrite all `Description:` fields to ~10–15 words. Move existing prose ("Doublestar pattern; matched against the path relative to --path." etc.) behind `--verbose`. Drop redundant enum echo when the value is in the default-line.
- **Where:** Every entry in the registry in [internal/verbs/help/help.go](../internal/verbs/help/help.go) (~17 verb structs, ~80 args).
- **Token savings:** Compounds with 3.2. Default per-verb help drops to ~50–100 tokens.
- **Risk/cost:** Low-medium. ~200 LOC of editorial string churn. No code-path changes.

**3.4. Numeric truncation reasons in errors**
- Standardize error structure: `Error.Code` already exists ([proto.go:55](../internal/proto/proto.go#L55)). Move long human messages into a `hint` field that pretty renders locally; keep `Msg` short and stable.
- **Token savings:** **5–15 tokens per error response.** Multiplies across iterative agent failure loops.
- **Risk/cost:** Medium. Touches every error-construction site. ~150 LOC.

### Tier 4 — Speculative / not recommended

- **Verb renames** — most verbs are 1 token already (read/find/grep/git/edit/diff/write/test/stat/help/hook/init/stop/bench). `metrics`/`report`/`uninit` are 1–2 tokens. Churn cost (CLAUDE.md, hook, docs, session-note history, every Linear ticket title) outweighs savings. **Skip.**
- **Per-session schema-dictionary handshake** — LLMs can't track per-session state reliably. Static dict (1.4) captures the win without the complexity. **Skip.**
- **Streaming responses** — agents read full responses synchronously through the harness Bash tool. No clear path. **Park.**
- **Drop `pretty` once `compact` is default** — pretty is genuinely useful for human debugging at low marginal cost. **Soft no.**
- **Flip default-true booleans to default-false** (e.g., `mkdir`) — defaults are already what agents want; flipping just adds tokens. **Skip.**

## Aggregate session savings

For a representative heavy ash session (100 calls: 30 grep, 20 find, 15 read, 10 edit, 10 git, 5 metrics/report, 5 test, 5 help/other):

| Source | Per-session tokens saved (est.) |
|---|---|
| 1.5 stderr summary trim | 1,000–1,500 |
| 1.3 omitempty hygiene | 500–1,000 |
| 1.4 short metric keys | 1,000–2,000 |
| 1.2 compact format on row verbs | 1,500–4,000 |
| 1.6 truncation hint compaction | 300–800 |
| 2.1 flag aliases | 300–800 |
| 2.3 positional slots | 100–300 |
| 3.1 + 3.2 + 3.3 help compaction | 3,500–5,000 (mostly one-shot per session) |

**Total: ~8,000–15,000 tokens per heavy session.** Against an ash-heavy session of ~30k–60k output tokens for ash specifically: **roughly 20–35% reduction in ash-related output tokens**, with help-text wins (3.1/3.2/3.3) doing the heaviest single-call lift.

## Non-token wins coming along for the ride

- **1.1** removes a real correctness bug — `--format json` is currently unusable for downstream tooling.
- **1.2** is also smaller on the msgpack wire — `bytes_out` drops in lockstep with `tokens_out`, which improves the ledger's signal-to-noise (truncation/cost analyses get cleaner).
- **3.1/3.2/3.3** strip ~200 LOC of prose churn from the help registry and address the "help can lag code" gotcha called out in CLAUDE.md.
- **1.3 + 1.4** simplify the msgpack codec slightly and remove dead-zero-field serialization branches.
- **1.6** lets the pretty formatter own truncation-hint phrasing — single source of truth, easier to evolve.
- **2.3** (positional `git`) makes the hook's suggested replacements more natural and brings the verb closer to bash-`git` ergonomics agents already know.

## Recommended ticket ordering for Linear

1. **1.1** (json correctness) — unblocks downstream
2. **3.1** (`ash help` no-arg one-liner) — biggest single-call win, smallest diff
3. **1.5** (stderr footer) + **1.3** (omitempty) — trivial, instant wins
4. **1.6** (truncation hint structuring) + **3.2** (help format)
5. **1.2** (compact format) + **1.4** (short metric keys, proto v2)
6. **2.1** (flag aliases) + **2.3** (positionals) — incremental
7. **3.3** (description rewrite) — editorial sweep, do last so other changes don't churn it

## Critical files

- [internal/proto/proto.go](../internal/proto/proto.go) — envelope and Metrics struct (lines 53–101)
- [internal/proto/pretty.go](../internal/proto/pretty.go) — request/response pretty rendering
- [internal/verbs/help/help.go](../internal/verbs/help/help.go) — registry + PrettyResponse / writeArg (~lines 469, 489)
- [cmd/ash/main.go](../cmd/ash/main.go) — `extractFormat` (~173), `verbPositionals` (~194), flag parsing, stderr footer (~130–155)
- [internal/verbs/argutil/argutil.go](../internal/verbs/argutil/argutil.go) — shared key/type lookup
- [internal/verbs/verbs.go](../internal/verbs/verbs.go) — verb registration (extension point for Result-factory registry)

## Verification (per-ticket, end-to-end)

Each ticket above ships with a measurement plan: capture `tokens_out` for a representative call before and after via `ash report --since 5m` (which already aggregates p50/p95 tokens_out per verb). The ledger is the substrate — every claim in this doc can be re-validated against it post-implementation. For help-text changes specifically, also capture `ash help` and `ash help --verb <every>` token counts before/after to confirm the 45–60% reduction on the help corpus. Ledger queries: `ash report --verb help`, `ash report --verb metrics --since 1d`, etc.
