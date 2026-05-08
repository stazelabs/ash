# CLAUDE.md — agent guidance for the ash repo

This file is the operational counterpart to `README.md`. The README is the design; this is how an agent works *inside* the project. Keep it short, keep it current, and update it as the surface evolves.

## Project context

`ash` is an agentic shell for coding agents — a small, structured, instrumented command surface designed to replace the bash-shaped substrate that agents currently drive. The README has the full pitch.

This repo is also a deliberate experiment in *recursive* tool development: as soon as primordial `ash` exists (Phase 1: `find` + `grep` + `read` + daemon + bash shim + ledger), agents working on this repo start using `ash` for those operations. Every session feeds the next phase's design.

**Current phase:** Phase 2, ship 9 — `ash read`, `ash find`, `ash grep`, `ash git --op status|log|diff`, `ash metrics`, `ash report`, `ash stat`, `ash bench`, and `ash write` are live. Daemon (`ashd`) auto-starts on first invocation, persists per-call instrumentation to a SQLite ledger at `.ash/ledger.db`, and tokenizes every response with `cl100k_base` for honest token counts. Sub-phase latency (walk/io/regex µs) is captured in `Metrics.Phases` and surfaces in `ash metrics`. Ledger failures surface in `Metrics.LedgerError` and the client prints a `WARNING` line if any call's metrics didn't persist.

## Project constraints

These are hard rules. If a change would violate one, stop and discuss before proceeding.

- **No CGO.** All dependencies must be pure Go. We're prioritizing portability — every developer should be able to clone, `go build`, and run, on any platform Go cross-compiles to, with no native toolchain required. This rules out `mattn/go-sqlite3`, CGO-bound tree-sitter, and similar. We've already paid the perf cost on SQLite (`modernc.org/sqlite`) and accept it.
- **All paths are explicit and absolute-friendly.** No verb relies on `cwd` for path resolution beyond the daemon's project root. The agent passes the full path it cares about; the daemon canonicalizes and validates. Beyond removing a class of mistakes, this gives us a "sandbox-lite" hook for free: a verb can refuse paths outside the project root, reject symlink-escapes, or apply per-path policy *before* the verb body runs. We're not a sandbox today, but we are deliberately keeping the affordance open.

## When to prefer ash over bash

This is the operational checklist. It is **gated by which verbs are live**. Do not try to invoke a verb that hasn't shipped yet.

### Phase 2 ship 9 (now) — `read`, `find`, `grep`, `git status/log/diff`, `metrics`, `report`, `stat`, `bench`, and `write` are live

Build the binaries first (one-time per session, cheap to redo):

```sh
go build -o bin/ash ./cmd/ash
go build -o bin/ashd ./cmd/ashd
```

Then any `ash` invocation auto-starts the daemon. Use `bin/ash` from the repo root, or add `bin/` to `$PATH` for the session.

**Switch criteria — what to actually use ash for now:**

1. **Any path or filename lookup across more than one directory** — use `ash find`. Examples:
   - "list all .go files under cmd/" → `ash find --path cmd --glob '**/*.go' --type file`
   - "show top-level files only" → `ash find --path . --max_depth 1`
   - "find anything related to ledger" → `ash find --path . --glob '**/*ledger*'`
   - Hidden directories (`.git`, `.ash`, `.vscode`) are skipped by default. Pass `--include_hidden true` if you actually want them.
   - `.gitignore` at the walk root is respected by default. Pass `--respect_gitignore false` for a raw walk (e.g. when you genuinely need to inspect generated artifacts in `bin/` or `dist/`).
2. **Any pattern search across one or more files** — use `ash grep`. Examples:
   - "where is `ParseArgs` called?" → `ash grep --pattern 'ParseArgs' --path . --glob '**/*.go'`
   - "find TODO/FIXME notes" → `ash grep --pattern 'TODO|FIXME' --path .`
   - "literal string with regex metacharacters" → `ash grep --pattern 'cfg.Foo()' --path . --fixed_string true`
   - "just the file list (e.g. for `ash read` follow-up)" → add `--files_only true`
   - "with surrounding lines" → `--context_before 2 --context_after 2`
   - Smart-case is the default: an all-lowercase pattern is matched case-insensitively; any uppercase letter switches to case-sensitive. Override with `--case sensitive` or `--case insensitive`.
   - Binary files (NUL byte in the leading 8 KiB) and files >16 MiB are skipped silently; the response counts them so you know why a hit didn't appear.
   - Same hidden-dir and `.gitignore` defaults as `find`.
