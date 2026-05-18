| case | verb | cli_tok | ashmcp_env_tok | harness_env_tok | Δashmcp-vs-cli | Δashmcp-vs-harness |
|---|---|---:|---:|---:|---:|---:|
| `find_go_files` | find | 1440 | 1545 | 1636 | +7% | -5% |
| `find_md_in_docs` | find | 194 | 347 | 301 | +78% | +15% |
| `find_shallow` | find | 47 | 108 | 113 | +129% | -4% |
| `grep_files_only` | grep | 910 | 90 | 1139 | -90% | -92% |
| `grep_heavy_func_internal` | grep | 5015 | 5451 | 46749 | +8% | -88% |
| `grep_parseargs_internal` | grep | 5985 | 6607 | 10282 | +10% | -35% |
| `grep_rare_pattern` | grep | 88 | 167 | 375 | +89% | -55% |
| `grep_todo_repo` | grep | 1139 | 1313 | 1418 | +15% | -7% |
| `read_range` | read | 739 | 836 | 980 | +13% | -14% |
| `read_small` | read | 6331 | 6729 | 8109 | +6% | -17% |
| `read_tiny_range` | read | 25 | 85 | 51 | +240% | +66% |

**Subset totals (11 cases):** cli 21913 tok, ashmcp_env 23278 tok, harness_env 71153 tok.
* ashmcp vs cli:     **+6%** (envelope tax)
* ashmcp vs harness: **-67%**
