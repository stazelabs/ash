# ash bench — baseline 2026-05-10

ash_version: `0.1.0`  ash_commit: `924dcba2`  case_set: `cs-c3a63e2b5fce05fa`  repo: `924dcba2` (dirty)

| case | verb | ash_tok | bash_tok | Δtok% | trunc |
|---|---|---:|---:|---:|---|
| `diff_stat_only` | diff | 17 | 10330 | -100% |  |
| `diff_two_files` | diff | 9907 | 10330 | -4% |  |
| `edit_string_replace` | edit | 22 | 0 | +0% |  |
| `find_go_files` | find | 795 | 874 | -9% |  |
| `find_md_in_docs` | find | 773 | 754 | +3% |  |
| `find_shallow` | find | 46 | 62 | -26% |  |
| `git_log_20` | git | 766 | 6425 | -88% |  |
| `git_status` | git | 226 | 312 | -28% |  |
| `grep_files_only` | grep | 677 | 778 | -13% |  |
| `grep_heavy_func_internal` | grep | 5126 | 24866 | -79% |  |
| `grep_parseargs_internal` | grep | 5175 | 6073 | -15% |  |
| `grep_rare_pattern` | grep | 82 | 263 | -69% |  |
| `grep_todo_repo` | grep | 1183 | 1303 | -9% |  |
| `read_range` | read | 745 | 725 | +3% |  |
| `read_small` | read | 5549 | 5535 | +0% |  |
| `read_tiny_range` | read | 37 | 17 | +118% |  |
| `stat_bulk` | stat | 33 | 276 | -88% |  |
| `stat_single` | stat | 17 | 90 | -81% |  |
| `write_small` | write | 20 | 0 | +0% |  |

**Overall:** 19 cases, ash 31196 tok, bash 69013 tok, **-54.8%**.

Latency (informational; machine `Mac.attlocal.net`, 11 CPUs, repeat=1, warmup=0):

| case | ash_us_p50 | ash_us_min | bash_us_p50 | bash_us_min |
|---|---:|---:|---:|---:|
| `diff_stat_only` | 137 | 137 | 2653 | 2653 |
| `diff_two_files` | 256 | 256 | 3271 | 3271 |
| `edit_string_replace` | 251 | 251 | 2427 | 2427 |
| `find_go_files` | 2850 | 2850 | 31736 | 31736 |
| `find_md_in_docs` | 204 | 204 | 2007 | 2007 |
| `find_shallow` | 587 | 587 | 2773 | 2773 |
| `git_log_20` | 1252 | 1252 | 29687 | 29687 |
| `git_status` | 6187 | 6187 | 36429 | 36429 |
| `grep_files_only` | 3658 | 3658 | 94823 | 94823 |
| `grep_heavy_func_internal` | 3124 | 3124 | 9394 | 9394 |
| `grep_parseargs_internal` | 3745 | 3745 | 7329 | 7329 |
| `grep_rare_pattern` | 17025 | 17025 | 811054 | 811054 |
| `grep_todo_repo` | 22151 | 22151 | 943634 | 943634 |
| `read_range` | 27 | 27 | 1948 | 1948 |
| `read_small` | 73 | 73 | 2466 | 2466 |
| `read_tiny_range` | 30 | 30 | 1542 | 1542 |
| `stat_bulk` | 17 | 17 | 5970 | 5970 |
| `stat_single` | 13 | 13 | 5133 | 5133 |
| `write_small` | 177 | 177 | 5677 | 5677 |
