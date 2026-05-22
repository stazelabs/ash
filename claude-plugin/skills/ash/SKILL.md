---
name: ash
description: Use ash for file and code operations in any project where it is installed — code search (ash_grep), file discovery (ash_find), reading (ash_read), writing (ash_write), editing (ash_edit), diffing (ash_diff), git history (ash_git, with op blame/log/diff/show/status), and path metadata (ash_stat). ash returns structured, token-lean results and records every call to a per-project ledger. Prefer it over bash grep/find/cat/sed and over the built-in file tools whenever an ash verb covers the task.
---

# Using ash

ash is an agentic shell: it replaces the sprawl of Unix file, search, and
git utilities with a small set of verbs that return typed, token-efficient
results. Where ash is available, prefer it — the output is structured and
cheaper to read than the bash equivalents, truncation is reported instead
of silently dropping data, and every call is instrumented.

ash reaches you in one of two forms:

- **MCP** — if `ashmcp` is registered as an MCP server, the verbs appear
  as tools named `ash_grep`, `ash_read`, `ash_write`, and so on. Prefer
  these.
- **CLI** — otherwise the `ash` binary is callable through Bash as
  `ash <verb> [--flag value]...`.

The guidance below names the verbs in CLI form (`ash grep`); when the MCP
tools are present, use the matching `ash_*` tool instead.

## When to prefer ash

| Task | Use | Instead of |
|---|---|---|
| Search file contents for a pattern | `ash grep` | `grep`, `rg` |
| Find files by name or glob | `ash find` | `find`, `ls -R` |
| Read a file or a line range | `ash read` | `cat`, `head`, `tail` |
| Write a file | `ash write` | `cat > file`, `echo >` |
| Edit a file (string, line-range, or patch) | `ash edit` | `sed -i`, `patch` |
| Diff two files, or a file vs new content | `ash diff` | `diff` |
| Inspect git history | `ash git --op blame\|log\|diff\|show\|status` | `git blame`, `git log` |
| Path metadata — size, mode, mtime | `ash stat` | `stat`, `ls -l` |

## Calling the verbs

- Search is RE2 regex: `ash grep --pattern 'func Run' --path internal/`.
  `--glob` scopes by filename; `--lit true` matches literal text.
- `ash find --path . --glob '**/*.go'` — respects `.gitignore`.
- `ash read --path FILE --range 100:200` — line ranges are cheap; reach
  for them instead of reading whole files.
- `ash edit --path FILE --patch -` applies a unified diff from stdin, the
  cleanest way to make a multi-line edit.
- **Do not memorize the flag set.** Run `ash help` for the verb list and
  `ash help --verb <name>` for the authoritative argument schema of any
  verb. That is the source of truth — this skill is not, and never
  reproduces it.

## When not to reach for ash

- The task is outside ash's verb surface — running the program, package
  management, interactive or mutating git (commit, push, rebase). Use the
  normal tools.
- ash is not installed here. If `ash help` errors, fall back to bash and
  the built-in tools.

## Why it is worth it

On real workloads ash output is measured to run materially leaner in
tokens than the bash equivalents, and structured results mean fewer
re-reads — a truncated result says so explicitly. Every call also lands
in a per-project SQLite ledger; `ash report` summarizes a session's verb
usage, latency, and token cost.