3. **`git status` and `git log` in this repo** — use `ash git --op status|log` instead of bash. Examples:
   - "what's the working-tree state?" → `ash git --op status`
   - "include ignored files (e.g. inspecting `.ash/`)" → `ash git --op status --ignored true`
   - "last 20 commits" → `ash git --op log` (default `--limit 20`)
   - "commits in a range" → `ash git --op log --range HEAD~10..HEAD`
   - "filter by author" → `ash git --op log --author Chris`
   - "by date / by path" → `--since '1 week ago'` / `--pathspec internal/walker/`
   - Status splits changes into `staged`/`unstaged`/`untracked`/`conflicts`; a file with index AND worktree changes appears in both. Log emits structured `Commit` records (full+short SHA, author/committer name+email+time-as-unix-nanos, parents, subject, body) — the body is preserved with embedded newlines intact.
   - Other git ops (diff, blame, show, commit/push/reset/…) stay in bash until those ops ship.
4. **Reads of files in this repo** — use `ash read` deliberately at least a few times per session, even when the harness Read tool would suffice. We need ledger data to compare.
5. **Ledger queries** — use `ash metrics` for raw rows or `ash report` for aggregated synthesis. `ash report` (default: current session) gives per-verb n/ok%/p50-p95 latency/truncation rate at a glance — reach for it first when a session feels heavy. Sub-phase columns (`walk_us`, `io_us`, `regex_us`) appear in `ash metrics` rows when the verb instrumented them.
6. **Filesystem metadata for one or more explicit, known paths** — use `ash stat`. Examples:
   - "what's the size and mtime of this file?" → `ash stat --paths cmd/ash/main.go`
   - "bulk metadata before deciding what to read" → `ash stat --paths a.go,b.go,internal/`
   - "check if a path exists without reading it" → `ash stat --paths some/path` (error field = "not_found" if absent)
   - `ash stat` uses `lstat`, so symlinks report as their own type with `link_target` set.
   - A missing path sets `error="not_found"` on that entry; the call itself still succeeds with the other paths.
   - Both `--paths` (comma-separated, canonical) and `--path` (single path alias) are accepted.
7. **Writing files in this repo** — use `ash write` instead of the harness Write tool. Examples:
   - "create a new file" → `ash write --path internal/foo/foo.go --content '...'`
   - "overwrite an existing file" → same; default is overwrite
   - "ensure no accidental overwrite" → add `--create_only true`
   - "write binary/base64 content" → `--encoding base64`
   - Parent dirs are created automatically (`--mkdir true` default).
8. Bash `find`, `cat`, `head`, `tail`, `ls -R`, `grep`, `rg`, `git status`, `stat` should be replaced by their `ash` equivalents in this repo.

**The whole point** is that you are the first user. If a verb errors, hangs, or feels heavier than the bash equivalent, that's a bug or a design gap — investigate, don't paper over. Write the session note.

### Enforcement

The repo ships a `PreToolUse` hook at `.claude/hooks/prefer-ash.py` (registered in `.claude/settings.json`) that denies the harness's built-in `Grep`/`Glob`/`Read` tools and bash `grep`/`rg`/`find`/`cat`/`head`/`tail`/`ls -R`/`git status`/`git log`/`stat` in this project, returning the equivalent `ash` invocation as the deny reason. Image/PDF/notebook reads are allowed through (`ash read` can't render them). `git diff`/`blame`/`show` and other not-yet-shipped ops are allowed through. See [docs/PreToolUse.md](docs/PreToolUse.md) for the full design and behavior matrix.

If `ash` genuinely doesn't fit (a verb that hasn't shipped, a non-text artifact, etc.), the hook is best-effort — when it gets in the way, that is a session-note finding, not a hook bug to "work around" with `--no-verify`-style escape hatches.

## How to invoke ash

```sh
ash <verb> [--key value | --key=value]... [--format pretty|json|msgpack]
```

`--format` is a global client flag stripped before the request is sent to the daemon. `pretty` (default) is human-readable; `json` emits the full response envelope as indented JSON; `msgpack` writes the raw wire bytes to stdout.

The client is `bin/ash`; the daemon is `bin/ashd`. The client auto-starts the daemon on first invocation by exec'ing `bin/ashd` (sibling lookup, then `$PATH`, then `$ASH_DAEMON`). Subsequent calls reuse the same daemon over a per-project Unix domain socket.

State lives in:

- `.ash/ledger.db` — SQLite, one row per call.
- `.ash/ashd.log` — daemon stderr/stdout.
- `$XDG_RUNTIME_DIR/ash/` or `$TMPDIR` — UDS file (`ash-<8-byte-hash>.sock`).

