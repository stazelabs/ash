# ash bench — baseline 2026-05-18

ash_version: `0.1.0`  ash_commit: `f35cf2c5`  case_set: `cs-37ed1eba686fb647`  repo: `f35cf2c5`

| case | verb | ash_tok | bash_tok | Δtok% | trunc |
|---|---|---:|---:|---:|---|
| `diff_stat_only` | diff | 15 | 12154 | -100% |  |
| `diff_two_files` | diff | 11562 | 12154 | -5% |  |
| `edit_string_replace` | edit | 20 | 0 | +0% |  |
| `find_go_files` | find | 1440 | 1609 | -11% |  |
| `find_go_files_absolute` | find | 1440 | 3359 | -57% |  |
| `find_md_in_docs` | find | 194 | 188 | +3% |  |
| `find_shallow` | find | 47 | 91 | -48% |  |
| `git_log_20` | git | 750 | 8577 | -91% |  |
| `git_status` | git | 20 | 39 | -49% |  |
| `grep_files_only` | grep | 910 | 1109 | -18% |  |
| `grep_heavy_func_internal` | grep | 5015 | 44486 | -89% |  |
| `grep_parseargs_absolute` | grep | 5985 | 13175 | -55% |  |
| `grep_parseargs_internal` | grep | 5985 | 9424 | -36% |  |
| `grep_rare_pattern` | grep | 88 | 314 | -72% |  |
| `grep_todo_repo` | grep | 1139 | 1306 | -13% |  |
| `read_range` | read | 739 | 731 | +1% |  |
| `read_small` | read | 6331 | 6320 | +0% |  |
| `read_tiny_range` | read | 25 | 17 | +47% |  |
| `stat_bulk` | stat | 30 | 270 | -89% |  |
| `stat_single` | stat | 14 | 87 | -84% |  |
| `write_small` | write | 18 | 0 | +0% |  |

**Overall:** 21 cases, ash 41767 tok, bash 115410 tok, **-63.8%**.

Latency (informational; platform `darwin/arm64`, 11 CPUs, repeat=5, warmup=2):

| case | ash_us_p50 | ash_us_min | bash_us_p50 | bash_us_min |
|---|---:|---:|---:|---:|
| `diff_stat_only` | 234 | 189 | 2955 | 2609 |
| `diff_two_files` | 276 | 258 | 2771 | 2533 |
| `edit_string_replace` | 222 | 194 | 2550 | 2348 |
| `find_go_files` | 1514 | 1395 | 18880 | 18423 |
| `find_go_files_absolute` | 1576 | 1468 | 18659 | 18190 |
| `find_md_in_docs` | 178 | 149 | 1796 | 1698 |
| `find_shallow` | 219 | 206 | 1773 | 1572 |
| `git_log_20` | 1367 | 1252 | 31612 | 31239 |
| `git_status` | 10167 | 9331 | 42554 | 40186 |
| `grep_files_only` | 7084 | 6276 | 377519 | 361741 |
| `grep_heavy_func_internal` | 3389 | 3224 | 15604 | 13777 |
| `grep_parseargs_absolute` | 6653 | 5622 | 12439 | 12415 |
| `grep_parseargs_internal` | 5422 | 4805 | 11542 | 10617 |
| `grep_rare_pattern` | 37989 | 36385 | 4514641 | 4442153 |
| `grep_todo_repo` | 11791 | 10742 | 4694372 | 4561633 |
| `read_range` | 69 | 47 | 1854 | 1788 |
| `read_small` | 118 | 77 | 2450 | 2018 |
| `read_tiny_range` | 63 | 45 | 1847 | 1713 |
| `stat_bulk` | 69 | 55 | 5928 | 5755 |
| `stat_single` | 52 | 29 | 4128 | 3942 |
| `write_small` | 275 | 274 | 5464 | 4983 |
