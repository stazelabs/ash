| case | verb | cli_tok | ashmcp_env_tok | harness_env_tok | Δashmcp-vs-cli | Δashmcp-vs-harness |
|---|---|---:|---:|---:|---:|---:|
| `find_go_files` | find | 1440 | 5215 | 1636 | +262% | +218% |
| `find_md_in_docs` | find | 194 | 1045 | 301 | +438% | +247% |
| `find_shallow` | find | 47 | 415 | 113 | +782% | +267% |
| `grep_files_only` | grep | 910 | 987 | 1139 | +8% | -13% |
| `grep_heavy_func_internal` | grep | 5015 | 9092 | 46749 | +81% | -80% |
| `grep_parseargs_internal` | grep | 5985 | 10333 | 10282 | +72% | +0% |
| `grep_rare_pattern` | grep | 88 | 176 | 375 | +100% | -53% |
| `grep_todo_repo` | grep | 1139 | 1556 | 1418 | +36% | +9% |
| `read_range` | read | 739 | 857 | 980 | +15% | -12% |
| `read_small` | read | 6331 | 6747 | 8109 | +6% | -16% |
| `read_tiny_range` | read | 25 | 106 | 51 | +324% | +107% |

**Subset totals (11 cases):** cli 21913 tok, ashmcp_env 36529 tok, harness_env 71153 tok.
* ashmcp vs cli:     **+66%** (envelope tax)
* ashmcp vs harness: **-48%**
