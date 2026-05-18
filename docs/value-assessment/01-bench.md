# Sweep 1 — bench corpus: ash vs bash on canonical workloads

**Source:** [bench/baseline.md](../../bench/baseline.md) regenerated `2026-05-18` at commit `f35cf2c5` via `make bench-baseline` (`./bin/ash bench --repeat 5 --warmup 2 --record_baseline true`). Platform `darwin/arm64`, 11 CPUs.

## Headline

**21 cases · ash 41,767 tokens · bash 115,410 tokens · Δ = −63.8%**

The win improved from −54.7% (baseline 2026-05-11, commit a2510969) — bash output grew on several cases (e.g. `diff_stat_only` baseline 9,599 → 12,154; `grep_heavy_func_internal` 27,985 → 44,486) while ash output stayed roughly flat because the envelope is bounded.

## Cases bucketed by token Δ

### Big wins (≥20% saved) — 11 cases

| case | verb | ash_tok | bash_tok | Δtok% | latency Δ |
|---|---|---:|---:|---:|---|
| `diff_stat_only` | diff | 15 | 12,154 | **−100%** | 234µs vs 2,955µs (−92%) |
| `grep_heavy_func_internal` | grep | 5,015 | 44,486 | −89% | 3,389µs vs 15,604µs (−78%) |
| `git_log_20` | git | 750 | 8,577 | −91% | 1,367µs vs 31,612µs (−96%) |
| `stat_bulk` | stat | 30 | 270 | −89% | 69µs vs 5,928µs (−99%) |
| `stat_single` | stat | 14 | 87 | −84% | 52µs vs 4,128µs (−99%) |
| `grep_rare_pattern` | grep | 88 | 314 | −72% | 38ms vs 4,515ms (−99%) |
| `find_go_files_absolute` | find | 1,440 | 3,359 | −57% | 1,576µs vs 18,659µs (−92%) |
| `grep_parseargs_absolute` | grep | 5,985 | 13,175 | −55% | 6,653µs vs 12,439µs (−47%) |
| `git_status` | git | 20 | 39 | −49% | 10,167µs vs 42,554µs (−76%) |
| `find_shallow` | find | 47 | 91 | −48% | 219µs vs 1,773µs (−88%) |
| `grep_parseargs_internal` | grep | 5,985 | 9,424 | −36% | 5,422µs vs 11,542µs (−53%) |

Pattern: **grep, find, git, stat, diff dominate the wins.** These are the agent-hot verbs (Tier A in [docs/optimization-tiers.md](../optimization-tiers.md)). The token win is structural — bash dumps raw stdout while ash returns structured records with bounded truncation; the latency win is mostly gitignore-aware walking (find/grep) and avoiding fork+exec (stat).

### Near tie (±20%) — 7 cases

| case | verb | ash_tok | bash_tok | Δtok% |
|---|---|---:|---:|---:|
| `grep_files_only` | grep | 910 | 1,109 | −18% |
| `grep_todo_repo` | grep | 1,139 | 1,306 | −13% |
| `find_go_files` | find | 1,440 | 1,609 | −11% |
| `diff_two_files` | diff | 11,562 | 12,154 | −5% |
| `find_md_in_docs` | find | 194 | 188 | +3% |
| `read_range` | read | 739 | 731 | +1% |
| `read_small` | read | 6,331 | 6,320 | +0% |

Pattern: **read is a wash** — the envelope tax (per-record path prefix + headers) lines up almost exactly with the bash overhead of `cat`/`head`/`sed` plus shell framing. The other near-ties are workloads where the bash output is already small enough that the structured envelope doesn't have headroom to compress.

### Loss (≥20% worse) — 1 case

| case | verb | ash_tok | bash_tok | Δtok% |
|---|---|---:|---:|---:|
| `read_tiny_range` | read | 25 | 17 | **+47%** |

A 17-vs-25 token gap on the smallest read in the corpus. Envelope cost dominates when the payload is one line. Not load-bearing in absolute terms (8 tokens) but it does mean the envelope-tax cliff is real — agents that habitually read 1-2-line ranges pay a small tax for the structure.

### No-output ops — 2 cases

| case | verb | ash_tok | bash_tok |
|---|---|---:|---:|
| `edit_string_replace` | edit | 20 | 0 |
| `write_small` | write | 18 | 0 |

Bash equivalents (`sed -i`, `cat > file`) succeed silently with no stdout. Ash returns a structured success ack (~20 tokens). Apples-to-oranges — the value of these verbs is in the side-effect (atomic write, structured edit semantics), not the response payload. Worth noting that the 20-token ack is the cost of converting "no news = good news" into "explicit confirmation," which is an agent-friendly tradeoff (the harness Write tool similarly returns an ack).

## Latency

Ash beats bash on **every single case** in p50 latency, often by 1–2 orders of magnitude. The two heaviest grep cases (`grep_rare_pattern`, `grep_todo_repo`) take 4.5+ seconds in bash vs 12–38 ms in ash — a 100×+ win driven by gitignore-aware walking that bash `grep` lacks. Even the smallest cases (read, stat) save 1–6 ms per call by avoiding fork+exec.

## Interpretation

1. **The −63.8% aggregate is real and load-bearing.** It is driven by the agent-hot verbs (grep, find, git, stat, diff) which are exactly the calls an agent makes most often. Token wins compound at the call volumes those verbs see in real sessions (validated in [02-aggregate.md](02-aggregate.md)).

2. **Read is the breakeven verb.** No structural advantage over `cat`/`head`. The reason to ship `ash read` is *not* token savings — it's path-canonicalization, jail enforcement, ledger instrumentation, and binary-base64 handling. Those need to be the justification, not raw bytes.

3. **The `read_tiny_range` loss is a real but tiny envelope-tax artifact.** Not worth optimizing alone but is a signal to be mindful of: every verb whose output is small enough to fit in 1 record pays a non-trivial envelope tax. The same shape will appear for any 1-line `grep`, 1-file `find`, etc.

4. **Latency is unambiguously won.** Even on the breakeven token cases, the per-call wall-clock is 10–50× faster. For agents whose iteration loop is rate-limited by tool latency (which is most of them), this is the biggest user-visible win and arguably more important than the token delta.

---

*Provenance: `make bench-baseline` at f35cf2c5 on 2026-05-18; raw data in [bench/baseline.json](../../bench/baseline.json) and [bench/latency-snapshot.json](../../bench/latency-snapshot.json).*
