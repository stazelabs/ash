# Project-scope `.mcp.json` — `${CLAUDE_PROJECT_DIR}` is not expanded

**Task.** Diagnose `ashmcp failed: ENOENT: no such file or directory, posix_spawn '${CLAUDE_PROJECT_DIR}/bin/ashmcp'` reported by Claude Code at session start.

**Verbs used.** `ash read`, `ash write`, `ash find`. No bash needed beyond an `ls` for the binary stat.

**Root cause.** A locally-authored, untracked `.mcp.json` in the repo root contained:

```json
{ "mcpServers": { "ash": { "command": "${CLAUDE_PROJECT_DIR}/bin/ashmcp", "args": [] } } }
```

Claude Code does **not** expand environment variables in the `command` field of `.mcp.json`. `${CLAUDE_PROJECT_DIR}` is a hook-context-only variable injected by the PreToolUse runtime — it is not part of the MCP server spawn environment. Result: `posix_spawn` got the literal string and failed with ENOENT.

**Fix applied.** Replaced with the absolute path:

```json
{ "mcpServers": { "ash": { "command": "/Users/cstaszak/Stazelabs/projects/ash/bin/ashmcp", "args": [] } } }
```

Worked on next session start. `.mcp.json` stays untracked (it is per-checkout-machine because of the absolute path).

**Friction.**

- **`docs/adoption/claude-code.md` doesn't cover this case.** The doc covers user-scope install (`make install` → `~/.local/bin/ashmcp` → either `claude mcp add --scope user` or hand-edit `~/.claude.json`) and mentions `--scope project` writes a `.mcp.json`. It does not show what a project-scope `.mcp.json` should look like for `ashmcp`, and it does not warn that `${CLAUDE_PROJECT_DIR}` (or any env var) won't expand there. An agent trying to dogfood ashmcp in the ash repo itself — the canonical first user — hits this trap with no doc to land on.
- **Diagnosis required reading source.** The error surfaces inside Claude Code's session bootstrap, not from `ashmcp`. There is no ash-side instrumentation possible — the binary never ran. So the only mitigation is documentation. There is no ledger row for this failure mode (correctly — ash was never invoked).
- **`.mcp.json` is not portable across checkouts.** Because the `command` must be an absolute path, a checked-in `.mcp.json` would either be wrong for everyone but its author, or would need a `Makefile`-rendered template. Worth a design moment before any "let's ship a `.mcp.json` template" reflex.

**Workarounds.** None needed beyond editing the file. No bash fallback.

**Suggestions.**

- Extend `docs/adoption/claude-code.md` with a "Dogfooding inside the ash repo" subsection covering project-scope `.mcp.json` with an absolute path, and call out the `${CLAUDE_PROJECT_DIR}` non-expansion explicitly.
- Consider whether `make install` (or a new `make install-mcp`) should idempotently write/update `~/.claude.json`'s `mcpServers.ash` entry to the install-prefix-resolved absolute path. Removes the hand-edit step and the path-literal trap for the user-scope flow. Lower priority — current docs work, this is ergonomic polish.

**Instrumentation.** N/A — the failure is pre-daemon. The fix verified by reading the corrected file with `ash read`:

```
ash bi=155 bo=296 ti=20 to=57 us=20/75 io=25
```

Same shape as any read. Confirms ashmcp launch will use ledger-instrumented dispatch on next session.
