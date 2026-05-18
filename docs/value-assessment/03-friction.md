# Sweep 3 — friction inventory: where does ash actively cost us?

**Source:** `ash report --since 30d --verb hook` (583 denials in 5,000 hook-call sample) + verb-level error/truncation rates from [02-aggregate.md](02-aggregate.md) + [CLAUDE.md §Gotchas](../../CLAUDE.md#gotchas).

## Headline

**~12% of hook calls in 30 d result in a bash deny** — meaning roughly 1 in 8 of the agent's bash idioms gets redirected. Most redirects are clean (verb maps cleanly to intent); a small but real number are painful (args validation, lang ambiguity). The most-load-bearing friction issues are concentrated in three places: **find args validation**, **find/grep truncation visibility**, and **lang LSP heuristics**.

## Hook denial histogram (30 d, sample of 5,000 hook calls, 583 denies)

| rank | rule | count | suggested replacement | pain shape |
|---:|---|---:|---|---|
| 1 | `Bash:go-build` | 155 | `ash build` | Low — clean map, just muscle memory |
| 2 | `Read` (harness) | 109 | `ash read` | Low — clean map, but signal that agents still reach for harness Read first |
| 3 | `Bash:git-status` | 57 | `ash git` | Low |
| 4 | `Bash:git-diff` | 45 | `ash git` | Low |
| 5 | `Bash:find` | 37 | `ash find` | Low |
| 6 | `Bash:git-log` | 31 | `ash git` | Low |
| 7 | `Write` (harness) | 28 | `ash write` | Low |
| 8 | `Bash:go-test` | 27 | `ash test` | Low |
| 9 | `Bash:grep` | 17 | `ash grep` | Low |
| 10 | `Bash:redirect-write` | 16 | `ash write --content -` | **Medium** — multi-line content via `<<EOF` is awkward to remember (documented as Gotcha) |
| 11 | `Bash:cat` | 14 | `ash read` | Low |
| 12 | `Bash:head` / `Bash:tail` | 11 / 11 | `ash read --range` | Low |
| 13 | `Bash:git-show` | 8 | `ash git --op show` | Low |
| 14 | `Grep` (harness) | 7 | `ash grep` | Low |
| 15 | `Bash:sed` | 4 | `ash edit` / `ash read --range` | **Medium** — sed has wider semantics than either replacement covers |
| 16 | `Edit` (harness) | 4 | `ash edit` | Low |
| 17 | `Bash:stat` | 2 | `ash stat` | Low |

**Aggregate finding:** 13 of 17 rules redirect cleanly; only 3 categories (`Bash:redirect-write`, `Bash:sed`, the git family's chained-command interaction) cost the agent meaningful extra effort per occurrence.

Git ops are the most-redirected category at **141 total denies**. The replacements all work cleanly via `ash git --op <name>`, but the muscle-memory tax is real (3–5 redirects per session is a friction floor).

## Ranked friction sources (severity = frequency × per-occurrence pain)

### 1. find args validation — "type must be one of: any, file, dir, symlink" (124 errors in 30 d)
- **Severity:** highest single error class. 124 occurrences is ~43% of all find errors.
- **Pain:** the rejected value is usually agent intent (e.g. `--type=files` plural, or a glob pattern mistakenly passed as type). Validation message is technically correct but doesn't suggest the fix.
- **Cheap fix:** accept common variants (`files` → `file`, `directories` → `dir`) or expand the error to show what the agent likely meant. Roughly a 1-hour ticket.
- **Verdict:** **fix.** Highest-leverage friction win in the entire inventory.

### 2. find truncation at 37% (106/289 calls) + grep truncation at 18% (87/474 calls)
- **Severity:** high. More than 1-in-3 finds and 1-in-5 greps silently hit limits.
- **Pain:** agents may not notice they got partial results. The `truncated` flag is in the response but its visibility in the pretty rendering may be insufficient.
- **Investigation needed:** are agents acting on partial data without noticing? `ash report` does surface `trunc%` per verb but agents in-the-loop may not check.
- **Verdict:** **investigate before fixing.** Either raise default limits, improve the truncation-signal prominence, or both. Worth a Linear ticket to decide.

### 3. lang verb error rate at 30% (23 calls, 7 errors)
- **Severity:** medium-low (low volume) but high per-occurrence pain.
- **Errors:** `lsp_ambiguous` (symbol matched multiple locations), `lsp_not_found`, syntax-error-in-test-file leakage.
- **Verdict:** **scope decision.** Either invest in better LSP heuristics (auto-pick by package, surface candidates better) or drop the lang verb's claims to "exact-symbol-name lookups only." 30% error rate at low volume tells us few agents are using it; either fix it or formally narrow it.

### 4. Bash:go-build deny — 155 occurrences (the single largest deny class)
- **Severity:** high freq, low per-event pain.
- **Pain:** agent loses one turn per deny to switch from `go build ./...` to `ash build`. Replacement is clean.
- **Implication:** the volume tells us either (a) agents come into the repo without `ash build` in muscle memory, or (b) something about the `ash build` ergonomics is worse than `go build`. Worth a quick check on which.
- **Verdict:** **monitor.** If `ash build` ergonomics are equivalent, this is just onboarding tax that should decay over time.

### 5. Harness-tool denies — Read 109, Write 28, Edit 4, Grep 7 (148 total)
- **Severity:** medium freq, low per-event pain.
- **Pain:** clean redirect to ash equivalents. Signal that even Claude Code's native tools get reached for first.
- **Implication:** the docs/MCP-server adoption story may be more important than we treat it. If ashmcp surfaces these as MCP tools the harness already trusts, the redirect would be invisible to the agent.
- **Verdict:** **strategic input** — strengthens the case for ashmcp as the primary adoption surface, not the CLI.

### 6. Chained-bash-deny whole-command rejection (documented Gotcha #9; ASH-170 in progress)
- **Severity:** unmeasured but documented as recurring pain. ASH-170 (committed 5 days ago) started naming the matched segment so the deny message is debuggable.
- **Pain:** `git add … && git commit … && git status` denies on the status segment, so commit never runs. Loss of one full command-construction worth of work.
- **Verdict:** **partially addressed.** Watch for downstream improvements (e.g. allow the un-denied prefix to run, or document the pattern more loudly).

### 7. Bash:redirect-write and Bash:sed (16 + 4 occurrences)
- **Severity:** low freq, medium per-event pain.
- **Pain:** `cat > FILE << EOF` is muscle-memorized; the `ash write --content -` heredoc form is documented in Gotcha #6 but the documentation tax is real. `sed`'s capability surface is bigger than `ash edit` + `ash read --range` cover (in-place substitution with regex backrefs, multi-file find-replace, etc.).
- **Verdict:** **accept.** These are real but acceptable taxes for non-trivial-fraction-of-1% of calls.

## Gotchas already promoted (the load-bearing tuition)

10 gotchas in [CLAUDE.md §Gotchas](../../CLAUDE.md#gotchas):

1. Daemon config hot-reload is jail-only (ASH-164)
2. Path-form semantics differ across verbs (find/grep mirror, git always rel)
3. Ledger-first debugging
4. `ash help` text can lag code
5. `ash read --range` end is clamped, start is not
6. Hook redirects `cat`/`echo`/`printf`/`tee` + `>` to `ash write`
7. Streaming responses live behind `Request.Stream=true`
8. Mid-stream cancellation is wire-level
9. Chained bash commands die whole on first denied segment
10. Daemon process is long-lived; rebuilds don't auto-restart it

**Read:** each gotcha is a sharp edge that bit at least one agent and got promoted because it would bite the next one too. Ten such edges over the project's life is *not bad* — comparable surfaces (Docker, kubectl, git itself) accumulate orders of magnitude more. But (5), (6), (7), (9), (10) are all "the system behaves in a way that differs from a natural mental model"; reducing this class of friction would matter more than micro-token-shape work.

## Where the report's other errors land

From [02-aggregate.md](02-aggregate.md), errors over 30 d:

| count | error | reading |
|---:|---|---|
| 124 | args (find `--type`) | top friction win above |
| 18 | not_found (ash.toml) | agent running ash outside project root; minor |
| 7 | match_not_found (edit) | whitespace mismatch on `--old`; user error, not ash bug |
| 6 | lsp_ambiguous | lang verb friction (above) |
| 5 | is_dir | agent passed a directory where file expected; minor |
| 1 each | path_denied, range_out_of_bounds, lsp_not_found, syntax, stat | tail; not actionable |

The friction surface is dominated by **find arg validation** and **find/grep truncation**, not by a long tail of weird errors. Both are addressable with modest investment.

## What this tells us for the decision

1. **No deny rule looks like a scope mistake.** Every rule has a clean ash replacement and the agents are following the redirects. The bash dogfooding constraint is paying off rather than producing chaos.

2. **The biggest fixable friction (find args, truncation visibility) is small-ticket work**, not a re-architecture. Both could land in a day each.

3. **The lang verb is the only candidate for scope reduction.** 30% error rate at low call volume tells us it's not pulling its weight as-is.

4. **The ashmcp adoption story matters more than the CLI dogfooding numbers suggest.** 148 harness-tool denies in 30 d means agents in MCP-capable harnesses are reaching for the native tools first; if ashmcp tools occupy that namespace, the friction disappears.

5. **The Gotchas list (10 entries) is the realistic upper bound on agent-facing complexity.** Not zero, but not runaway either. Half are mechanical (config reload, path forms), half are mental-model gaps; the second half is where future investment should go.

---

*Provenance: `ash report --since 30d --verb hook --top 30` and `--session all` at f35cf2c5 on 2026-05-18; CLAUDE.md §Gotchas at HEAD.*
