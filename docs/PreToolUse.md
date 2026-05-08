# PreToolUse hook — forcing agents onto `ash` verbs

## Context

`ash` is a recursive-development experiment: the project's value depends on agents working in this repo *actually using* `ash` for `find`/`grep`/`read`/`git status`/`git log` so the ledger captures real friction data. We observed that agents (Claude included) were not reliably following the [CLAUDE.md](../CLAUDE.md) switch criteria — they reach for the harness's built-in `Grep`/`Glob` tools (or shell out to `grep`/`rg`/`find` via `Bash`) instead of `ash`.

The root cause is structural, not documentary: the harness system prompt instructs the model to "prefer dedicated tools over Bash," and the dedicated `Grep`/`Glob` tools are the ergonomic default. `CLAUDE.md` prose competes with that bias and loses. Shell aliases don't help — the `Bash` tool runs commands non-interactively (no rc files), and the dedicated tools bypass bash entirely.

The fix is a `PreToolUse` hook that intercepts the offending tool calls and denies them with a message pointing to the equivalent `ash` invocation. Block-and-nudge (not auto-rewrite), so the friction stays visible and feeds the session-notes ritual. Project-scoped (checked into the repo) so anyone working on `ash` gets the same enforcement.

## Approach

One small Python script behind a single `PreToolUse` matcher. The script reads the hook payload from stdin, decides per-tool whether to deny, and emits the structured `hookSpecificOutput` JSON when it wants to block. Exits 0 with no output when the call should be allowed through.

Why Python over bash+jq: the hook needs to parse JSON, dispatch on tool name, do per-tool argument extraction, and tokenize bash commands across `|`/`&&`/`;`. Python 3 is a reasonable assumption on any dev box working on this repo (we already require Go), and it keeps the script readable. No third-party deps.

Why one script, not three: easier to maintain; the matcher fires for any of the three tools and the script branches internally.

## Files

### [.claude/settings.json](../.claude/settings.json)

```json
{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Grep|Glob|Bash",
        "hooks": [
          {
            "type": "command",
            "command": "python3 \"$CLAUDE_PROJECT_DIR/.claude/hooks/prefer-ash.py\""
          }
        ]
      }
    ]
  }
}
```

`$CLAUDE_PROJECT_DIR` is set by Claude Code when invoking hooks; it gives the script a stable anchor regardless of cwd. The matcher is a regex OR of the three tool names. (`Read` is intentionally absent — see "Why `Read` isn't denied" below.)

### [.claude/hooks/prefer-ash.py](../.claude/hooks/prefer-ash.py)

Reads the JSON payload from stdin, dispatches on `tool_name`:

- **`Grep`** → always deny. Suggests `ash grep --pattern <p> --path <d> [--glob …]`.
- **`Glob`** → always deny. Suggests `ash find --path <d> --glob <p> --type file`.
- **`Read`** → not handled (allowed through). See "Why `Read` isn't denied" below.
- **`Bash`** → tokenize the command across `|`/`&&`/`||`/`;`. For each segment, look at the first command word (skipping leading `VAR=value` assignments and `env`/`command`/`exec` prefixes). Deny when:
  - `grep|rg|egrep|fgrep` → suggest `ash grep`
  - `find` → suggest `ash find` (extracts `-name`/`-iname` glob if present)
  - `cat|head|tail` → suggest `ash read`
  - `ls -R` (or `--recursive`) → suggest `ash find`
  - `git status` / `git log` → suggest `ash git --op status|log`
  - **Allowed git ops** (do not block): `diff`, `blame`, `show`, `add`, `commit`, `push`, `reset`, `rebase`, `checkout`, `branch`, `stash`, `tag`, `fetch`, `pull`, `init`, `remote`, `merge`, `cherry-pick`, `restore`, `switch`. Per `CLAUDE.md`, these stay in bash until the corresponding `ash git --op <name>` ships.
  - Anything else → allow.

The deny payload:

```json
{
  "hookSpecificOutput": {
    "hookEventName": "PreToolUse",
    "permissionDecision": "deny",
    "permissionDecisionReason": "Use ash instead: `ash <verb> ...`. See CLAUDE.md \"When to prefer ash over bash\". If ash genuinely falls short, run it anyway and write a session note in docs/session-notes/."
  }
}
```

Robustness: any unexpected exception during decision-making prints to stderr and exits 0 (allow). The hook should steer, never break.

The suggested `ash` invocation is best-effort — the goal is to give the agent a known-good starting point, not a perfect translation.

## Behavior matrix

