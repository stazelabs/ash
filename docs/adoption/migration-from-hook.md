# Migrating from the PreToolUse hook to MCP

`ash init` writes a `PreToolUse` hook into a target repo's `.claude/settings.json`. The hook intercepts Claude Code's built-in `Read`/`Grep`/`Glob`/`Edit`/`Write`/`Bash` tools and denies them with a message pointing at the equivalent `ash` invocation. It is the steering mechanism that makes `ash` win against the harness's default tool bias.

The MCP server (`ashmcp`) takes a different route: it advertises `ash_read`, `ash_grep`, etc. as first-class tools alongside the harness's built-ins from session start. The model picks them up organically because they're typed, structured, and cheap.

Both can coexist. The hook denies harness `Read`; the MCP path doesn't go through harness `Read`. Different code paths, no conflict.

## Decision matrix

| Situation | Keep the hook? |
|---|---|
| Mix of MCP-aware (Claude Code, Claude Desktop) and non-MCP harnesses on the same repo | **Yes** — the hook is the only enforcement for non-MCP harnesses. |
| Everyone on the team is on Claude Code with `ashmcp` registered | **Optional** — the hook becomes a belt-and-suspenders nudge. Remove for a quieter session, or keep as a guard against MCP outages. |
| Only Claude Desktop, no Claude Code | **Yes if the desktop config pins `cwd`**, otherwise **yes** — the hook is the project-scoped guard while Desktop runs from a global cwd. |
| Recursive-development sessions on the `ash` repo itself | **Keep** — the deny rows in `ash report --verb hook` are load-bearing friction data. |
| MCP server crashed mid-session (`tools/call` error: `dial daemon`) | **Hook saves you.** Calls fall back to harness built-ins, which the hook intercepts and points back to `ash`. |

The interesting case is the third row: when MCP succeeds, the harness's `Grep` tool simply isn't called, so the hook is silent. The hook only fires when the agent tries to use the *harness's* tools — usually because MCP isn't wired up, or the model chose a built-in over the typed equivalent.

## How to remove the hook

Two equivalent paths.

### Via `ash uninit`

```sh
cd /path/to/target-repo
ash uninit
```

Removes the hook entry from `.claude/settings.json`, removes the `.ash/` line from `.gitignore`, and deletes the repo from `~/.config/ash/installed-repos.txt`. The ledger DB (`.ash/ledger.db`) is left in place so historical session data stays queryable.

Re-running `ash init` reinstalls cleanly.

### Manually

Edit `<repo>/.claude/settings.json` and remove the `PreToolUse` entry whose command contains `ash hook`:

```json
{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Grep|Glob|Bash|Edit|Write|Read",
        "hooks": [
          { "type": "command", "command": "\"$CLAUDE_PROJECT_DIR/bin/ash\" hook" }
        ]
      }
    ]
  }
}
```

Delete the object, save, and restart the Claude Code session. The ledger and registry entry are left untouched.

## Verifying the migration

After removing the hook and registering `ashmcp`:

1. Restart the Claude Code session so both the hook removal and the MCP registration take effect.
2. Ask the model to grep something. It should call `ash_grep` directly (visible in the tool-use stream) with no deny message.
3. From a terminal: `ash report --since 5m --verb grep` shows the call; `ash report --since 5m --verb hook` is empty.
4. Confirm the harness has no fallback path: ask the model to use a regular `Grep` call explicitly. Without the hook, it succeeds — the harness's built-in runs, no `ash_*` call lands in the ledger. This is the trade-off you've accepted: less coverage, fewer denies.

## Why we don't auto-remove the hook

`ash init`'s job is to make the repo work for *any* agent that walks in, not to assume MCP is wired up. Auto-removing on `ashmcp` registration would surprise the next teammate whose harness doesn't speak MCP yet (Codex CLI, Cursor's older versions, a custom internal harness). The migration is one explicit command (`ash uninit`) when you're ready.

## Recursive-development implication

The hook produced the highest-fidelity friction signal in `ash`'s first year: every deny row is the agent fighting the surface, and `ash report --verb hook` aggregates the rules that fire most. MCP shifts that signal to a different shape — instead of denies, we see tool-call frequencies in `ash report --verb <name>`. Both are useful, neither is complete:

- **Hook denies** tell us where the harness's built-ins still pull agents off `ash`.
- **MCP call rates** tell us which `ash_*` tools the model reaches for organically.

A session note comparing the two for the same task is the kind of evidence the experiment runs on — see [docs/session-notes/](../session-notes/) for the format.
