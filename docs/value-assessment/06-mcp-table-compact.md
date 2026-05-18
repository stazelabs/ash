| case | verb | cli_tok | ashmcp_env_tok | harness_env_tok | Δashmcp-vs-cli | Δashmcp-vs-harness |
|---|---|---:|---:|---:|---:|---:|
| `find_go_files` | find | 1440 | 3981 | 1636 | +176% | +143% |
| `find_md_in_docs` | find | 194 | 834 | 314 | +329% | +165% |
| `find_shallow` | find | 47 | 326 | 113 | +593% | +188% |
| `grep_files_only` | grep | 910 | 77 | 1139 | -91% | -93% |
| `grep_heavy_func_internal` | grep | 5015 | 7245 | 46749 | +44% | -84% |
| `grep_parseargs_internal` | grep | 5985 | 8358 | 10282 | +39% | -18% |
| `grep_rare_pattern` | grep | 88 | 146 | 375 | +65% | -61% |
| `grep_todo_repo` | grep | 1139 | 1320 | 1418 | +15% | -6% |
| `read_range` | read | 739 | 857 | 980 | +15% | -12% |
| `read_small` | read | 6331 | 6747 | 8109 | +6% | -16% |
| `read_tiny_range` | read | 25 | 106 | 51 | +324% | +107% |

**Subset totals (11 cases):** cli 21913 tok, ashmcp_env 29997 tok, harness_env 71166 tok.
* ashmcp vs cli:     **+36%** (envelope tax)
* ashmcp vs harness: **-57%**