| Caller surface              | Decision | Suggested replacement                          |
|-----------------------------|----------|------------------------------------------------|
| `Grep` tool                 | deny     | `ash grep --pattern <p> --path <d> [--glob …]` |
| `Glob` tool                 | deny     | `ash find --path <d> --glob <p> --type file`   |
| `Read` tool                 | allow    | (load-bearing for harness Edit/Write — see below) |
| `Bash`: `grep`/`rg`/…       | deny     | `ash grep …`                                   |
| `Bash`: `find …`            | deny     | `ash find …`                                   |
| `Bash`: `cat`/`head`/`tail` | deny     | `ash read …`                                   |
| `Bash`: `ls -R`             | deny     | `ash find …`                                   |
| `Bash`: `git status`/`log`  | deny     | `ash git --op status\|log`                     |
| `Bash`: `git diff`/`blame`/…| allow    | (not yet a verb; CLAUDE.md says stay in bash)  |
| `Bash`: `go build`/`test`/… | allow    | (whitelisted in CLAUDE.md)                     |
| `Bash`: anything else       | allow    |                                                |

## Why `Read` isn't denied

The hook originally denied harness `Read` on text files (allow-listing only `.png`/`.jpg`/`.pdf`/`.ipynb`/etc., where `ash read`'s base64 fallback isn't useful). That decision had a hidden interaction with the harness's edit workflow: the `Edit` and `Write` tools both refuse to act unless the harness has previously seen a `Read` on the same path. Denying `Read` therefore made it impossible to edit a Go file without bash workarounds — `tee` heredocs, `python3 -c "open(...).write(...)"`, and similar — which both bypass the ledger and produce uglier diffs than the native `Edit` tool.

Friction-as-feature is good only when the friction has a productive exit. This one didn't: there's no `ash edit`/`ash write` verb yet, so the agent had nowhere to land. We removed the `Read` handler entirely. Bash `cat`/`head`/`tail` denials are kept, so pure-exploration reading still funnels through `ash read` and shows up in the ledger; we just stop blocking the read that the harness needs in order to *edit*.

When `ash edit` and `ash write` ship, this can be revisited and `Read` denial reintroduced symmetrically. See [docs/session-notes/2026-05-08-hook-allows-read.md](session-notes/2026-05-08-hook-allows-read.md) for the original incident.

## What we deliberately did NOT do

- **Auto-rewriting** the call to an `ash` invocation. Block-and-nudge is the chosen design so friction stays visible and feeds session notes.
- **An escape hatch** (env var or sentinel comment). Per CLAUDE.md's spirit, friction is the feature. If a future use case demands a bypass, add it in a follow-up driven by an actual session note — not preemptively.
- **Allowlist-style permission denies** in `settings.json` (`"deny": ["Grep"]`). They block without a custom reason and don't cover `Bash`-side leakage. The hook is strictly more flexible.
- **Shell aliases.** Don't work for the harness `Bash` tool (non-interactive, no rc) and don't touch `Grep`/`Glob`/`Read` at all.

## Verification

```sh
# 1. Direct script invocation — Grep should deny.
python3 .claude/hooks/prefer-ash.py < <(echo '{"tool_name":"Grep","tool_input":{"pattern":"foo","path":"."}}')

# 2. Read on any file — should allow (no output, exit 0). Read isn't dispatched at all.
python3 .claude/hooks/prefer-ash.py < <(echo '{"tool_name":"Read","tool_input":{"file_path":"main.go"}}')

# 3. Bash `git diff` — should allow.
python3 .claude/hooks/prefer-ash.py < <(echo '{"tool_name":"Bash","tool_input":{"command":"git diff"}}')

# 4. Bash `git status` — should deny with `ash git --op status`.
python3 .claude/hooks/prefer-ash.py < <(echo '{"tool_name":"Bash","tool_input":{"command":"git status"}}')

# 5. Bash `cat foo.go` — should still deny with `ash read --path foo.go`.
python3 .claude/hooks/prefer-ash.py < <(echo '{"tool_name":"Bash","tool_input":{"command":"cat foo.go"}}')
```

End-to-end: restart the Claude Code session in this project so the new `settings.json` takes effect (hooks are loaded at session start). In a fresh session, ask Claude to "find all `.go` files under `internal/`" — the `Glob` or `Bash find` attempt should be denied and Claude should retry with `ash find`. Then `ash report --since 1h` should show the corresponding bump in `ash` calls.

If Claude *fights* the hook (repeated bash subshells, creative escapes), that itself is a finding — write the session note rather than weakening the hook.
