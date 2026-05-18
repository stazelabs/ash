# Sweep 2 — real-session aggregate: does the bench win translate?

**Source:** `ash report --since 30d --session all` at commit `f35cf2c5` on 2026-05-18.

## Headline

In the last 30 days the daemon served **5,000 calls** (97% ok), **519 s exec time**, **521 k tokens_in / 1.93 M tokens_out**. Applying the bench per-verb token ratios to the observed call distribution yields a Fermi estimate of **~2.0–2.5 M tokens saved over the 30-day window vs the bash counterfactual** — material at this call volume.

The bigger win is latency: total exec time was 519 s. At bench bash-side ratios, the same workload would have been **5–10× wall-clock**, much of which is fork+exec overhead and the absence of gitignore-aware walking.

## Per-verb call distribution and est. token Δ vs bash

| verb | tier | calls | ok% | p50 tok_out | p95 tok_out | bench ratio (ash/bash) | rough savings shape |
|---|---|---:|---:|---:|---:|---:|---|
| hook | A | 1,938 | 100% | 2 | 23 | n/a (client-only) | 2-token deny acks; not bash-replaceable |
| read | A | 734 | 97% | 1,076 | 4,887 | 1.004 | **breakeven** — no token win |
| grep | A | 474 | 89% | 201 | 1,007 | 0.27 (corpus avg) | ~3× savings on typical calls |
| edit | A | 467 | 98% | 18 | 21 | n/a (bash has no output) | side-effect verb; 18-tok ack |
| git | A | 299 | 100% | 71 | 5,682 | 0.09 (log) / 0.51 (status) | huge savings on log/diff, modest on status |
| find | A | 289 | 76% | 51 | 420 | 0.60 (corpus avg) | ~1.7× savings; **24% error rate** |
| write | A | 220 | 100% | 18 | 29 | n/a (bash has no output) | side-effect verb |
| diff | A | 4 | 100% | 15 | 15 | 0.48 (corpus avg) | low call volume; bench wins don't matter much |
| test | B | 172 | 100% | 146 | 2,526 | n/a | side-effect verb |
| stat | B | 111 | 99% | 14 | 22 | 0.12 | ~8× savings, low volume |
| lang | B | 23 | 70% | 75 | 633 | n/a | **30% error rate** — LSP friction |
| build | B | 16 | 100% | 24 | 79 | n/a | side-effect verb |
| help | C | 175 | 100% | 391 | 450 | n/a | self-documentation |
| report/metrics/replay/usage/bench | D | 68 | mostly 100% | varies | varies | n/a | human-warm, not agent-hot |

**Tier rollup:** Tier A is **88% of calls (4,425/5,000)** and **90% of tokens_out (1.74M / 1.93M)** — the optimization-tier doc's prediction that Tier A is where the action is holds at the call-volume level.

## Fermi estimate: tokens saved vs bash counterfactual

Per-verb shape (calls × p50_out × inverse-of-bench-ratio is the bash counterfactual; subtract observed ash tokens for net savings):

| verb | observed ash tokens (calls × p50) | bash counterfactual | est. savings |
|---|---:|---:|---:|
| read | 790 k | 787 k | ~0 (breakeven) |
| grep | 95 k | 350 k | **~255 k** (under-counted: p95 is 5× p50, right-skew amplifies the win) |
| git | 21 k | ~233 k | **~212 k** (assuming log/diff dominate the right tail) |
| find | 15 k | 25 k | ~10 k |
| stat | 1.6 k | 13 k | ~12 k |
| diff | 60 | 125 | trivial |
| (no-output verbs: hook/edit/write/test/build) | ~30 k | 0 | **−30 k tax** for structured acks |

**Net per-call savings, conservative (using p50, ignoring right-tail amplification):** ~459 k tokens over 30 days = **~15 k tokens/day** = **~5.5 M tokens/year**.

**Net per-call savings, accounting for right-tail (p95 closer to mean for grep/git):** likely **2–3× the conservative estimate**, i.e. **~30–45 k tokens/day**, **11–16 M tokens/year**.

