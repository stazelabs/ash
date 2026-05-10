# ash bench — baseline 2026-05-10

ash_version: `0.1.0`  ash_commit: `1ded64de`  case_set: `cs-913b838c885bf357`  repo: `1ded64de` (dirty)

| case | verb | ash_tok | bash_tok | Δtok% | trunc |
|---|---|---:|---:|---:|---|
| `diff_stat_only` | diff | 17 | 9599 | -100% |  |
| `diff_two_files` | diff | 9184 | 9599 | -4% |  |
| `edit_string_replace` | edit | 22 | 0 | +0% |  |
| `find_go_files` | find | 809 | 890 | -9% |  |
| `find_md_in_docs` | find | 773 | 754 | +3% |  |
| `find_shallow` | find | 48 | 72 | -33% |  |
| `git_log_20` | git | 766 | 4719 | -84% |  |
| `git_status` | git | 48 | 96 | -50% |  |
| `grep_files_only` | grep | 685 | 794 | -14% |  |
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

**Overall:** 19 cases, ash 30658 tok, bash 66330 tok, **-53.8%**.

Latency (informational; machine `Mac.attlocal.net`, 11 CPUs, repeat=1, warmup=0):

| case | ash_us_p50 | ash_us_min | bash_us_p50 | bash_us_min |
|---|---:|---:|---:|---:|
| `diff_stat_only` | 177 | 177 | 2956 | 2956 |
| `diff_two_files` | 237 | 237 | 3763 | 3763 |
| `edit_string_replace` | 284 | 284 | 2329 | 2329 |
| `find_go_files` | 1322 | 1322 | 31915 | 31915 |
| `find_md_in_docs` | 218 | 218 | 2271 | 2271 |
| `find_shallow` | 228 | 228 | 3661 | 3661 |
| `git_log_20` | 1300 | 1300 | 34439 | 34439 |
| `git_status` | 7134 | 7134 | 54556 | 54556 |
| `grep_files_only` | 3948 | 3948 | 159509 | 159509 |
| `grep_heavy_func_internal` | 3173 | 3173 | 9398 | 9398 |
| `grep_parseargs_internal` | 5789 | 5789 | 8107 | 8107 |
| `grep_rare_pattern` | 19417 | 19417 | 1644131 | 1644131 |
| `grep_todo_repo` | 6369 | 6369 | 1877722 | 1877722 |
| `read_range` | 27 | 27 | 2435 | 2435 |
| `read_small` | 60 | 60 | 4628 | 4628 |
| `read_tiny_range` | 37 | 37 | 1808 | 1808 |
| `stat_bulk` | 22 | 22 | 6635 | 6635 |
| `stat_single` | 14 | 14 | 5077 | 5077 |
| `write_small` | 177 | 177 | 5085 | 5085 |
