# ASH-108 envelope reorder — empirical scoreboard (ASH-135)

[ASH-108](https://linear.app/stazelabs/issue/ASH-108) reordered
`proto.Response` so the cache-stable prefix (V, OK, Data, Err) precedes
the volatile suffix (ID, Metrics). The structural claim — that two
consecutive identical-input calls share a long matching prefix suitable
for Anthropic prompt-cache hits — was pinned at the type level
(`TestResponse_VolatileSuffixOrdering`) and at the encoded-bytes level
(`TestResponse_CacheStableWirePrefix`). Both tests rely on synthetic
fixtures. ASH-135 closes the loop with real recorded sessions.

## Methodology

`ash replay --cache_prefix true` re-runs prior ledger calls against the
current build and, when enabled, additionally computes the matching
byte-prefix between consecutive **same-verb** replayed responses. Each
response is encoded twice:

1. **New ordering** — today's `proto.Response`: `V, OK, Data, Err, ID, Metrics`.
2. **Legacy ordering** — a `legacyResponse` struct in
   `internal/verbs/replay/cache_prefix.go` mirroring the pre-ASH-108
   layout: `V, ID, OK, Data, Err, Metrics`.

`replayOne` leaves volatile fields (ID, Metrics) zero — the daemon
populates them after a verb runs, not at dispatch time. To make the A/B
honest the cache-prefix pass injects synthetic, distinct ID and Metrics
values per call (random-shape IDs, slightly different `LatencyExecUs` /
`TokensOut` / `BytesOut`). Without this synthesis the encoded bytes for
two stable-data pairs would be byte-identical and the matching prefix
would equal the entire encoded length — meaningless.

**Gate.** Per-pair prefix is recorded only when the two responses'
`Data` payloads are byte-identical. The cache-prefix question is only
meaningful when the bounded-stable middle is itself stable; world drift
(file contents, git tree) between recorded and replayed calls would
otherwise dominate the metric. `StablePairs / Pairs` in the per-verb
table is the gate's hit rate.

## Results

Run: `ash replay --session all --since 24h --cache_prefix true --limit 1000`
against the live `.ash/ledger.db` on 2026-05-16.

```
cache prefix (ASH-108 A/B, stable pairs):
verb        pairs  stable  enc_len  old_pre  new_pre   Δgain
------------------------------------------------------------
find           46      23     1399        8     1360   +1352
git            62      26      455        8      416    +408
grep           51      11     2384        8     2345   +2337
help           22      11    14130        8    14092  +14084
hook          364     281       94        8       54     +46
metrics         2       0        0        0        0      +0
read          139      10     5014        8     4973   +4965
report          2       1     2390        8     2351   +2343
stat           16      13      139        8      101     +93
usage           1       0        0        0        0      +0
overall       705     376      815        8      775    +767
```

Columns:

- `pairs` — consecutive same-verb pairs considered.
- `stable` — subset where both responses had byte-identical `Data`.
- `enc_len` — average encoded response length across stable pairs.
- `old_pre` — average matching byte-prefix under the legacy ordering.
- `new_pre` — average matching byte-prefix under today's ordering.
- `Δgain` — `new_pre - old_pre`, the per-call cache-prefix win attributable
  to the reorder.

## Interpretation

The headline number is **+767 bytes of cache prefix per call on
average**, across 376 stable-pair samples drawn from a real 24-hour
working window. That's the structural reorder paying off in bytes the
Anthropic prompt cache can latch onto.

The per-verb pattern is the expected one:

- **`help` +14084** is the most dramatic line item: the verb schema dump
  is ~14 KiB and every byte of it sits in the cache-stable region under
  the new layout. Identical `help` invocations cost 10× less the
  second time when prompt caching is active.
- **`read` +4965** is the canonical agent loop — re-reading the same
  file across the conversation is now nearly free after the first read.
- **`grep` +2337**, **`find` +1352**, **`report` +2343** all show that
  search-and-summarize sequences benefit substantially.
- **`hook` +46** is small because the deny payloads themselves are tiny;
  the absolute byte gain matches the encoded length minus the volatile
  tail.
- **`stat` +93** is also small for the same reason.

The legacy ordering collapsed to **8 bytes** of shared prefix in every
verb measured — the msgpack `\x86\xa1v\x02\xa2id\xcf` framing for "the
v key, then the id key" — because `id` was the second field in the
struct and the moment it diverges across calls the prefix ends. This
matches the synthetic test's expectation in `TestEncodeLegacy_PutsVolatileEarly`.

## Why the gate matters

Only **376 / 705 (53%)** of same-verb consecutive pairs in the window
passed the data-stability gate. The rest had different `Data` payloads
between the two replays — almost certainly because the world drifted
since recording (files edited, search results changed). For those
pairs the matching prefix would be noisy: not zero, but a function of
how much of the `Data` happens to share a prefix, not the envelope
ordering.

`read` is the most affected: 139 pairs but only 10 stable. Reads
against this repo churn (the same path can return different bytes a
few minutes apart). `hook` is the inverse: 281 / 364 stable, because
hook responses are tiny deterministic deny records and the path
arguments rarely produce different output across re-invocations.

For verbs that *do* clear the gate, the gain numbers are real: they
reflect what an Anthropic prompt cache would actually be able to hit
on consecutive identical-input calls in this codebase.

## Reproducibility

```sh
ash replay --session all --since 24h --cache_prefix true --limit 1000
```

The numbers above were captured at 2026-05-16. Re-running on a different
ledger window will produce different per-verb counts (and possibly
different per-verb stable-pair ratios as the working tree drifts), but
the `Δgain` column should stay positive and dominated by `help`,
`read`, and `grep` for any session that exercises those verbs more
than once on the same path.
