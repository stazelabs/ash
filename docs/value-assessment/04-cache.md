# Sweep 4 — cache shape validation: is the ASH-108/135 work paying off?

> **Update 2026-05-18 (ASH-188):** The "44% of pairs are arg-stable" headline in this sweep counts arg-stable pairs *anywhere in a 24h window*, **not** within the 5-minute Anthropic prompt-cache TTL. With the TTL filter applied the cache-eligible count collapses dramatically — `ash usage --since 7d --session all` (post-ASH-185) routinely reports 0 same-args pairs within TTL across 100+ calls. The structural conclusions below are still true as written, but read every "stable pair" / "cache-eligible" number with the TTL caveat in mind: it's an upper bound on what the cache could possibly hit, not an estimate of what it does hit. Real cache-hit telemetry is now wired via the Stop hook → `turns` ledger table (see [docs/cache-telemetry.md](../cache-telemetry.md)); `ash usage` surfaces the real hit rate when turn rows exist.

**Source:** `ash replay --cache_prefix true --since 24h --session all --limit 500` at f35cf2c5 on 2026-05-18.

## Headline

The ASH-108/135 cache-shape work is **structurally validated on real traffic.** Over 24 h: 434 replayable call pairs, 190 (44%) prefix-stable, **average stable prefix 424 bytes** (vs 8 bytes pre-ASH-135). Help responses dominate the win at +14,298 bytes/pair; hook denies are the highest-volume stable case at 172 pairs.

**Caveat:** we measure *structural* prefix overlap, not actual Anthropic prompt-cache hit-rate. The "44% arg-stable pairs in 24h" headline does **not** apply the 5-minute prompt-cache TTL filter — the same measurement with TTL applied collapses toward zero on most real sessions (see header note above). The structural prefix is genuinely +416 bytes per stable pair; what fraction of those structural overlaps actually land within TTL and produce a billable cache hit is unmeasurable from the replay sweep alone.

## Replay summary (24 h window)

- **451 calls replayed**, 49 skipped (5 args-truncated, 13 heavy, 31 mutating).
- orig vs new: 181,749 → 190,515 tokens (+8,766, +4.8%).
- 0 ok-mismatches (result correctness held through replay).
- 33 regressions (largely workload growth, not ash regressions — top offenders are reads/greps where the underlying file content changed since the original call).

The +4.8% token drift is **code change, not ash regression** — files grew between original sessions and replay. Top regressors:
- `read internal/verbs/metrics/metrics.go` — orig 195 tok, new 1,834 tok (+840%): file grew significantly.
- `grep pattern=Code: "[a-z_]+"` — orig 2,860, new 7,151 (+150%): more matches now.

Two improvements visible (calls that got *cheaper*):
- `grep pattern=cacheArgs.*"max"` — −92%
- `grep glob=**/*.md pattern=verbs? (live\|are live)` — −90%

These are likely the result of the CLAUDE.md / docs cleanup in recent commits removing duplicate verb-list strings (ASH-172).

## Cache prefix overlap by verb (ASH-108 A/B)

| verb | pairs | stable | stable% | avg enc_len | old_pre | new_pre | Δgain (bytes) |
|---|---:|---:|---:|---:|---:|---:|---:|
| help | 19 | 5 | 26% | 14,345 | 8 | 14,306 | **+14,298** |
| hook | 196 | 172 | 88% | 88 | 8 | 48 | +40 |
| grep | 85 | 7 | 8% | 104 | 8 | 65 | +57 |
| find | 31 | 6 | 19% | 80 | 8 | 41 | +33 |
| git | 9 | 0 | 0% | 0 | 0 | 0 | 0 |
| read | 91 | 0 | 0% | 0 | 0 | 0 | 0 |
| report | 1 | 0 | 0% | 0 | 0 | 0 | 0 |
| stat | 2 | 0 | 0% | 0 | 0 | 0 | 0 |
| **overall** | **434** | **190** | **44%** | **463** | **8** | **424** | **+416** |

## Reading the numbers

### Help is the per-call jackpot