The daemon also prints a one-line metrics summary to stderr on every call, which is what you see after the response body.

### Inspecting the ledger

Use `ash report` for a synthesized view, `ash metrics` for raw rows — no sqlite3 required:

```sh
ash report                         # per-verb summary for this session
ash report --since 1h              # same, filtered to the last hour
ash report --verb grep             # drill into one verb
ash metrics                        # last 20 raw rows
ash metrics --last 50              # last 50 raw rows
ash metrics --verb find            # only find rows
```

If you need raw SQL access that these verbs can't express yet:

```sh
sqlite3 .ash/ledger.db "SELECT verb, ok, tokens_in, tokens_out, latency_exec_us FROM calls ORDER BY id DESC LIMIT 20"
```

The ledger is the substrate for the recursive-development experiment. If a session feels heavy or surprising, query the ledger first — it almost certainly knows why.

### Live verbs

- `ash read --path <p> [--range start:end] [--range_kind lines|bytes] [--limit_bytes N]` — read a file (or a line/byte range of one). UTF-8 returned as-is; binary returned base64-encoded with `encoding=base64` in the response. Default size cap is 256 KiB.
- `ash find --path <p> [--glob <pattern>] [--type any|file|dir|symlink] [--max_depth N] [--limit N] [--exclude <pattern>] [--include_hidden true|false] [--respect_gitignore true|false] [--with_meta true|false]` — list paths under `<p>`. `glob` and `exclude` are doublestar patterns (`**` for recursive, `*.{go,md}` for alternation, etc.). `include_hidden` defaults to false (directories starting with `.` are skipped; leaf dotfiles like `.gitignore` are still findable). `respect_gitignore` defaults to **true** — the `.gitignore` at the walk root (`<p>`) is loaded and applied. Pass `--respect_gitignore false` for a raw filesystem walk. Nested `.gitignore` files are not yet honored. Default `limit` is 256, hard cap 4096; truncation produces a hint about how to narrow. Symlinks are reported but never followed. **Pretty form** is path-only by default (with trailing `/` for directories); pass `--with_meta true` for `<F|D|L> <size> <yyyy-mm-dd> <path>` rows when the per-record overhead (~10 tokens/row, dominated by the date) is worth it. The wire/JSON form always carries size + mtime + type — `--with_meta` only changes the human-readable rendering.
- `ash grep --pattern <re> --path <p> [--glob <pattern>] [--case smart|sensitive|insensitive] [--fixed_string true|false] [--word true|false] [--max_matches N] [--max_per_file N] [--context_before N] [--context_after N] [--files_only true|false] [--exclude <pattern>] [--max_depth N] [--include_hidden true|false] [--respect_gitignore true|false]` — RE2 pattern search. `path` may be a single file or a directory. `case` defaults to `smart` (insensitive unless the pattern has an uppercase letter, like ripgrep). `fixed_string=true` escapes regex metacharacters; `word=true` wraps with `\b…\b`. Default `max_matches` is 256, hard cap 4096; `max_per_file` is unlimited (0). Context (before/after) is capped at 50 lines per side and dedups across overlapping matches. `files_only=true` returns just the paths of matching files. Same `.gitignore`, hidden-dir, and walk semantics as `find`. Binary files (NUL in first 8 KiB) and files >16 MiB are skipped silently and reported via `files_skipped_binary` / `files_skipped_large`. Symlinks are not followed.
- `ash git --op <op> [--path <p>] [op-specific flags]` — version control as structured calls. Single verb with `--op` discriminator. Live ops: `status`, `log`, `diff`.
  - `status` splits changes into `staged`/`unstaged`/`untracked`/`conflicts` (a file with index AND worktree changes appears in both). Branch info: `branch`, `upstream`, `ahead`, `behind`, `detached`, `initial`. Args: `--untracked` (default true), `--ignored` (default false).
  - `log` returns structured `Commit` records: full+short SHA, author/committer name+email+time (unix nanos), parents (full SHAs), subject, body. Body retains embedded newlines. Args: `--limit` (default 20, max 200), `--range`, `--author`, `--since`, `--until`, `--pathspec`.
  - `diff` returns per-file structured diff: path, old_path (renames), status (A/D/M/R/C), additions, deletions, raw patch text. Args: `--staged` (index vs HEAD), `--range` (e.g. `HEAD~1..HEAD`), `--pathspec`, `--stat true` (token-cheap counts-only mode), `--context` (default 3), `--limit_bytes` (default 256 KiB).
  - Shells out to system `git`; `git_not_found` / `not_a_repo` / `git_failed` error codes. Future ops (`blame`, `show`) follow in subsequent ships.
