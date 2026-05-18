| case | verb | ash_tok | bash_tok | harness_tok | Δash-vs-bash | Δash-vs-harness | note |
|---|---|---:|---:|---:|---:|---:|---|
| `diff_stat_only` | diff | 15 | 12154 | n/a | -99% | — | no clean harness equivalent |
| `diff_two_files` | diff | 11562 | 12154 | n/a | -4% | — | no clean harness equivalent |
| `edit_string_replace` | edit | 20 | 0 | n/a | +inf% | — | no clean harness equivalent |
| `find_go_files` | find | 1440 | 1609 | 1609 | -10% | -10% | = bash find (harness Glob returns same paths, mtime-sorted) |
| `find_go_files_absolute` | find | 1440 | 3359 | 3359 | -57% | -57% | = bash find (harness Glob returns same paths, mtime-sorted) |
| `find_md_in_docs` | find | 194 | 188 | 188 | +3% | +3% | = bash find (harness Glob returns same paths, mtime-sorted) |
| `find_shallow` | find | 47 | 91 | 91 | -48% | -48% | = bash find (harness Glob returns same paths, mtime-sorted) |
| `git_log_20` | git | 750 | 8577 | n/a | -91% | — | no clean harness equivalent |
| `git_status` | git | 20 | 39 | n/a | -48% | — | no clean harness equivalent |
| `grep_files_only` | grep | 910 | 1109 | 1109 | -17% | -17% | = bash grep (harness Grep wraps ripgrep, same default format) |
| `grep_heavy_func_internal` | grep | 5015 | 44486 | 44486 | -88% | -88% | = bash grep (harness Grep wraps ripgrep, same default format) |
| `grep_parseargs_absolute` | grep | 5985 | 13175 | 13175 | -54% | -54% | = bash grep (harness Grep wraps ripgrep, same default format) |
| `grep_parseargs_internal` | grep | 5985 | 9424 | 9424 | -36% | -36% | = bash grep (harness Grep wraps ripgrep, same default format) |
| `grep_rare_pattern` | grep | 88 | 314 | 314 | -71% | -71% | = bash grep (harness Grep wraps ripgrep, same default format) |
| `grep_todo_repo` | grep | 1139 | 1306 | 1306 | -12% | -12% | = bash grep (harness Grep wraps ripgrep, same default format) |
| `read_range` | read | 739 | 731 | 926 | +1% | -20% | Read (cat -n format applied to bash output) |
| `read_small` | read | 6331 | 6320 | 7736 | +0% | -18% | Read (cat -n format applied to bash output) |
| `read_tiny_range` | read | 25 | 17 | 34 | +47% | -26% | Read (cat -n format applied to bash output) |
| `stat_bulk` | stat | 30 | 270 | n/a | -88% | — | no clean harness equivalent |
| `stat_single` | stat | 14 | 87 | n/a | -83% | — | no clean harness equivalent |
| `write_small` | write | 18 | 0 | n/a | +inf% | — | no clean harness equivalent |

**Comparable subset (13 cases):** ash 29338 tok, bash 82129 tok, harness 83757 tok.
* ash vs bash:    **-64%**
* ash vs harness: **-64%**
