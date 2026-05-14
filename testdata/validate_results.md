# encexplore: cl100k vs Claude (claude-sonnet-4-5) cross-validation

| corpus | sub-set | cl Δ | cl Δ% | claude Δ | claude Δ% | agreement |
|---|---|---:|---:|---:|---:|:---:|
| read-small | errors_ascii | +0 | +0.00% | +0 | +0.00% | — |
| read-small | metrics_short_ascii | +0 | +0.00% | +0 | +0.00% | — |
| read-small | metrics_no_equals | +0 | +0.00% | +0 | +0.00% | — |
| read-small | headers_compact | +0 | +0.00% | +0 | +0.00% | — |
| read-small | combined_aggressive | +0 | +0.00% | +0 | +0.00% | — |
| read-medium | errors_ascii | +0 | +0.00% | +0 | +0.00% | — |
| read-medium | metrics_short_ascii | +0 | +0.00% | +0 | +0.00% | — |
| read-medium | metrics_no_equals | +4 | +0.08% | +8 | +0.14% | ✓ |
| read-medium | headers_compact | +0 | +0.00% | +0 | +0.00% | — |
| read-medium | combined_aggressive | +4 | +0.08% | +8 | +0.14% | ✓ |
| read-large | errors_ascii | +0 | +0.00% | +0 | +0.00% | — |
| read-large | metrics_short_ascii | +0 | +0.00% | +0 | +0.00% | — |
| read-large | metrics_no_equals | +0 | +0.00% | +0 | +0.00% | — |
| read-large | headers_compact | +0 | +0.00% | +0 | +0.00% | — |
| read-large | combined_aggressive | +0 | +0.00% | +0 | +0.00% | — |
| find-shallow | errors_ascii | +0 | +0.00% | +0 | +0.00% | — |
| find-shallow | metrics_short_ascii | +0 | +0.00% | +0 | +0.00% | — |
| find-shallow | metrics_no_equals | +0 | +0.00% | +0 | +0.00% | — |
| find-shallow | headers_compact | +0 | +0.00% | +0 | +0.00% | — |
| find-shallow | combined_aggressive | +0 | +0.00% | +0 | +0.00% | — |
| find-deep-glob | errors_ascii | +0 | +0.00% | +0 | +0.00% | — |
| find-deep-glob | metrics_short_ascii | +0 | +0.00% | +0 | +0.00% | — |
| find-deep-glob | metrics_no_equals | +0 | +0.00% | +0 | +0.00% | — |
| find-deep-glob | headers_compact | +0 | +0.00% | +0 | +0.00% | — |
| find-deep-glob | combined_aggressive | +0 | +0.00% | +0 | +0.00% | — |
| grep-common | errors_ascii | +0 | +0.00% | +0 | +0.00% | — |
| grep-common | metrics_short_ascii | +0 | +0.00% | +0 | +0.00% | — |
| grep-common | metrics_no_equals | +0 | +0.00% | +0 | +0.00% | — |
| grep-common | headers_compact | +0 | +0.00% | +0 | +0.00% | — |
| grep-common | combined_aggressive | +0 | +0.00% | +0 | +0.00% | — |
| grep-error-code | errors_ascii | +0 | +0.00% | +0 | +0.00% | — |
| grep-error-code | metrics_short_ascii | +0 | +0.00% | +0 | +0.00% | — |
| grep-error-code | metrics_no_equals | +0 | +0.00% | +0 | +0.00% | — |
| grep-error-code | headers_compact | +0 | +0.00% | +0 | +0.00% | — |
| grep-error-code | combined_aggressive | +0 | +0.00% | +0 | +0.00% | — |
| git-log | errors_ascii | +0 | +0.00% | +0 | +0.00% | — |
| git-log | metrics_short_ascii | +0 | +0.00% | +0 | +0.00% | — |
| git-log | metrics_no_equals | +0 | +0.00% | +0 | +0.00% | — |
| git-log | headers_compact | +2 | +0.27% | +4 | +0.49% | ✓ |
| git-log | combined_aggressive | +2 | +0.27% | +4 | +0.49% | ✓ |
| git-status | errors_ascii | +0 | +0.00% | +0 | +0.00% | — |
| git-status | metrics_short_ascii | +0 | +0.00% | +0 | +0.00% | — |
| git-status | metrics_no_equals | +0 | +0.00% | +0 | +0.00% | — |
| git-status | headers_compact | +0 | +0.00% | +0 | +0.00% | — |
| git-status | combined_aggressive | +0 | +0.00% | +0 | +0.00% | — |
| metrics-last | errors_ascii | +0 | +0.00% | +0 | +0.00% | — |
| metrics-last | metrics_short_ascii | +0 | +0.00% | +0 | +0.00% | — |
| metrics-last | metrics_no_equals | +0 | +0.00% | +0 | +0.00% | — |
| metrics-last | headers_compact | +0 | +0.00% | +0 | +0.00% | — |
| metrics-last | combined_aggressive | +0 | +0.00% | +0 | +0.00% | — |
| report-session | errors_ascii | +0 | +0.00% | +0 | +0.00% | — |
| report-session | metrics_short_ascii | +0 | +0.00% | +0 | +0.00% | — |
| report-session | metrics_no_equals | +2 | +0.40% | +2 | +0.40% | ✓ |
| report-session | headers_compact | +0 | +0.00% | +0 | +0.00% | — |
| report-session | combined_aggressive | +2 | +0.40% | +2 | +0.40% | ✓ |
| help-all | errors_ascii | +0 | +0.00% | +0 | +0.00% | — |
| help-all | metrics_short_ascii | +0 | +0.00% | +0 | +0.00% | — |
| help-all | metrics_no_equals | +0 | +0.00% | +0 | +0.00% | — |
| help-all | headers_compact | +0 | +0.00% | +0 | +0.00% | — |
| help-all | combined_aggressive | +0 | +0.00% | +0 | +0.00% | — |
| help-find | errors_ascii | +0 | +0.00% | +0 | +0.00% | — |
| help-find | metrics_short_ascii | +0 | +0.00% | +0 | +0.00% | — |
| help-find | metrics_no_equals | +0 | +0.00% | +0 | +0.00% | — |
| help-find | headers_compact | +0 | +0.00% | +0 | +0.00% | — |
| help-find | combined_aggressive | +0 | +0.00% | +0 | +0.00% | — |
| help-grep | errors_ascii | +0 | +0.00% | +0 | +0.00% | — |
| help-grep | metrics_short_ascii | +0 | +0.00% | +0 | +0.00% | — |
| help-grep | metrics_no_equals | +0 | +0.00% | +0 | +0.00% | — |
| help-grep | headers_compact | +0 | +0.00% | +0 | +0.00% | — |
| help-grep | combined_aggressive | +0 | +0.00% | +0 | +0.00% | — |
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
| errors_ascii | 18040 | 18035 | +0.03% | 21557 | 21548 | +0.04% |
| metrics_short_ascii | 18040 | 18040 | +0.00% | 21557 | 21557 | +0.00% |
| metrics_no_equals | 18040 | 18034 | +0.03% | 21557 | 21547 | +0.05% |
| headers_compact | 18040 | 18038 | +0.01% | 21557 | 21553 | +0.02% |
| combined_aggressive | 18040 | 18027 | +0.07% | 21557 | 21534 | +0.11% |
