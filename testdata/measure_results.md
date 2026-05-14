# encexplore: substitution measurement results (cl100k_base)

## Aggregate (across the entire corpus)

| sub-set | surface | before | after | Δ tokens | Δ % |
|---|---|---:|---:|---:|---:|
| metrics_no_equals | metrics | 19122 | 18992 | 130 | +0.68% |
| combined_aggressive | combined | 19122 | 19028 | 94 | +0.49% |
| metrics_short_ascii | metrics | 19122 | 19079 | 43 | +0.22% |
| headers_compact | headers | 19122 | 19087 | 35 | +0.18% |
| errors_ascii | errors | 19122 | 19115 | 7 | +0.04% |
| truncation_compact | headers | 19122 | 19119 | 3 | +0.02% |
| errors_cjk | errors | 19122 | 19120 | 2 | +0.01% |
| status_cjk | status | 19122 | 19122 | 0 | +0.00% |

## Per-corpus detail (rows with non-zero Δ)

| corpus | sub-set | before | after | Δ tokens | Δ % |
|---|---|---:|---:|---:|---:|
| err-bad-range | errors_ascii | 17 | 15 | 2 | +11.76% |
| err-bad-range | combined_aggressive | 17 | 15 | 2 | +11.76% |
| err-bad-range | errors_cjk | 17 | 16 | 1 | +5.88% |
| err-not-found | errors_ascii | 17 | 14 | 3 | +17.65% |
| err-not-found | combined_aggressive | 17 | 14 | 3 | +17.65% |
| err-not-found | errors_cjk | 17 | 18 | -1 | -5.88% |
| find-deep-glob | headers_compact | 880 | 878 | 2 | +0.23% |
| find-deep-glob | combined_aggressive | 880 | 878 | 2 | +0.23% |
| find-shallow | headers_compact | 165 | 163 | 2 | +1.21% |
| find-shallow | combined_aggressive | 165 | 163 | 2 | +1.21% |
| git-log | combined_aggressive | 732 | 725 | 7 | +0.96% |
| git-log | truncation_compact | 732 | 729 | 3 | +0.41% |
| git-log | errors_ascii | 732 | 730 | 2 | +0.27% |
| git-log | errors_cjk | 732 | 730 | 2 | +0.27% |
| git-log | headers_compact | 732 | 730 | 2 | +0.27% |
| git-status | headers_compact | 171 | 169 | 2 | +1.17% |
| git-status | combined_aggressive | 171 | 169 | 2 | +1.17% |
| grep-common | combined_aggressive | 5203 | 5198 | 5 | +0.10% |
| grep-common | truncation_compact | 5203 | 5200 | 3 | +0.06% |
| grep-common | headers_compact | 5203 | 5201 | 2 | +0.04% |
| grep-error-code | headers_compact | 245 | 242 | 3 | +1.22% |
| grep-error-code | combined_aggressive | 245 | 242 | 3 | +1.22% |
| help-all | headers_compact | 338 | 336 | 2 | +0.59% |
| help-all | combined_aggressive | 338 | 336 | 2 | +0.59% |
| help-find | headers_compact | 202 | 199 | 3 | +1.49% |
| help-find | combined_aggressive | 202 | 199 | 3 | +1.49% |
| help-grep | headers_compact | 313 | 310 | 3 | +0.96% |
| help-grep | combined_aggressive | 313 | 310 | 3 | +0.96% |
| metrics-last | metrics_no_equals | 756 | 632 | 124 | +16.40% |
| metrics-last | combined_aggressive | 756 | 712 | 44 | +5.82% |
| metrics-last | metrics_short_ascii | 756 | 713 | 43 | +5.69% |
| metrics-last | headers_compact | 756 | 754 | 2 | +0.26% |
| metrics-last | truncation_compact | 756 | 757 | -1 | -0.13% |
| read-large | headers_compact | 2879 | 2873 | 6 | +0.21% |
| read-large | combined_aggressive | 2879 | 2873 | 6 | +0.21% |
| read-medium | combined_aggressive | 4841 | 4836 | 5 | +0.10% |
| read-medium | metrics_no_equals | 4841 | 4837 | 4 | +0.08% |
| read-medium | headers_compact | 4841 | 4839 | 2 | +0.04% |
| read-medium | truncation_compact | 4841 | 4842 | -1 | -0.02% |
| read-small | headers_compact | 792 | 790 | 2 | +0.25% |
| read-small | combined_aggressive | 792 | 790 | 2 | +0.25% |
| report-session | combined_aggressive | 1571 | 1568 | 3 | +0.19% |
| report-session | metrics_no_equals | 1571 | 1569 | 2 | +0.13% |
| report-session | headers_compact | 1571 | 1569 | 2 | +0.13% |
| report-session | truncation_compact | 1571 | 1572 | -1 | -0.06% |
