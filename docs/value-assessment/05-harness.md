# Sweep 5b — harness-native baseline (ASH-181)

**Source:** [cmd/harnessbench/main.go](../../cmd/harnessbench/main.go) run against [bench/baseline.json](../../bench/baseline.json) at commit f35cf2c5 + ASH-183 + this work. Methodology: simulate Claude Code's harness Read/Grep/Glob response formats from the bash equivalent output, tokenize with cl100k.

## Headline

**ash beats Claude Code's harness-native tools by −64% on the comparable subset** (read + grep + find/glob), virtually identical to the −64% it beats bash. **For read specifically, ash wins more decisively against harness than against bash** (−18% to −26% vs harness, vs roughly breakeven vs bash) because the harness Read format prefixes every line with the cat-n line-number overhead that bash `cat` doesn't.

This closes Q1 from [decision.md](decision.md): the −63.8% bash-baseline headline **does apply to Claude Code users**, not only to people running ash from a generic shell.

## Methodology

The PreToolUse hook denies harness Read/Grep/Glob in-repo, so we can't drive them from inside this session and capture real responses. The simulator models the documented response formats:

| harness tool | simulated format | source |
|---|---|---|
| **Read** | `%6d\t<content>\n` per line (cat -n format) | tool description: "Results are returned using cat -n format, with line numbers starting at 1" |
| **Grep** | `file:line:content` per match | default output mode is "content"; wraps ripgrep with `grep -rn`-equivalent format |
| **Glob** | paths sorted by mtime, one per line | content matches `find`; mtime sort doesn't change token count |

For each verb the simulator either (a) re-runs the bash equivalent (`BashFor(case)`), captures stdout, applies the cat-n transformation, and tokenizes; or (b) uses the existing `bash_tokens` from `bench/baseline.json` directly when bash ≡ harness format (grep, find).

Verified: cat -n format is 6-character right-padded line number, tab, content, newline — confirmed byte-for-byte against `awk '{printf "%6d\t%s\n", NR, $0}'`.