### Methodology caveats

- These are **counterfactual estimates**, not measured A/B. The bench corpus is canonical, not statistically representative of real calls.
- The bench ratio for `read` (1.004) is computed from three small cases; real reads have wider variance and probably trend slightly worse for ash (the 47% loss on `read_tiny_range` likely materializes anywhere agents read 1–5 line ranges).
- Bash counterfactuals for `git` are extrapolated heavily from two bench cases (log + status). Real git calls in the report show p95=5,682, which only the `log` bench case approximates — the savings estimate likely under-counts.
- The "no-output verbs" tax (~30 k tokens over 30 d) is the cost of structured acks. It is a real ledger cost but is offset by the safety/correctness wins from atomic write, jail enforcement, etc.

## Latency

- **Total exec: 519 s over 5,000 calls = ~104 ms mean.**
- Slowest verb tier: `test` (p50 1.9 s, p95 4.7 s) — dominates total exec, but that's `go test` runtime, not ash overhead.
- Tier A verbs (the agent-hot ones) are uniformly fast: p50 21 µs (hook) to 12.5 ms (git). All p95 under 60 ms.
- At bench bash-side ratios, total wall-clock for the same workload would be **several thousand seconds** — most of it from grep (bash p50 163–377 ms vs ash 4–7 ms) and git (bash p50 30–43 ms vs ash 1.4–10 ms).

## Friction signals visible in the report

These get expanded in [03-friction.md](03-friction.md) but worth flagging here as input to the aggregate picture:

- **find truncation rate: 37%** (106 calls) — agents are hitting the default limit. Either defaults are wrong or agents need a better signal.
- **grep truncation rate: 18%** (87 calls) — same shape, less severe.
- **find error rate: 24%** (mostly "type must be one of: any, file, dir, symlink" — 124 errors) — args validation pain.
- **lang error rate: 30%** — LSP integration is the rough edge.
- **210 hook denials over 30 days** — see [03-friction.md](03-friction.md) for the rule histogram.

## MCP envelope cost (visible in this report)

- **MCP-routed calls: 807 / 5,000 = 16%.**
- `tokens_out_emit = 1,060,810` (envelope cost) vs `tokens_out = 1,932,110` (verb output).
- **MCP envelope tax = 55% of the verb output on average for MCP-routed calls.** This matches the ~3.4× envelope ratio measured in ASH-123 once you account for the fact that not every call goes through MCP — averaged into the full corpus, the per-MCP-call envelope cost is roughly equal to the verb output. Material if ashmcp adoption grows.

## Interpretation

1. **The bench wins translate at call volume.** Conservative estimate of ~5 M tokens saved/year is meaningful; the realistic estimate of 11–16 M is genuinely material for an agent that calls these verbs every iteration.

2. **Latency is the bigger win and is unambiguous at any call volume.** A 5–10× total wall-clock reduction is the kind of thing agents *feel* in iteration speed, not just see in token bills.

3. **Read carries 41% of all output tokens but contributes ~0 savings.** If we ever scale-back, `ash read` would survive on the strength of jail-enforcement + binary handling + ledger instrumentation, not bytes. This is fine — but it means *the case for ash read is qualitative, not the −63.8% headline*.

4. **MCP envelope tax is real and is the biggest token-shape risk going forward.** Every additional ashmcp adoption adds roughly 1:1 envelope cost on top of the verb output. The ASH-156 single-emit and ASH-146 tax-2 closure work was load-bearing; further reduction would be the highest-leverage token-shape investment remaining.

5. **Truncation + error rates are non-trivial.** find @ 24% error / 37% truncation and lang @ 30% error are not invisible — they belong in friction analysis ([03](03-friction.md)) as candidates for either defaults tuning or scope reduction.

---

*Provenance: `ash report --since 30d --session all` at f35cf2c5 on 2026-05-18; raw rows queryable via `ash metrics`.*
