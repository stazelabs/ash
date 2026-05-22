# Adding ash to Codex CLI

Five-minute path to `ash_*` tools inside OpenAI's Codex CLI. Same `ashmcp`
binary as the [Claude Code path](claude-code.md); only the registration
mechanics differ — Codex keeps MCP servers in a TOML config.

## Prerequisites

1. Install the binaries — Homebrew or from source. See
   [install.md](install.md). The short version:

   ```sh
   brew install stazelabs/tap/ash
   ```

2. Confirm `ashmcp` is on `$PATH`:

   ```sh
   which ashmcp
   # /opt/homebrew/bin/ashmcp
   ```

   You'll want this absolute path below — Codex launches MCP servers
   without inheriting your interactive shell's `$PATH` modifications, so a
   bare `ashmcp` may fail.

## Register the MCP server

Either via the `codex mcp` CLI (recommended; no hand-editing) or by
editing `~/.codex/config.toml` directly.

### Option A — `codex mcp add` (recommended)

```sh
codex mcp add ash -- "$(which ashmcp)"
```

The `--` separates Codex's own flags from the server launch command;
`ashmcp` itself takes no arguments. The Codex CLI and the Codex IDE
extension share `~/.codex/config.toml`, so this one command wires up both.

### Option B — edit `~/.codex/config.toml`

Add an `[mcp_servers.ash]` table. If the file already has other servers,
merge this in — don't replace anything:

```toml
[mcp_servers.ash]
command = "/opt/homebrew/bin/ashmcp"
args = []
```

`command` is required and must be an absolute path; `args`, `env`, and
`cwd` are optional.

### Project-scoped config

Codex also reads `.codex/config.toml` at the repo root, for **trusted**
projects only. The same `[mcp_servers.ash]` block there scopes ash to that
one repository. Untrusted projects ignore the file silently — trust the
project in Codex, or fall back to `~/.codex/config.toml`.

`ashmcp` speaks MCP over stdio, resolves the project root from its working
directory on every tool call, and auto-starts `ashd` for that project if
it isn't already running.

## Verify

In a Codex session, type `/mcp` to list active MCP servers — `ash` should
appear with its tools. `codex mcp --help` lists the management
subcommands if you prefer to inspect from a shell.

Then ask Codex to do something search-shaped — "find every reference to
`Foo` under `src/`" — and watch for an `ash_grep` call in the tool-use
stream.

## Project root and working directory

Codex launches `ashmcp` from the directory you started Codex in, so run
Codex from the repository root and `ashmcp` resolves the project
correctly. If you start Codex elsewhere, either pin the root with a `cwd`
key in the `[mcp_servers.ash]` block:

```toml
[mcp_servers.ash]
command = "/opt/homebrew/bin/ashmcp"
args = []
cwd = "/Users/you/code/my-project"
```

or tell the agent to pass absolute paths to every `ash_*` tool.

## What the model sees

Identical MCP surface to the Claude Code path — same `ashmcp` binary, same
wire protocol. See
[claude-code.md §What the model sees](claude-code.md#what-the-model-sees)
for a real `tools/list` and `tools/call` capture. `ashmcp` exposes 14
tools: 11 read-side (`ash_read`, `ash_find`, `ash_grep`, `ash_stat`,
`ash_git`, `ash_report`, `ash_metrics`, `ash_help`, `ash_recap`,
`ash_workspace`, `ash_lang`) and 3 write-side (`ash_write`, `ash_edit`,
`ash_diff`). Every call still lands in the project's `.ash/ledger.db`.

## Troubleshooting

- **`ash` doesn't appear under `/mcp`.** Confirm `command` is an absolute
  path and executable, and that `~/.codex/config.toml` parses as TOML.
  Restart the Codex session — MCP registration happens at session start.
- **Tool calls error with `dial daemon`.** `ashmcp` couldn't reach `ashd`
  for the project root. Run `ash help` in the target project once to
  confirm the daemon starts; check `.ash/ashd.log` for crashes.
- **Calls write to the wrong project's ledger.** Codex started `ashmcp`
  outside the repo. Pin `cwd` in the `[mcp_servers.ash]` block, or have
  the agent pass absolute paths. Inspect `.ash/ledger.db` under the
  project's actual root to confirm.
- **A project-scoped `.codex/config.toml` is ignored.** Codex only reads
  it for trusted projects — trust the project, or move the entry to
  `~/.codex/config.toml`.

## The bash hook still works alongside this

If a repo was set up with `ash init`, its PreToolUse hook and the MCP path
coexist without conflict — see
[migration-from-hook.md](migration-from-hook.md) for when to remove the
hook.
