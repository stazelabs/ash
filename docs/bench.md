# `ash bench` — measurement framework: pre-ash vs ash (design)

## Context

The PreToolUse hook is now redirecting bash `grep`/`find`/`cat`/`git status`/etc. to ash equivalents in this repo. The ledger captures detailed per-call instrumentation: real cl100k_base tokens (in/out), parse/exec/serialize latency, walk/io/regex sub-phases, truncation, args. We have a dense, honest picture of *what ash costs*.

What we don't have: any record of what the bash counterfactual would have cost. Without that, "ash saves tokens" is conjecture, not measurement. This doc establishes (a) the conceptual frame for what "saving" means, (b) what data is missing today, and (c) the design for `ash bench`, the first ship that closes the gap.

## How to think about the comparison

Four cost dimensions, ordered by measurement difficulty:

| Dimension | What it measures | Measurable today? |
|---|---|---|
| Output tokens | Tokens the agent consumes from one call's response | Yes — `tokens_out` per ash call (real cl100k_base) |
| Wall-clock latency | Daemon exec time | Yes — `latency_exec_us` (+ walk/io/regex sub-phases) |
| Agent step-count | Calls needed to accomplish a task | Inferable from session ledger; not aggregated yet |
| Cognitive correctness | Did the agent reach the right answer with fewer false starts | Hard — needs full-session experiments |

For (1) and (2), comparison is unlocked the moment we record what bash would have cost. (3) and (4) require multi-call sessions and are downstream.

**Hypothesis on where ash should win/lose:**

- Big wins on grep/find when result sets are large — ash truncates with a narrow-this-query hint; bash dumps everything.
- Small wins / breakeven / minor losses on tiny queries — ash JSON envelope can exceed raw bash output for one-line answers.
- Latency wins from in-process daemon on every call (no fork+exec).
- Step-count wins from structured output (one ash grep returns path+line+excerpt+context; bash often needs follow-up reads).

The measurement should make these wins *visible per verb and per query shape*, not blended into a single average.

**Caveat to be honest about:** ash truncating to 256 matches is a "saving" only because the agent rarely wants 8000 matches — but the comparison is between two different artifacts. Reporting must be careful: "ash spent X to convey 256 matches with a narrow hint; bash would have spent Y to dump 8000." Both numbers are real; the design choice is what makes ash cheaper. The case list should also include small-query cases where ash is *expected* to be breakeven or worse, so we don't blind-spot the regressions.

## Existing infrastructure (recap)

