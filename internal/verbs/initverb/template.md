# ash — agent guidance

This section is managed by `ash init`. Re-running `ash init` refreshes the
content between the `<!-- ash:begin -->` and `<!-- ash:end -->` markers; do
not edit between them. The rest of this file is yours.

## What ash is

`ash` is an agentic shell — a daemon-backed CLI that exposes filesystem,
search, git, and test operations as token-efficient verbs and records every
call to a per-repo SQLite ledger at `.ash/ledger.db`. The PreToolUse hook
installed alongside this section redirects the harness's built-in
`Grep`/`Glob`/`Read`/`Edit`/`Write` tools and several bash commands
(`find`, `grep`, `cat`, `head`, `tail`, `ls -R`, `git status`, `git log`,
`git diff`, `stat`) to their `ash` equivalents.

`ash help` is the authoritative verb list. `ash help --verb <name>` is the
authoritative arg schema. Markdown can drift; help is generated from code.

## When to use ash

Any `ash` invocation auto-starts the daemon. Subsequent calls reuse it over
a per-project Unix domain socket.

1. **Path or filename lookup across more than one directory** — `ash find --path <p> --glob '<pat>' --type file|dir|symlink`. Hidden directories and `.gitignore`d paths are skipped by default.
2. **Pattern search across files** — `ash grep --pattern '<re>' --path <p> --glob '<pat>'`. Smart-case by default; add `--fixed_string true` for literal matches, `--files_only true` for path-only output, `--no_text true` for `path:line:col` rows without excerpts.
3. **Read a file** — `ash read --path <p> [--range start:end]`. Default cap 256 KiB; UTF-8 returned as-is, binary base64-encoded.
4. **Stat paths** — `ash stat --paths a,b,c`. Uses `lstat`. Per-entry errors keep bulk calls alive when some paths are missing.
5. **Diff two files or contents** — `ash diff --path a --other b` or `ash diff --path f --content - < new`. Both inputs capped at 4000 lines.
6. **Write a file** — `ash write --path <p> --content - << 'EOF' … EOF`. Atomic via temp-file + rename.
7. **Edit a file** — `ash edit` with one of: `--old_string`/`--new_string`, `--range start:end --new_content`, or `--patch`. **Default to stdin** for any non-trivial content.
8. **Git status, log, diff, show** — `ash git --op status|log|diff|show`. Other git ops (commit, push, blame, rebase, checkout, etc.) stay in bash.
9. **Run Go tests** — `ash test [--packages <p>] [--run <name>] [--race true] [--short true]`. Failures arrive as a structured slice with `file:line` extracted; build failures land as `Status=build_failed`.
10. **Inspect the ledger** — `ash report` for synthesis, `ash metrics` for raw rows. `ash report --since 1h` is the most common form when a session feels heavy.

## The shell-quoting footgun

`ash edit` and `ash write` accept content via flags, but inline shell
arguments silently corrupt backticks, single quotes, backslashes, escape
sequences, and multiline blocks. **Default to stdin** for any non-trivial
content, in this order:

- Whole-file rewrite: `ash write --path <p> --content - << 'EOF' … EOF`
- Line-range edit: `ash edit --path <p> --range 5:10 --new_content - << 'EOF' … EOF`
- Cross-cutting or multi-region: `ash edit --path <p> --patch - << 'EOF' … EOF`

Inline `--new_content='…'` is for short ASCII-only swaps with no quoting
hazards. For hostile content (heredoc-with-EOF, mixed quotes), write a
Python fixer to `/tmp` via `ash write` and execute it — Python string
concat sidesteps all shell quoting.

## When ash doesn't fit

The hook is best-effort. Some operations stay in bash:

- `git` ops other than `status`, `log`, `diff`, `show`.
- `go build`, `go vet`, system package management, OS-level process management.
- Anything not yet shipped as an `ash` verb. Run `ash help` to check the live list.

If the hook gets in the way of a legitimate operation, run the bash command
anyway. The deny message is a nudge, not a wall.

## Daemon stickiness

Edits to `ash.toml` (jail policy, git backend, daemon limits) take effect
only on daemon restart. Run `ash stop`; the next `ash` invocation auto-starts
a fresh daemon. Don't `pkill ashd` — it bypasses graceful shutdown.

## Reference

- `ash help` — authoritative verb list.
- `ash help --verb <name>` — per-verb arg schema.
- `.ash/ledger.db` — SQLite ledger of every call (one row per invocation).
- The upstream ash project's `CLAUDE.md` and `docs/` for design depth and
  the per-ship session notes that drove these gotchas.
