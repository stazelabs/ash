# Adding ash to Cursor

Five-minute path to `ash_*` tools inside Cursor. Same `ashmcp` binary as
the [Claude Code path](claude-code.md); Cursor keeps MCP servers in a
`mcp.json` file.

## Prerequisites

1. Install the binaries — Homebrew or from source. See
   [install.md](install.md). The short version:

   ```sh
   brew install stazelabs/tap/ash
   ```

2. Confirm the path you'll paste into the config:

   ```sh
   which ashmcp
   # /opt/homebrew/bin/ashmcp
   ```

   You'll need the absolute path — Cursor launches MCP servers without
   your interactive shell's `$PATH`. `command` must be on the system path
   or a full path.

## Register the MCP server

Cursor reads two `mcp.json` files and merges them:

| Scope | Path | Applies to |
|---|---|---|
| Project | `.cursor/mcp.json` in the repo root | that repository only |
| Global  | `~/.cursor/mcp.json` (`%USERPROFILE%\.cursor\mcp.json` on Windows) | every project |

**Project scope is recommended** — it keeps ash with the repo it belongs
to and gives `ashmcp` the right working directory (see below).

Create `.cursor/mcp.json` with an `mcpServers` object. Merge with any
existing entries — don't replace the object:

```json
{
  "mcpServers": {
    "ash": {
      "command": "/opt/homebrew/bin/ashmcp",
      "args": []
    }
  }
}
```

`command` is required; `args` and `env` are optional. `ashmcp` itself
takes no arguments — it speaks MCP over stdio, resolves the project root
from its working directory on every tool call, and auto-starts `ashd` if
needed.

> **Missing `mcpServers` fails silently.** If the root key is absent, or
> the JSON has a stray comma, Cursor ignores the whole file with no error.
> Validate with `python3 -m json.tool < .cursor/mcp.json`.

## Verify

1. Open **Settings** (`Cmd+Shift+J` / `Ctrl+Shift+J`) → **Features** →
   **Model Context Protocol**. `ash` should be listed; the toggle beside
   it enables/disables it, and the row expands to show the available
   tools.
2. Ask the agent something search-shaped — "find every TODO under
   `src/`" — and watch for an `ash_grep` call in the tool stream.
3. From a terminal, `ash report --verb grep --since 5m` in the project —
   the grep you just triggered should appear in the ledger.

## Project root and working directory

`ashmcp` treats its working directory as the project root. With a
project-scoped `.cursor/mcp.json`, Cursor launches it inside that
workspace, so paths resolve correctly. If you use the global
`~/.cursor/mcp.json` across multiple repos and tool calls land in the
wrong project, tell the agent to pass **absolute paths** to every `ash_*`
tool — `ashmcp` honors them regardless of working directory.

## Tool budget

`ashmcp` contributes 14 tools. Cursor caps the number of *active* MCP
tools across all servers combined (around 40); past the cap it warns and
silently drops tools. If you run several MCP servers, disable ones you
aren't using from the MCP settings pane so ash's tools stay active.

## What the model sees

Identical MCP surface to the Claude Code path — same `ashmcp` binary, same
wire protocol. See
[claude-code.md §What the model sees](claude-code.md#what-the-model-sees)
for a real `tools/list` and `tools/call` capture. The 14 tools are 11
read-side (`ash_read`, `ash_find`, `ash_grep`, `ash_stat`, `ash_git`,
`ash_report`, `ash_metrics`, `ash_help`, `ash_recap`, `ash_workspace`,
`ash_lang`) and 3 write-side (`ash_write`, `ash_edit`, `ash_diff`). Every
call still lands in the project's `.ash/ledger.db`.

## Troubleshooting

- **`ash` doesn't appear in the MCP settings pane.** Validate the JSON
  (`python3 -m json.tool < .cursor/mcp.json`); confirm `command` is an
  absolute path and executable. Cursor reloads `mcp.json` on edit, but a
  full restart is the reliable reset.
- **`ash` is listed but every call errors `dial daemon`.** `ashmcp`
  couldn't reach `ashd` for the project root. Run `ash help` in the
  project once to confirm the daemon starts; check `.ash/ashd.log`.
- **Calls write to the wrong project's ledger.** A global `mcp.json` left
  `ashmcp` resolving paths against the wrong directory — switch to a
  project-scoped `.cursor/mcp.json`, or have the agent pass absolute
  paths.
- **ash's tools are missing even though `ash` is enabled.** Cursor's
  active-tool cap was exceeded — disable other MCP servers you aren't
  using.

## The bash hook still works alongside this

If a repo was set up with `ash init`, its PreToolUse hook and the MCP path
coexist without conflict — see
[migration-from-hook.md](migration-from-hook.md) for when to remove the
hook.
