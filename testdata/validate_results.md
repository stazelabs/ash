# encexplore: cl100k vs Claude (claude-sonnet-4-5) cross-validation

| corpus | sub-set | cl Δ | cl Δ% | claude Δ | claude Δ% | agreement |
|---|---|---:|---:|---:|---:|:---:|
| read-small | errors_ascii | +0 | +0.00% | +0 | +0.00% | — |
| read-small | metrics_short_ascii | +0 | +0.00% | +0 | +0.00% | — |
| read-small | metrics_no_equals | +0 | +0.00% | +0 | +0.00% | — |
| read-small | headers_compact | +2 | +0.25% | +2 | +0.21% | ✓ |
| read-small | combined_aggressive | +2 | +0.25% | +2 | +0.21% | ✓ |
| read-medium | errors_ascii | +0 | +0.00% | +0 | +0.00% | — |
| read-medium | metrics_short_ascii | +0 | +0.00% | +0 | +0.00% | — |
| read-medium | metrics_no_equals | +4 | +0.08% | +8 | +0.14% | ✓ |
| read-medium | headers_compact | +2 | +0.04% | +2 | +0.04% | ✓ |
| read-medium | combined_aggressive | +5 | +0.10% | +8 | +0.14% | ✓ |
| read-large | errors_ascii | +0 | +0.00% | +0 | +0.00% | — |
| read-large | metrics_short_ascii | +0 | +0.00% | +0 | +0.00% | — |
| read-large | metrics_no_equals | +0 | +0.00% | +0 | +0.00% | — |
| read-large | headers_compact | +6 | +0.21% | +10 | +0.30% | ✓ |
| read-large | combined_aggressive | +6 | +0.21% | +10 | +0.30% | ✓ |
| find-shallow | errors_ascii | +0 | +0.00% | +0 | +0.00% | — |
| find-shallow | metrics_short_ascii | +0 | +0.00% | +0 | +0.00% | — |
| find-shallow | metrics_no_equals | +0 | +0.00% | +0 | +0.00% | — |
| find-shallow | headers_compact | +2 | +1.21% | +3 | +1.42% | ✓ |
| find-shallow | combined_aggressive | +2 | +1.21% | +3 | +1.42% | ✓ |
| find-deep-glob | errors_ascii | +0 | +0.00% | +0 | +0.00% | — |
| find-deep-glob | metrics_short_ascii | +0 | +0.00% | +0 | +0.00% | — |
| find-deep-glob | metrics_no_equals | +0 | +0.00% | +0 | +0.00% | — |
| find-deep-glob | headers_compact | +2 | +0.23% | +3 | +0.24% | ✓ |
| find-deep-glob | combined_aggressive | +2 | +0.23% | +3 | +0.24% | ✓ |
| grep-common | errors_ascii | +0 | +0.00% | +0 | +0.00% | — |
| grep-common | metrics_short_ascii | +0 | +0.00% | +0 | +0.00% | — |
| grep-common | metrics_no_equals | +0 | +0.00% | +0 | +0.00% | — |
| grep-common | headers_compact | +2 | +0.04% | +3 | +0.05% | ✓ |
| grep-common | combined_aggressive | +5 | +0.10% | +3 | +0.05% | ✓ |
| grep-error-code | errors_ascii | +0 | +0.00% | +0 | +0.00% | — |
| grep-error-code | metrics_short_ascii | +0 | +0.00% | +0 | +0.00% | — |
| grep-error-code | metrics_no_equals | +0 | +0.00% | +0 | +0.00% | — |
| grep-error-code | headers_compact | +3 | +1.22% | +3 | +0.91% | ✓ |
| grep-error-code | combined_aggressive | +3 | +1.22% | +3 | +0.91% | ✓ |
| git-log | errors_ascii | +2 | +0.27% | +1 | +0.13% | ✓ |
| git-log | metrics_short_ascii | +0 | +0.00% | +0 | +0.00% | — |
| git-log | metrics_no_equals | +0 | +0.00% | +0 | +0.00% | — |
| git-log | headers_compact | +2 | +0.27% | +3 | +0.38% | ✓ |
| git-log | combined_aggressive | +7 | +0.96% | +4 | +0.50% | ✓ |
| git-status | errors_ascii | +0 | +0.00% | +0 | +0.00% | — |
| git-status | metrics_short_ascii | +0 | +0.00% | +0 | +0.00% | — |
| git-status | metrics_no_equals | +0 | +0.00% | +0 | +0.00% | — |
| git-status | headers_compact | +2 | +1.17% | +3 | +1.43% | ✓ |
| git-status | combined_aggressive | +2 | +1.17% | +3 | +1.43% | ✓ |
| metrics-last | errors_ascii | +0 | +0.00% | +0 | +0.00% | — |
| metrics-last | metrics_short_ascii | +43 | +5.69% | +86 | +10.40% | ✓ |
| metrics-last | metrics_no_equals | +124 | +16.40% | +167 | +20.19% | ✓ |
| metrics-last | headers_compact | +2 | +0.26% | +3 | +0.36% | ✓ |
| metrics-last | combined_aggressive | +44 | +5.82% | +88 | +10.64% | ✓ |
| report-session | errors_ascii | +0 | +0.00% | +0 | +0.00% | — |
| report-session | metrics_short_ascii | +0 | +0.00% | +0 | +0.00% | — |
| report-session | metrics_no_equals | +2 | +0.13% | +2 | +0.12% | ✓ |
| report-session | headers_compact | +2 | +0.13% | +3 | +0.18% | ✓ |
| report-session | combined_aggressive | +3 | +0.19% | +4 | +0.25% | ✓ |
| help-all | errors_ascii | +0 | +0.00% | +0 | +0.00% | — |
| help-all | metrics_short_ascii | +0 | +0.00% | +0 | +0.00% | — |
| help-all | metrics_no_equals | +0 | +0.00% | +0 | +0.00% | — |
| help-all | headers_compact | +2 | +0.59% | +3 | +0.75% | ✓ |
| help-all | combined_aggressive | +2 | +0.59% | +3 | +0.75% | ✓ |
| help-find | errors_ascii | +0 | +0.00% | +0 | +0.00% | — |
| help-find | metrics_short_ascii | +0 | +0.00% | +0 | +0.00% | — |
| help-find | metrics_no_equals | +0 | +0.00% | +0 | +0.00% | — |
| help-find | headers_compact | +3 | +1.49% | +3 | +1.21% | ✓ |
| help-find | combined_aggressive | +3 | +1.49% | +3 | +1.21% | ✓ |
| help-grep | errors_ascii | +0 | +0.00% | +0 | +0.00% | — |
| help-grep | metrics_short_ascii | +0 | +0.00% | +0 | +0.00% | — |
| help-grep | metrics_no_equals | +0 | +0.00% | +0 | +0.00% | — |
| help-grep | headers_compact | +3 | +0.96% | +3 | +0.79% | ✓ |
| help-grep | combined_aggressive | +3 | +0.96% | +3 | +0.79% | ✓ |
| err-not-found | errors_ascii | +3 | +17.65% | +4 | +13.79% | ✓ |
| err-not-found | metrics_short_ascii | +0 | +0.00% | +0 | +0.00% | — |
| err-not-found | metrics_no_equals | +0 | +0.00% | +0 | +0.00% | — |
| err-not-found | headers_compact | +0 | +0.00% | +0 | +0.00% | — |
| err-not-found | combined_aggressive | +3 | +17.65% | +4 | +13.79% | ✓ |
| err-bad-range | errors_ascii | +2 | +11.76% | +5 | +18.52% | ✓ |
| err-bad-range | metrics_short_ascii | +0 | +0.00% | +0 | +0.00% | — |
| err-bad-range | metrics_no_equals | +0 | +0.00% | +0 | +0.00% | — |
| err-bad-range | headers_compact | +0 | +0.00% | +0 | +0.00% | — |
| err-bad-range | combined_aggressive | +2 | +11.76% | +5 | +18.52% | ✓ |

## Aggregate by sub-set

| sub-set | cl before | cl after | cl Δ% | claude before | claude after | claude Δ% |
|---|---:|---:|---:|---:|---:|---:|
| errors_ascii | 19122 | 19115 | +0.04% | 22784 | 22774 | +0.04% |
| metrics_short_ascii | 19122 | 19079 | +0.22% | 22784 | 22698 | +0.38% |
| metrics_no_equals | 19122 | 18992 | +0.68% | 22784 | 22607 | +0.78% |
| headers_compact | 19122 | 19087 | +0.18% | 22784 | 22737 | +0.21% |
| combined_aggressive | 19122 | 19028 | +0.49% | 22784 | 22635 | +0.65% |