- Ledger schema: 20-column `calls` table at [internal/ledger/ledger.go:19-52](../internal/ledger/ledger.go#L19-L52).
- Single chokepoint: [cmd/ashd/main.go:95](../cmd/ashd/main.go#L95) (`handle()`) — every verb flows through here.
- Tokenizer: pure-Go `tiktoken-go` accessible via `led.Counter().Count(s)` — can tokenize bash output too.
- Report aggregation: [internal/verbs/report/report.go](../internal/verbs/report/report.go) — extensible.
- Hook bash↔ash mapping table is encoded in `.claude/hooks/prefer-ash.py`. Doesn't log denials today.
- No existing bench, shadow, or comparison code. Greenfield.

## What's missing (the gap)

1. **Bash counterfactual data.** No record of what bash would have returned (bytes, tokens, latency).
2. **Hook denial telemetry.** Hook denies but doesn't log; no record of how often the agent reaches for bash.
3. **Comparison aggregation in `ash report`.** No view that places ash and bash side-by-side.
4. **Session-level token rollup.** No "this session: X tokens spent on ash; Y estimated tokens saved" summary.

## First ship: `ash bench` verb (explicit, controlled)

Why an explicit bench verb rather than continuous in-band shadow execution:

- No per-call overhead in production. Shadow on every call doubles I/O and complicates failure modes (bash hangs, OOMs).
- Reproducible. Run on demand, get the same comparison.
- Easier to reason about correctness — bash equivalent runs in a controlled sandbox once per case.
- Bench results stay out of the production `calls` table; the recursive-development data stays clean.
- Can run in CI later without touching the hot path.

### Verb shape

```
ash bench [--verb <verb>] [--limit N] [--case <name>]
```

For each case:
1. Run via ash (in-process verb dispatch, same Counter + tokenization path used by the daemon) → ash latency, ash tokens.
2. Reverse-translate to bash equivalent via a static mapping table (mirror of `prefer-ash.py`).
3. Run bash equivalent in a subprocess sandbox with timeout + stdout cap → capture stdout, tokenize via the same `Counter`, measure wall-clock.
4. Emit one row per case: `verb, case, ash_tokens, bash_tokens, Δtok%, ash_us, bash_us, Δlat%`.
5. Aggregate per-verb and overall.

Bench results are returned in the response, not persisted to a new ledger table in the first cut. (The verb's own call goes through the ledger like any other verb — that's fine.) If we want trend-over-time later, a `bench_runs` table can be added in a follow-up ship. Keeping the first ship in-memory keeps the schema migration story zero.

### Initial case list (~15 cases, embedded in Go)

Cases are defined as a static slice in `internal/bench/cases.go` to avoid a TOML dep. A future ship can add `--cases <path>` for overrides.

- find: shallow root, deep recursive, glob-heavy (`**/*.go`), gitignore-heavy directory
- grep: rare pattern, common pattern, fixed_string with regex metacharacters, large single file
- read: small file, large file, line range
- git status: clean tree, with untracked
- git log: small limit, with author filter
- stat: single path, bulk paths

Each case includes a `Why:` field documenting the hypothesis it tests (large-result truncation win, small-query JSON-envelope penalty, etc.) so a regression in any direction is interpretable.

### Bash translation: the trickiest correctness piece

The `internal/bench/translate.go` table is the inverse of `prefer-ash.py`. Honesty rules:

- Use the bash command **the agent would actually have written**, not the most charitable bash. (Agents reach for `grep -r`, not `grep -rn --include=...`.)
- Don't add flags that ash adds for free (e.g. `.gitignore` filtering — `git ls-files | xargs grep` would be the truly charitable comparison; we explicitly do *not* do that, because the agent wouldn't have).
- Bash is run with PATH-resolved binaries, no shell expansion, with a hard stdout cap (16 MiB) so we don't OOM the daemon if the agent's hypothetical query would have returned the universe.
- Bash exit code != 0 (e.g. grep with no matches) is recorded but not a failure of the case.

Example mappings:

| ash invocation | bash equivalent |
|---|---|
| `ash find --path . --glob '**/*.go'` | `find . -type f -name '*.go'` |
| `ash grep --pattern TODO --path .` | `grep -rn TODO .` |
| `ash grep --pattern X --path . --files_only true` | `grep -rln X .` |
| `ash read --path README.md` | `cat README.md` |
| `ash read --path X --range 1:50` | `sed -n '1,50p' X` |
| `ash git --op status` | `git status` |
| `ash git --op log --limit 20` | `git log -n 20` |
| `ash stat --paths a,b` | `stat a b` |

### Critical files

**New:**
- [internal/bench/cases.go](../internal/bench/cases.go) — canonical case list (Go-embedded slice).
- [internal/bench/translate.go](../internal/bench/translate.go) — ash→bash mapping per verb.
- [internal/bench/runner.go](../internal/bench/runner.go) — sandboxed bash exec (timeout, stdout cap, env scrub).
- [internal/verbs/bench/bench.go](../internal/verbs/bench/bench.go) — verb body: orchestrates ash+bash runs, returns Result.

**Edit:**
- [internal/verbs/verbs.go](../internal/verbs/verbs.go) — register `bench` in `PrettyHandlers()` and `Runners()`.
- [internal/verbs/help/help.go](../internal/verbs/help/help.go) — add `bench` schema entry.
- [cmd/ash/main.go](../cmd/ash/main.go) — verb usage in `printUsage()`.
- [CLAUDE.md](../CLAUDE.md) and [README.md](../README.md) — add `ash bench` to live verb list (after the verb is proven).

## Verification

1. `go build -o bin/ash ./cmd/ash && go build -o bin/ashd ./cmd/ashd`
2. `bin/ash bench --limit 5` → table of 5 cases with ash and bash columns side-by-side.
3. `bin/ash bench --format json | jq .` → parseable JSON with case-level rows.
4. `bin/ash bench` (full ~15-case run) completes in under 30s.
5. Sanity: ash should beat bash on grep with many matches; should be roughly equal on `read` of a small file; latency should be lower on all small-query cases (no fork+exec).
6. Spot-check: pick one case, run the bash equivalent the bench reports manually, confirm bytes/tokens line up.
7. The `bench` call itself should appear as a row in `ash report` (sanity that it goes through the ledger).

## Follow-ups (in priority order, not part of first ship)

1. **Hook-side denial telemetry** (parallel small ship). `prefer-ash.py` appends one JSONL row per denial to `.ash/denials.jsonl`: `{ts, original_tool, original_command, suggested_ash}`. Behavioral signal.
2. **`bench_runs` table for trends.** Persist bench results so we can track ash improvements over time as the surface evolves.
3. **Optional in-band shadow recording.** `ASH_SHADOW=sample` mode; daemon records bash counterfactual on a sampled fraction of real calls.
4. **`ash report --vs-bash`.** Aggregation that joins production session against bench baselines.
5. **End-to-end session experiment.** Real agent task run twice, hook on / hook off; closest to ground truth on cognitive/step-count savings.

## Conceptual takeaway

The hook puts us in a position to *build* the comparison, not in possession of it. The hook contains the bash↔ash mapping but only uses it to deny. `ash bench` walks that mapping in reverse to run controlled comparisons on demand. Hook telemetry and optional shadow recording are layered follow-ups.

## First optimization round (2026-05-08): `find` pretty form

Bench v1 surfaced `find` at **+148% tokens** vs bash on the per-verb summary (find_shallow +495%, find_go_files +123%, find_md_in_docs +87%). Profiling the pretty form ([internal/verbs/find/find.go](../internal/verbs/find/find.go) `writeRecord`) showed each row was `<F|D|L> <size> <yyyy-mm-dd> <path>` — about ~10 tokens of metadata per record, dominated by the date column (`2026-05-08` is 5 tokens because tiktoken splits yyyy-mm-dd on the hyphens).

Fix: lean default + opt-in metadata.

- Default rendering is path-only, with a trailing `/` on directories to disambiguate from files. Symlinks render as the bare path (use `ash stat` for the link target).
- `--with_meta true` brings back the `<F|D|L> <size> <yyyy-mm-dd> <path>` form for callers that want it.
- Wire/JSON data is unchanged — size + mtime + type are still in the structured `Result.Records`. Only the pretty rendering changes.

Bench delta after the change:

| case | before | after | Δ |
|---|---|---|---|
| find_shallow | +495% | +71% | -424pp |
| find_go_files | +123% | -6% | -129pp |
| find_md_in_docs | +87% | +11% | -76pp |
| **find verb summary** | **+148%** | **+7%** | **-141pp** |
| **overall (13 cases)** | **-40%** | **-44%** | -4pp |

Take-aways the bench made visible:

1. The single biggest cost was the date column — tiktoken splits hyphenated dates aggressively. If we ever bring date back to the default, format it as `yyyymmdd` (3 tokens) rather than `yyyy-mm-dd` (5 tokens).
2. JSON-envelope penalty on tiny queries (find_shallow still +71%) is a header amortization issue — the `§find: N results [scope]` line is fixed cost. Acceptable for now; if it becomes load-bearing, we can collapse the header on small results.
3. Structured data on the wire vs human-readable rendering are separable concerns — the design principle "robot-first, structured everything" survived the change because nothing the agent could *do* with the result lost any information.

## Heavy-tree case (2026-05-08): `grep_heavy_func_internal` exercises truncation

The grep verb summary after the find optimization above sat at **-16% tokens** vs bash. The hypothesis going into bench v1 had been "ash should win big on grep when there are many matches, because ash truncates with a narrow-this-query hint while bash dumps everything." The four original grep cases (TODO across repo, ParseArgs in internal, files-only, rare pattern) all returned well under the default `max_matches=256` cap — none of them ever forced ash to truncate, so the load-bearing argument was untested. (See [ASH-18](https://linear.app/stazelabs/issue/ASH-18).)

Fix: add one case to the canonical set that is guaranteed to hit the cap.

- New case: [`grep_heavy_func_internal`](../internal/bench/cases.go) — `ash grep --pattern func --path internal`. The literal `func` returns ~586 matches under `internal/`, well over the 256-match default. Bash equivalent (`grep -rn func internal`) dumps every line.
- Scope is `internal/` rather than `.` for reproducibility — `internal/` does not include `.git/`, `bin/`, or `.ash/`, so the bash side does not vary with local daemon state, build artifacts, or git pack churn.
- Default-flag honesty: the case does *not* pass `--max_matches` explicitly. The whole point is to exercise the default ash experience. If the default ever changes, the case will reflect that change.

Bench delta after adding the case (13 → 14 cases):

| case | ash_tok | bash_tok | Δtok% |
|---|---|---|---|
| grep_heavy_func_internal (new) | 4982 | 14518 | **-66%** |
| **grep verb summary** | 8728 | 19001 | **-54%** (was -16%) |
| **overall** | 15593 | 31454 | **-50%** (was -44%) |

The case clears the >50% acceptance bar from the ticket and validates the original hypothesis: when result sets are large, ash truncates to 256 matches plus a narrow-this hint while bash returns the full dump, and the token gap is meaningful, not marginal.

Take-aways:

1. The bench needed *both* shapes to be honest — small-query cases (where ash can be neutral or negative due to JSON-envelope overhead) and many-match cases (where the truncation cap is the design choice that buys the win). The first ship had only the former.
2. Real-world repo dependence is not a problem here. The case scope is the repos own `internal/` tree, which evolves with the codebase but is fully checked in — so results are reproducible across machines and shift only when the code shifts. A synthetic blob would be cheaper to keep stable but would test nothing the agent will actually encounter.
3. Vendored / generated trees (e.g. `node_modules`, `vendor/`, large `bin/` artifacts) would push the gap wider still — once `ash` is benched against a larger codebase, that comparison is the next data point. For now, `internal/` is the heaviest tree the repo has on hand.

## Header trimming (2026-05-10): drop verbatim arg echoes from `read` and `find` pretty headers

After the find-pretty work above, three bench cases remained as small-query residuals where ash sat at parity or above bash for reasons traceable to the pretty header rather than the body: `read_tiny_range` (+118%), `read_range` (+3%), and `find_md_in_docs` (+3%). All three were paying tokens to echo arguments the agent already had — `range=X:Y` and `NL` on a verbatim range request, and `[path=…, glob=…, type=…]` on the find header — without surfacing any divergence from the request.

Fix: make the echoed fields divergence-only, or drop them when they carry no novel signal.

- `read` PrettyResponse ([internal/verbs/read/read.go](../internal/verbs/read/read.go)) — `range=` and the `<N>L` line-count are now suppressed when both are pure echoes of the request (range was given, `r.Lines == end-start+1`, `r.RangeReturned` matches the request). Both still emit on divergence: short file in lines mode (NL diverges), bytes-end clamp (RangeReturned diverges). The "you got less than you asked for" signal stays load-bearing.
- `find` PrettyResponse ([internal/verbs/find/find.go](../internal/verbs/find/find.go)) — the `[path=…, glob=…, type=…]` scope echo is dropped entirely. `scopeFromArgs()` is kept in the file for potential future `--meta=true` diagnostic output. Header is now `§find: N results [TRUNCATED]`.

Bench delta after the change:

| case | before | after | Δ |
|---|---|---|---|
| read_tiny_range | +118% | +59% | -59pp (~-10 tokens) |
| read_range | +3% | +1% | -2pp (~-10 tokens) |
| find_md_in_docs | +3% | +1% | -2pp (~-12 tokens) |
| **overall** | **-53.7%** | **-55.1%** | **-1.4pp** |

Take-aways:

1. The design principle made explicit: **don't echo what the agent already has.** The header should carry only what the agent could not have known before the call returned — counts, truncation flags, divergence from request. Verbatim arg echoes are dead weight; they cost tokens and add no signal.
2. `read_tiny_range` stays positive (+59%) and that is now the floor. Bash `sed -n 1,5p` has no header at all; the residual ~6 tokens of `§<path> <size>B` framing (post-ASH-114) is the irreducible cost of structured output over `sed`'s plain stdout. Closing it further would require a `--raw` mode that emits just the body — explicit non-goal: an extra mode adds agent cognitive load and undermines the "robot-first, structured everything" principle that buys ash its wins elsewhere.
