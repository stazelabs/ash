# wirecmp: CLI vs MCP wire cost

Same intent, two transports. CLI = daemon-pretty render; MCP = JSON envelope ashmcp emits as TextContent. Both renders are computed from a single daemon roundtrip per fixture; latency is the median of `-repeat` trials per transport.

| fixture | CLI bytes | CLI cl100k | CLI claude | MCP bytes | MCP cl100k | MCP claude | Δ bytes | Δ cl100k | Δ claude | CLI p50 | MCP p50 |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| read README:1-60 | 4829 | 1101 | 1221 | 5106 | 1224 | 1363 | +277 (+6%) | +123 (+11%) | +142 (+12%) | 1.5ms | 2.2ms |
| find **/*.go (20) | 41 | 9 | 16 | 142 | 50 | 63 | +101 (+246%) | +41 (+456%) | +47 (+294%) | 1.2ms | 989µs |
| grep ^func Run | 39 | 9 | 16 | 139 | 50 | 63 | +100 (+256%) | +41 (+456%) | +47 (+294%) | 962µs | 899µs |
| stat README.md | 35 | 14 | 26 | 200 | 74 | 90 | +165 (+471%) | +60 (+429%) | +64 (+246%) | 1.5ms | 1.6ms |
| git status | 318 | 102 | 135 | 603 | 182 | 229 | +285 (+90%) | +80 (+78%) | +94 (+70%) | 8.6ms | 7.1ms |
| help | 1349 | 336 | 399 | 16183 | 3761 | 4339 | +14834 (+1100%) | +3425 (+1019%) | +3940 (+987%) | 1.4ms | 4.7ms |

**Totals** — CLI 6611B / 1571 cl100k, MCP 22373B / 5341 cl100k. Δ +15762B (+238.4%) / +3770 cl100k tokens (+240.0%).
Claude: CLI 1813, MCP 6147, Δ +4334 (+239.1%).
