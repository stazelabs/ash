# ash bench — baseline 2026-05-10

ash_version: `0.1.0`  ash_commit: `4828ebad`  case_set: `cs-913b838c885bf357`  repo: `4828ebad` (dirty)

| case | verb | ash_tok | bash_tok | Δtok% | trunc |
|---|---|---:|---:|---:|---|
| `diff_stat_only` | diff | 17 | 9599 | -100% |  |
| `diff_two_files` | diff | 9184 | 9599 | -4% |  |
| `edit_string_replace` | edit | 22 | 0 | +0% |  |
| `find_go_files` | find | 809 | 890 | -9% |  |
| `find_md_in_docs` | find | 773 | 754 | +3% |  |
| `find_shallow` | find | 48 | 72 | -33% |  |
| `git_log_20` | git | 754 | 4585 | -84% |  |
| `git_status` | git | 100 | 158 | -37% |  |
| `grep_files_only` | grep | 685 | 803 | -15% |  |
| `grep_heavy_func_internal` | grep | 5121 | 25161 | -80% |  |
| `grep_parseargs_internal` | grep | 5156 | 6054 | -15% |  |
| `grep_rare_pattern` | grep | 82 | 276 | -70% |  |
| `grep_todo_repo` | grep | 1183 | 1316 | -10% |  |
| `read_range` | read | 745 | 725 | +3% |  |
| `read_small` | read | 5912 | 5898 | +0% |  |
| `read_tiny_range` | read | 37 | 17 | +118% |  |
| `stat_bulk` | stat | 33 | 273 | -88% |  |
| `stat_single` | stat | 17 | 87 | -80% |  |
| `write_small` | write | 20 | 0 | +0% |  |

**Overall:** 19 cases, ash 30698 tok, bash 66267 tok, **-53.7%**.

Latency (informational; platform `darwin/arm64`, 11 CPUs, repeat=1, warmup=0):

| case | ash_us_p50 | ash_us_min | bash_us_p50 | bash_us_min |
|---|---:|---:|---:|---:|
| `diff_stat_only` | 178 | 178 | 3114 | 3114 |
| `diff_two_files` | 252 | 252 | 3097 | 3097 |
| `edit_string_replace` | 230 | 230 | 2466 | 2466 |
| `find_go_files` | 3562 | 3562 | 30515 | 30515 |
| `find_md_in_docs` | 250 | 250 | 2370 | 2370 |
| `find_shallow` | 732 | 732 | 3100 | 3100 |
| `git_log_20` | 1741 | 1741 | 31506 | 31506 |
| `git_status` | 6307 | 6307 | 40583 | 40583 |
| `grep_files_only` | 3869 | 3869 | 154711 | 154711 |
| `grep_heavy_func_internal` | 3241 | 3241 | 9291 | 9291 |
| `grep_parseargs_internal` | 3856 | 3856 | 8209 | 8209 |
| `grep_rare_pattern` | 18268 | 18268 | 1650022 | 1650022 |
| `grep_todo_repo` | 14008 | 14008 | 1859104 | 1859104 |
| `read_range` | 32 | 32 | 2200 | 2200 |
| `read_small` | 80 | 80 | 2683 | 2683 |
| `read_tiny_range` | 20 | 20 | 1719 | 1719 |
| `stat_bulk` | 19 | 19 | 5961 | 5961 |
| `stat_single` | 20 | 20 | 6407 | 6407 |
| `write_small` | 162 | 162 | 5291 | 5291 |
