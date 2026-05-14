# encexplore: substitution measurement results (cl100k_base)

## Aggregate (across the entire corpus)

| sub-set | surface | before | after | Δ tokens | Δ % |
|---|---|---:|---:|---:|---:|
| combined_aggressive | combined | 18040 | 18023 | 17 | +0.09% |
| metrics_no_equals | metrics | 18040 | 18034 | 6 | +0.03% |
| errors_ascii | errors | 18040 | 18035 | 5 | +0.03% |
| truncation_compact | headers | 18040 | 18036 | 4 | +0.02% |
| headers_compact | headers | 18040 | 18038 | 2 | +0.01% |
| metrics_short_ascii | metrics | 18040 | 18040 | 0 | +0.00% |
| status_cjk | status | 18040 | 18040 | 0 | +0.00% |
| read_header_compact | headers | 18040 | 18040 | 0 | +0.00% |
| errors_cjk | errors | 18040 | 18040 | 0 | +0.00% |

## Per-corpus detail (rows with non-zero Δ)

| corpus | sub-set | before | after | Δ tokens | Δ % |
|---|---|---:|---:|---:|---:|
| err-bad-range | errors_ascii | 17 | 15 | 2 | +11.76% |
| err-bad-range | combined_aggressive | 17 | 15 | 2 | +11.76% |
| err-bad-range | errors_cjk | 17 | 16 | 1 | +5.88% |
| err-not-found | errors_ascii | 17 | 14 | 3 | +17.65% |
| err-not-found | combined_aggressive | 17 | 14 | 3 | +17.65% |
| err-not-found | errors_cjk | 17 | 18 | -1 | -5.88% |
| git-log | combined_aggressive | 751 | 746 | 5 | +0.67% |
| git-log | truncation_compact | 751 | 748 | 3 | +0.40% |
| git-log | headers_compact | 751 | 749 | 2 | +0.27% |
| grep-common | truncation_compact | 5201 | 5198 | 3 | +0.06% |
| grep-common | combined_aggressive | 5201 | 5198 | 3 | +0.06% |
| read-medium | metrics_no_equals | 4838 | 4834 | 4 | +0.08% |
| read-medium | combined_aggressive | 4838 | 4835 | 3 | +0.06% |
| read-medium | truncation_compact | 4838 | 4839 | -1 | -0.02% |
| report-session | metrics_no_equals | 504 | 502 | 2 | +0.40% |
| report-session | combined_aggressive | 504 | 503 | 1 | +0.20% |
| report-session | truncation_compact | 504 | 505 | -1 | -0.20% |
