# Adding ash to Claude Desktop

Five-minute path to `ash_*` tools inside Claude Desktop on macOS, Windows, or Linux. Same `ashmcp` binary as Claude Code; only the config path and a few cwd caveats differ.

## Prerequisites

Build and install the binaries from a checkout of this repo:

```sh
git clone https://github.com/stazelabs/ash.git
cd ash
make all          # builds bin/ash, bin/ashd, bin/ashmcp
make install      # symlinks them into ~/.local/bin (override with PREFIX=)
```

Then confirm the path you'll paste into the config:

```sh
which ashmcp
# /Users/you/.local/bin/ashmcp
```

You'll need the absolute path — Claude Desktop launches MCP servers from its own working directory without your shell's `$PATH`.

## Edit `claude_desktop_config.json`

Open the desktop client and use *Settings → Developer → Edit Config*, or open the file directly:

| Platform | Path |
|---|---|
| macOS   | `~/Library/Application Support/Claude/claude_desktop_config.json` |
| Windows | `%APPDATA%\Claude\claude_desktop_config.json` |
| Linux   | `~/.config/Claude/claude_desktop_config.json` |

Add an `mcpServers` entry. Merge with any existing entries — don't replace the object:

```json
{
  "mcpServers": {
    "ash": {
      "command": "/Users/you/.local/bin/ashmcp",
      "args": []
    }
  }
}
```

`ashmcp` takes no flags. It speaks MCP over stdio, resolves the project root from its working directory at every tool call, and auto-starts `ashd` if needed.

### Pinning the project root (recommended)

Claude Desktop launches `ashmcp` from a fixed working directory (the desktop app's, not yours). That means `ashmcp` will treat that directory as the project root unless you tell it otherwise. Two options:

1. **Pass absolute paths in every tool call.** Tell the agent: "always use absolute paths with `ash_*` tools." Works in any project the agent might inspect, but relies on the agent following the instruction.
2. **Pin the working directory.** Add a `cwd` field so `ashmcp` starts in a known project:

   ```json
   {
     "mcpServers": {
       "ash": {
         "command": "/Users/you/.local/bin/ashmcp",
         "args": [],
         "cwd": "/Users/you/code/my-project"
       }
     }
   }
   ```

   The trade-off is one `ash` server per project — add a second entry (`"ash-other"`) for each repo you regularly inspect. The daemon and ledger remain per-project; only the MCP front-end is duplicated.

Most users want Option 2 with a single primary project. Option 1 is right when the agent regularly hops across repos within one Desktop session.

## Restart Claude Desktop and verify

Fully quit Claude Desktop (the menu bar / tray icon, not just the window) and relaunch — MCP server registration happens at startup.

- Click the hammer / tools icon in the chat composer. The pop-over should list `ash` with 8 tools: `ash_find`, `ash_git`, `ash_grep`, `ash_help`, `ash_metrics`, `ash_read`, `ash_report`, `ash_stat`.
- Ask the model: *"Use ash_grep to find every TODO in the project and report the line numbers."* Watch the tool-call confirmation that pops up — the JSON arguments should land in `ashmcp` and the response should come back as structured text.
- In a terminal, run `ash report --verb grep --since 5m` inside the pinned project. The grep call you just made should show up in the ledger.

## What the model sees

Same MCP surface as Claude Code; see [claude-code.md §What the model sees](claude-code.md#what-the-model-sees) for a real `tools/list` and `tools/call` capture against this repo. The wire format is identical — only the harness differs.

## Troubleshooting

- **Hammer icon shows no `ash` entry.** Check the JSON parses (`python3 -m json.tool < ~/Library/Application\ Support/Claude/claude_desktop_config.json`). Confirm `command` is absolute. Restart Claude Desktop fully — *Cmd-Q* on macOS, not just close-window.
- **Tools list shows `ash` but every call errors `dial daemon`.** `ashmcp` started in a directory with no project context. Set `cwd` to a real project root, or have the agent pass absolute paths.
- **Tool calls succeed but write to the wrong project's ledger.** `cwd` is wrong, or the agent is using relative paths and `ashmcp` is resolving them against Desktop's launch directory. Inspect `.ash/ledger.db` under the project's actual root.
- **Logs.** Claude Desktop captures MCP stderr to `~/Library/Logs/Claude/mcp*.log` on macOS (analogous paths on Windows/Linux). `ashmcp`'s startup error and any failed dials surface there.

## What rolls out next

v1 exposes the eight read-side verbs only. Writes (`ash_write`, `ash_edit`, `ash_diff`) and long-running verbs (`ash_test`, `ash_bench`) follow as the Phase 2 ship list closes. Streaming responses for `ash_grep` / `ash_find` activate automatically when the harness sends a `progressToken`; Claude Desktop's MCP client opts in for long-running tool calls.