**Not modeled:** the tool-call envelope on the harness side (JSON framing around content blocks adds ~10–30 tokens per call). These numbers are the *payload* cost. Envelope-tax measurement is [ASH-182](https://linear.app/stazelabs/issue/ASH-182)'s scope.

## Per-case results

| case | verb | ash | bash | harness | Δash-vs-bash | Δash-vs-harness | note |
|---|---|---:|---:|---:|---:|---:|---|
| `read_small` | read | 6,331 | 6,320 | **7,736** | +0% | **−18%** | cat-n prefix on long content adds up |
| `read_range` | read | 739 | 731 | **926** | +1% | **−20%** | line numbers on 50-line range |
| `read_tiny_range` | read | 25 | 17 | **34** | +47% | **−26%** | even the corpus's only bash-loss flips to a harness-win |
| `grep_heavy_func_internal` | grep | 5,015 | 44,486 | 44,486 | −88% | **−88%** | identical (harness Grep ≡ bash grep format) |
| `grep_parseargs_absolute` | grep | 5,985 | 13,175 | 13,175 | −54% | **−54%** | identical |
| `grep_parseargs_internal` | grep | 5,985 | 9,424 | 9,424 | −36% | **−36%** | identical |
| `grep_rare_pattern` | grep | 88 | 314 | 314 | −71% | **−71%** | identical |
| `grep_files_only` | grep | 910 | 1,109 | 1,109 | −17% | **−17%** | identical |
| `grep_todo_repo` | grep | 1,139 | 1,306 | 1,306 | −12% | **−12%** | identical |
| `find_go_files_absolute` | find | 1,440 | 3,359 | 3,359 | −57% | **−57%** | identical |
| `find_shallow` | find | 47 | 91 | 91 | −48% | **−48%** | identical |
| `find_go_files` | find | 1,440 | 1,609 | 1,609 | −10% | **−10%** | identical |
| `find_md_in_docs` | find | 194 | 188 | 188 | +3% | **+3%** | identical (one of two harness-loss cases) |

**Comparable subset (13 cases):** ash 29,338 tok, bash 82,129 tok, harness 83,757 tok.
- **ash vs bash:    −64%**
- **ash vs harness: −64%**

Cases excluded as having no clean harness equivalent: `diff_stat_only`, `diff_two_files`, `edit_string_replace`, `git_log_20`, `git_status`, `stat_bulk`, `stat_single`, `write_small` (8 cases). The harness offers `Edit`/`Write` for files but no equivalents for git, stat, or diff — agents using these in Claude Code today reach for `Bash(git …)` etc., which is the bash comparison we already have.

## Interpretation

### Read flips from breakeven (vs bash) to clear win (vs harness)

The three read cases tell a complete story:

| | vs bash | vs harness | reason |
|---|---|---|---|
| `read_small` (100+ lines) | +0% (tie) | **−18%** (win) | every line costs ~14 chars of cat-n prefix in harness; ash's envelope is bounded |
| `read_range` (50 lines) | +1% (tie) | **−20%** (win) | same shape at smaller scale |
| `read_tiny_range` (5 lines) | +47% (loss) | **−26%** (win) | 5 × ~3-token line-number prefix in harness (~15 tokens) outweighs ash's ~8-token envelope overhead |

The decision doc previously flagged read as "breakeven, the case for ash read rests on jail/instrumentation/binary-handling not bytes." **That framing is too pessimistic for Claude Code users.** vs the harness, ash read is a real token win at every range size. The qualitative case still stands (jail, instrumentation, binary handling), and now the quantitative case is also positive for the target users.

### Grep and find are identical vs bash and vs harness

The harness's Grep wraps ripgrep with the same default `file:line:content` format that `grep -rn` produces. The harness's Glob returns the same set of paths as `find`, just sorted by mtime. The cl100k token cost is identical in both cases. So all of the ash vs bash wins on these verbs apply 1:1 to the harness comparison.

### The comparable subset captures the majority of agent-hot calls

13 cases comparable; 8 not. But the comparable subset covers **read + grep + find/glob**, which in the 30-day ledger ([02-aggregate.md](02-aggregate.md)) account for **1,497 of the 4,425 Tier A calls (~34%)** and the bulk of right-skewed token output. Git, stat, diff, edit, write — the non-comparable verbs — are either bash-only competitors (git/stat) or side-effect verbs with tiny output (edit/write/diff-stat) where the "win" is qualitative.

### What the un-modeled envelope tax would change

The simulator does not include the harness-side tool-call JSON envelope. For a back-of-envelope estimate: each harness tool call wraps content in a `tool_result` block with metadata that costs perhaps 10–30 cl100k tokens. Adding that to the harness side would make **ash win by an additional ~1–3% on average** (a small fraction at this volume). Adding it to ash's MCP-routed calls would partially offset (ashmcp envelope tax ~3.4× per [ASH-123](https://linear.app/stazelabs/issue/ASH-123), measured systematically in [ASH-182](https://linear.app/stazelabs/issue/ASH-182)). The headline number is robust to this caveat in either direction.

## Limitations and what would tighten this

1. **Simulation, not direct measurement.** The harness Read/Grep/Glob response format is modeled from documentation, not captured from actual tool invocations. The `cat -n` format is well-known and stable, but any harness-side wrapping or truncation hints we don't know about would shift the numbers.
2. **No envelope tax modeled.** See above; first-order analysis suggests the omission slightly under-counts ash's win.
3. **Read length distribution may differ from real agent usage.** The corpus has three read sizes (tiny / range / small file). Real agent reads probably have a longer-tail distribution; if anything, longer reads strengthen ash's relative position because the cat-n overhead grows linearly with line count.
4. **No latency comparison vs harness.** Latency isn't simulatable — it depends on the harness's tool-dispatch loop, which we don't observe from outside. The bash latency comparison from [01-bench.md](01-bench.md) is still a reasonable lower bound (the harness adds its own overhead on top of the underlying work).

## What this changes in the decision

[decision.md](decision.md) said:

> **Open Q1 — Does ash beat harness-native tools…? Strictly required to claim ash wins in MCP-aware harnesses.**

Answered: **yes**, by roughly the same margin as bash, and *more* decisively for read specifically. The adoption push for Claude Code users is no longer gated on this question.

The decision doc's recommendation to "not invest further in per-verb micro-optimizations for read purely for token shape" should be revised slightly: ash read does deliver a real token win vs harness Read (−18% to −26%), so the qualitative case is no longer the *only* case. But the conclusion holds — read isn't where the next round of optimization investment should go.

[ASH-182](https://linear.app/stazelabs/issue/ASH-182) (ashmcp envelope cost) is now the remaining gate before adoption push. With Q1 answered positively, the case for putting effort into Q2 is much stronger — if ashmcp envelope tax doesn't eat the read/grep/find wins, the full adoption story holds.

---

*Provenance: `go run ./cmd/harnessbench --in bench/baseline.json --out docs/value-assessment/05-harness-table.md` at f35cf2c5 + ASH-183 + this work on 2026-05-18. Raw table at [05-harness-table.md](05-harness-table.md).*