- `ash metrics [--last N] [--verb <verb>]` — query recent call history from the ledger. Returns a table of timestamp / verb / ok / tokens_in / tokens_out / latency_exec_us, with `walk_us` / `io_us` / `regex_us` columns appended when the verb instrumented them. `last` defaults to 20, max 200. `verb` filters to a single verb (e.g. `--verb find`). Use this instead of shelling out to `sqlite3`.
- `ash report [--session current|all|<id>] [--since <duration>] [--last <N>] [--verb <verb>]` — aggregated per-verb summary from the ledger: call count, ok%, p50/p95 exec latency, p50/p95 tokens_out, truncation rate. Defaults to the current daemon session. Use instead of `ash metrics --last 200` when you want synthesis rather than raw rows. Duration accepts Go format plus `d` suffix (e.g. `--since 1h`, `--since 7d`).
- `ash help [--verb <verb>]` — return the structured argument schema for one verb or all live verbs. Omit `--verb` to see all schemas. Useful for checking defaults and valid values without reading source.
- `ash stat --paths <p1>[,<p2>...]` — lstat one or more explicit paths and return `{type, size, mtime, mode, link_target?}` per entry. Per-entry `error` field (not_found / permission / stat) keeps a bulk call alive when some paths are missing. Use for pre-read size/mtime checks or existence testing without a full walk.
- `ash write --path <p> --content <text> [--encoding utf-8|base64] [--mkdir true|false] [--create_only true|false]` — write content to a file. Parent directories are created by default (`--mkdir true`). Atomic via temp-file+rename so a crash mid-write never leaves a partial file. Pass `--encoding base64` for binary content. `--create_only true` errors if the file already exists. Result reports `bytes_written` and `created` (bool).
- `ash bench [--verb <verb>] [--case <name>] [--limit N]` — run a canonical case list against ash and the bash equivalent the agent would otherwise have used; tokenize both with the same encoder and report per-case Δtokens / Δlatency, plus per-verb and overall summaries. Use to answer "is ash actually saving tokens, per verb and per query shape?" — see [docs/bench.md](docs/bench.md). Bash subprocesses are sandboxed (timeout, 16 MiB stdout cap); the bench call itself is recorded in the ledger like any other verb, but per-case ash dispatches are not.

## Session feedback ritual

This is the most important habit in this repo. The whole point of building `ash` recursively is that real session experience drives design. If we lose the feedback loop, the project loses its edge over top-down spec work.

**When to write a note.** Any session that touches `ash` (using it, debugging it, working around it, designing the next verb). When in doubt, write the note — it costs nothing and the project is still small enough that every data point matters.

**Where notes live.** `docs/session-notes/YYYY-MM-DD-<slug>.md` — one file per session, dated and slugged.

**What a note should contain.**

- **Task.** One line: what was the agent trying to do?
- **Verbs used.** Which `ash` verbs (or bash equivalents) were used.
- **Friction.** Where did `ash` fall short? Where did bash feel heavier than it needed to?
- **Workarounds.** Bash incantations or extra steps the agent reached for.
- **Suggestions.** New verbs, new flags, behavior changes — anything the experience pointed at.
- **Instrumentation.** When the ledger exists: paste the relevant rows, or summarize them. Numbers beat impressions.

Keep notes terse. A bullet list is fine. The goal is signal, not prose.

## Bash whitelist

Even after primordial `ash` ships, some operations stay in bash. Track them here so the dogfooding rule doesn't push agents into pretending verbs exist that don't.

- **`git` ops other than `status`, `log`, and `diff`** — `blame`, `show`, and all destructive ops (commit/push/reset/rebase/checkout/etc.) stay in bash until those ops ship under `ash git --op <name>`.
- **`go test`, `go build`, `go vet`** — until `test`/`build` land in Phase 2, build/test orchestration is bash.
- **System package management** (`brew`, `apt`, `npm install -g`, etc.) — never in scope for `ash`.
- **Process management at the OS level** — bash. (`proc` hasn't shipped yet.)
- **Anything not yet implemented as a verb.** When in doubt: bash, with a session note explaining what verb you wished existed.

Update this list as verbs ship and as new bash-only operations are identified.

## Memory hygiene

This file evolves with the surface. **Do not trust stale guidance.** If the verb list in the README has changed but the checklist here hasn't, the checklist is wrong — fix it before relying on it. If you're a fresh agent reading this for the first time, cross-reference the README's "Roadmap" section against the "When to prefer ash" checklist and reconcile any drift before acting.

The single rule that does not change: when in doubt, use bash, write a session note, and let the next agent benefit from your friction.