Help responses are large (~14 KB) and byte-identical for the same `--verb` argument. Every same-arg pair recovers ~14 KB / ~3,500 cl100k tokens. Even at 5 stable pairs / 24 h, that's ~17,500 tokens saved per day in cache hits if the prompt cache is actually warming.

### Hook is the volume play

172 stable pairs in 24 h. The per-pair savings are small (40 bytes / ~10 tokens) but the volume compounds: ~1,700 tokens/day in cache hits if warming.

### Read pairs: 0 stable

Every read has a unique path or range. The prefix shape ASH-108 designed for doesn't help reads — they have no repeating prefix. This is **not** a flaw, just a clarifying finding: the cache work helps verbs that are called with repeating args (help, hook, configs), not verbs whose args are inherently unique per call (read, grep, find with varying paths).

### Git/grep/find stable% is low (0–19%)

Most calls to these verbs vary their args (different paths, different patterns). The bench-corpus stable-pair measurement (ASH-135) over-represented stability by design. In real traffic, the 8% grep stability rate is more representative.

## What we *can* and *cannot* conclude

**Can conclude:**
- The cache-shape changes (ASH-108, ASH-135) produce **416 bytes of shared prefix on average** for stable pairs, vs 8 bytes pre-change. The structural win is real.
- The win is **highly concentrated**: help and hook account for 90%+ of the byte-volume. The rest of the verbs contribute marginally.
- **44% of consecutive-call pairs in 24 h are arg-stable** — enough to make caching worth doing on the *structural* axis. Note: this figure does NOT apply the 5-minute prompt-cache TTL. With the TTL filter applied (the `ash usage` proxy post-ASH-185) the same-args-pair-within-TTL count drops to ~0 on most real sessions. The 44% is "could in principle be a cache hit if the agent re-ran it within 5 minutes," not "actually got cached."

**Cannot conclude:**
- We cannot say what % of those structural overlaps actually land as Anthropic prompt-cache hits **from this sweep alone**. The 5-minute TTL means many won't (sessions don't always call the same verb twice in 5 min). The Stop hook → `turns` ledger plumbing shipped under [ASH-188](https://linear.app/stazelabs/issue/ASH-188) now records real hit rates per assistant turn; after a session or two `ash usage` will show whether the structural overlap converts to billable hits in practice.
- We cannot say the cache work has paid back its design cost. The structural win exists; whether it's load-bearing for real billing is the open question the new telemetry is built to answer.

## Implications for the decision

1. **The cache-shape work is not premature optimization, but it's also not a major value driver alone.** ~17,500 tokens/day saved if help-cache warms is real but small relative to the bench-corpus savings (~15–45 k tokens/day from sweep 2).

2. **The biggest concrete cache win — help responses — argues for promoting help-as-a-prefix more aggressively.** If the harness systematically calls `ash help` before invoking verbs, that's pure cache fodder. Worth investigating whether the existing MCP tool list embeds enough of help to make redundant calls unnecessary, or whether we should be even more explicit about "if you're going to invoke a verb, call help first" as a pattern.

3. **For non-help verbs, the cache work mostly insures against future high-stability-pattern workloads** rather than capturing current ones. If a tool ever wraps ash with a repeated-template workload (e.g. a babysitter that calls `ash report` every 5 min), cache shape is ready. Today it isn't load-bearing.

4. **The biggest blocker to claiming cache value is the lack of cache-hit telemetry.** `ash usage` is a manual-input verb; it cannot scrape harness logs. Either (a) the harness writes back observed cache hit/miss to the daemon via a callback, or (b) we accept structural prefix as a proxy and stop chasing the real-hit number. Option (b) is what we have today and is defensible.

5. **The regression-detection ability of `ash replay`** is independently valuable. 0 result-mismatches across 451 replays is strong evidence the verb surface is stable. The 33 token-volume regressions are mostly workload growth, not behavior changes — we can use this verb as a CI gate against unintentional token-shape regressions.

---

*Provenance: `ash replay --cache_prefix true --since 24h --session all --limit 500` at f35cf2c5 on 2026-05-18.*
