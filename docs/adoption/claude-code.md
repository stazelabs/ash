# Adding ash to Claude Code

Five-minute path to `ash_*` tools alongside Claude Code's built-ins. End state: `claude mcp list` reports `ash` as connected and the model can call `ash_grep`, `ash_read`, etc. without going through the bash hook.

## Prerequisites

1. Clone and build the binaries:

   ```sh
   git clone https://github.com/stazelabs/ash.git
   cd ash
   make all          # builds bin/ash, bin/ashd, bin/ashmcp
   make install      # symlinks them into ~/.local/bin (override with PREFIX=)
   ```

   Symlinks, not copies — a future `make all` updates every install site in place.

2. Confirm `ashmcp` is on `$PATH`:

   ```sh
   which ashmcp
   # /Users/you/.local/bin/ashmcp
   ```

   If your shell can't find it, either add the install prefix to `$PATH` or use the absolute path in the snippet below.

## Register the MCP server

Either via the Claude Code CLI (recommended; no JSON editing) or by editing `~/.claude.json` directly.

### Option A — `claude mcp add` (recommended)

```sh
claude mcp add --scope user ash "$(which ashmcp)"
```

`--scope user` makes the server available across every project on this machine. Use `--scope project` to scope it to one repo (writes `<repo>/.mcp.json` instead of `~/.claude.json`).

### Option B — edit `~/.claude.json`

Add an `mcpServers` entry. If the key already exists, merge in `ash`; don't replace the object:

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

Use an absolute path — Claude Code launches MCP servers without inheriting your interactive shell's `$PATH` modifications, so bare `ashmcp` may fail.

`ashmcp` takes no flags. It speaks MCP over stdio, resolves the project root from the current working directory on every tool call, and auto-starts `ashd` if it isn't already running for that project.

### Option C — project-scope `.mcp.json` (per-repo install)

To scope `ash` to a single repository — for dogfooding inside `ash` itself, or installing per-project on a multi-checkout machine — write a `.mcp.json` at the repo root:

```json
{
  "mcpServers": {
    "ash": {
      "command": "/absolute/path/to/bin/ashmcp",
      "args": []
    }
  }
}
```

`claude mcp add --scope project ash "$(which ashmcp)"` generates this idempotently — the absolute path is captured at config time. Hand-editing the file works the same way.

**Env vars are not expanded in `command`.** Claude Code does *not* substitute `${CLAUDE_PROJECT_DIR}`, `$HOME`, or any other variable in MCP `command` fields. A snippet like `"command": "${CLAUDE_PROJECT_DIR}/bin/ashmcp"` fails at session bootstrap with `ENOENT: posix_spawn '${CLAUDE_PROJECT_DIR}/bin/ashmcp'`. `${CLAUDE_PROJECT_DIR}` in particular is a hook-context-only variable injected by the PreToolUse runtime — it never reaches MCP server spawn. Use the absolute path.

**`.mcp.json` is per-checkout-machine.** Because `command` must be a literal absolute path, a checked-in `.mcp.json` would point at one author's install prefix and break for everyone else. In the `ash` repo `.mcp.json` is gitignored for exactly this reason — each contributor's copy points at their own `bin/ashmcp`.

## Verify

In a fresh shell:

```sh
claude mcp list
# ash: /Users/you/.local/bin/ashmcp - ✓ Connected
```

Then in a Claude Code session inside any project:

- Type `/mcp` to inspect connected servers. `ash` should appear with 8 tools: `ash_find`, `ash_git`, `ash_grep`, `ash_help`, `ash_metrics`, `ash_read`, `ash_report`, `ash_stat`.
- Ask Claude to do something search-shaped — "find every reference to `Foo` under `src/`" — and watch for an `ash_grep` call in the tool-use stream.

## What the model sees

`tools/list` from a real session against this repo (trimmed; one tool):

```jsonc
// →
{"jsonrpc":"2.0","id":2,"method":"tools/list"}
// ←
{"jsonrpc":"2.0","id":2,"result":{"tools":[
  {
    "name":"ash_grep",
    "description":"Search files for an RE2 pattern; skips binary and files >16 MiB.",
    "inputSchema":{
      "$schema":"https://json-schema.org/draft/2020-12/schema",
      "type":"object",
      "properties":{
        "pattern":{"type":"string","description":"RE2 regex; literal text when lit=true."},
        "path":{"type":"string","description":"File or directory to search."},
        "glob":{"type":"string","description":"Doublestar pattern; scan only matching files.","default":"**"}
        // ... 12 more args
      },
      "required":["path","pattern"]
    }
  }
  // ... 7 more tools
]}}
```

And a real `tools/call`:

```jsonc
// →
{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{
  "name":"ash_grep",
  "arguments":{"pattern":"adoption","path":"README.md"}
}}
// ←
{"jsonrpc":"2.0","id":3,"result":{"content":[{"type":"text","text":
  "{\"data\":{\"count\":1,\"file_count\":1,\"files_scanned\":1,\"match_count\":1,
   \"matches\":[{\"col\":17,\"line\":306,\"path\":\"README.md\",\"text\":\"### Phase 4 — adoption\"}]},
   \"metrics\":{\"lp\":15,\"le\":669,\"ti\":9,\"to\":37,\"bi\":64,\"bo\":225,\"ph\":{\"io\":270,\"r\":341,\"rc\":15}},
   \"ok\":true}"
}]}}
```

Each `tools/call` result is the same envelope `ash` itself emits — `ok`, `data`, and `metrics` fields — wrapped as MCP text content. The model parses it like any other tool result, and every call still lands in the project's `.ash/ledger.db`.

## Troubleshooting

- **`ENOENT: posix_spawn '${CLAUDE_PROJECT_DIR}/bin/ashmcp'` at session start.** A project-scope `.mcp.json` is using `${CLAUDE_PROJECT_DIR}` (or another env var) in the `command` field. Claude Code does not expand env vars there — replace with the absolute path. See [Option C](#option-c--project-scope-mcpjson-per-repo-install).
- **`claude mcp list` shows `ash: ... - ✗ Failed to connect`.** Check `ashmcp` is executable and on `$PATH` for non-interactive shells. Try the absolute path in `~/.claude.json` instead of `which ashmcp`'s output.
- **Tool calls error with `dial daemon`.** `ashmcp` couldn't reach `ashd` for the project root. Run `ash help` in the target project once to confirm the daemon starts; check `.ash/ashd.log` for crashes.
- **`ash_*` tools don't appear in the model's tool list.** Restart the Claude Code session — MCP server registration happens at session start, not on `~/.claude.json` save.
- **The bash hook keeps denying tool calls.** That's deliberate during transition; the hook steers the harness's built-in `Read`/`Grep` to `ash`, while MCP-routed calls go through `ash_*` and bypass the hook entirely. See [migration-from-hook.md](migration-from-hook.md) for when to remove the hook.

## What rolls out next

v1 exposes the eight read-side verbs only. Writes (`ash_write`, `ash_edit`, `ash_diff`) and the long-running verbs (`ash_test`, `ash_bench`) roll out as the Phase 2 ship list closes — same `ashmcp` binary, no config change. Streaming responses for `ash_grep` / `ash_find` activate automatically when the harness sends a `progressToken`.
