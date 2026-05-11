# ash bench — baseline 2026-05-11

ash_version: `0.1.0`  ash_commit: `a2510969`  case_set: `cs-37ed1eba686fb647`  repo: `a2510969` (dirty)

| case | verb | ash_tok | bash_tok | Δtok% | trunc |
|---|---|---:|---:|---:|---|
| `diff_stat_only` | diff | 17 | 9599 | -100% |  |
| `diff_two_files` | diff | 9184 | 9599 | -4% |  |
| `edit_string_replace` | edit | 22 | 0 | +0% |  |
| `find_go_files` | find | 834 | 929 | -10% |  |
| `find_go_files_absolute` | find | 834 | 1959 | -57% |  |
| `find_md_in_docs` | find | 808 | 800 | +1% |  |
| `find_shallow` | find | 44 | 72 | -39% |  |
| `git_log_20` | git | 734 | 6785 | -89% |  |
| `git_status` | git | 87 | 141 | -38% |  |
| `grep_files_only` | grep | 732 | 856 | -14% |  |
| `grep_heavy_func_internal` | grep | 4993 | 27985 | -82% |  |
| `grep_parseargs_absolute` | grep | 5882 | 9759 | -40% |  |
| `grep_parseargs_internal` | grep | 5882 | 6976 | -16% |  |
| `grep_rare_pattern` | grep | 82 | 276 | -70% |  |
| `grep_todo_repo` | grep | 1183 | 1326 | -11% |  |
| `read_range` | read | 735 | 725 | +1% |  |
| `read_small` | read | 5912 | 5898 | +0% |  |
| `read_tiny_range` | read | 27 | 17 | +59% |  |
| `stat_bulk` | stat | 33 | 273 | -88% |  |
| `stat_single` | stat | 17 | 87 | -80% |  |
| `write_small` | write | 20 | 0 | +0% |  |

**Overall:** 21 cases, ash 38062 tok, bash 84062 tok, **-54.7%**.

Latency (informational; platform `darwin/arm64`, 11 CPUs, repeat=1, warmup=0):

| case | ash_us_p50 | ash_us_min | bash_us_p50 | bash_us_min |
|---|---:|---:|---:|---:|
| `diff_stat_only` | 172 | 172 | 2974 | 2974 |
| `diff_two_files` | 273 | 273 | 2961 | 2961 |
| `edit_string_replace` | 239 | 239 | 2125 | 2125 |
| `find_go_files` | 3591 | 3591 | 18863 | 18863 |
| `find_go_files_absolute` | 3137 | 3137 | 14649 | 14649 |
| `find_md_in_docs` | 287 | 287 | 2484 | 2484 |
| `find_shallow` | 665 | 665 | 2805 | 2805 |
| `git_log_20` | 1266 | 1266 | 30133 | 30133 |
| `git_status` | 6795 | 6795 | 39498 | 39498 |
| `grep_files_only` | 4145 | 4145 | 163971 | 163971 |
| `grep_heavy_func_internal` | 3144 | 3144 | 9932 | 9932 |
| `grep_parseargs_absolute` | 4644 | 4644 | 9832 | 9832 |
| `grep_parseargs_internal` | 4575 | 4575 | 9273 | 9273 |
| `grep_rare_pattern` | 19779 | 19779 | 1637361 | 1637361 |
| `grep_todo_repo` | 20143 | 20143 | 1805275 | 1805275 |
| `read_range` | 49 | 49 | 3042 | 3042 |
| `read_small` | 132 | 132 | 2305 | 2305 |
| `read_tiny_range` | 39 | 39 | 1641 | 1641 |
| `stat_bulk` | 56 | 56 | 6072 | 6072 |
| `stat_single` | 55 | 55 | 5203 | 5203 |
| `write_small` | 211 | 211 | 4860 | 4860 |
