# PreToolUse hook — `ash hook`

## Context

`ash` is a recursive-development experiment: the project's value depends on agents working in this repo *actually using* `ash` for `find`/`grep`/`read`/`git status`/`git log` so the ledger captures real friction data. We observed that agents (Claude included) were not reliably following the [CLAUDE.md](../CLAUDE.md) switch criteria — they reach for the harness's built-in `Grep`/`Glob` tools (or shell out to `grep`/`rg`/`find` via `Bash`) instead of `ash`.

The root cause is structural, not documentary: the harness system prompt instructs the model to "prefer dedicated tools over Bash," and the dedicated `Grep`/`Glob` tools are the ergonomic default. `CLAUDE.md` prose competes with that bias and loses. Shell aliases don't help — the `Bash` tool runs commands non-interactively (no rc files), and the dedicated tools bypass bash entirely.

The fix is a `PreToolUse` hook that intercepts the offending tool calls and denies them with a message pointing to the equivalent `ash` invocation. Block-and-nudge (not auto-rewrite), so the friction stays visible and feeds the session-notes ritual. Project-scoped (checked into the repo) so anyone working on `ash` gets the same enforcement.

## Approach

The hook is implemented as the `ash hook` verb — the only client-only verb in ash. It runs the deny decision in-process for low latency, then best-effort fires a normal ash request to the daemon to record a ledger row. Two reasons for folding it into ash:

1. **Latency.** The hook runs on every `Grep`/`Glob`/`Bash`/`Edit`/`Write`/`Read` call. A single-binary Go fast path is faster than spawning a Python interpreter on each invocation, and importantly never blocks on the daemon — auto-start is intentionally skipped to keep hook latency on the agent's critical path low.
2. **Ledger queryability.** Hook denials are the highest-fidelity friction signal in the project. Routing them through the ledger turns the hook from a fence into a research instrument: `ash report --verb hook` aggregates which rules are firing, which transitions agents are fighting, and how often.

### Architecture

`ash hook` is the only verb in the codebase that runs **client-side**. Every other verb round-trips to `ashd`. The flow:

1. `cmd/ash/main.go` special-cases `verb == "hook"` immediately after parsing argv (before `dialOrStart`). Hand-off to `runHook` in `cmd/ash/hook.go`.
2. `runHook` reads the Claude payload from stdin, calls `hook.Decide`, writes the Claude decision JSON to stdout, then dials the daemon with a 5ms timeout and fires the same args as a normal `ash` request — fire-and-forget, no `ReadFrame`. If the daemon isn't up, the ledger row is skipped silently. **The daemon is never auto-started by the hook fire.**
3. Daemon-side: `hook` is registered as a regular verb (`internal/verbs/verbs.go`). Its `Run` re-runs `hook.Decide` over the same args. Client and daemon agree by construction — both call the same pure function.

Soft-fail is end-to-end: any error in payload parsing, decision, or fire produces "allow" (no Claude output, exit 0). The hook should steer agents, never break their tool calls.

## Files

### [.claude/settings.json](../.claude/settings.json)

The matcher is a regex OR of the harness tool names that the hook decides on: `Grep|Glob|Bash|Edit|Write|Read`. The command is `"$CLAUDE_PROJECT_DIR/bin/ash" hook`. `$CLAUDE_PROJECT_DIR` is set by Claude Code when invoking hooks; it gives the command a stable anchor regardless of cwd.

If `bin/ash` doesn't exist (fresh clone, before `go build`), the hook command fails non-zero and the harness allows the call through. CLAUDE.md already directs `go build` as session step 1.

### [internal/verbs/hook/](../internal/verbs/hook/)

The Go implementation. Exports:

- `ExtractArgs(payload []byte) (map[string]any, *Args, error)` — client-only helper that parses the Claude JSON into a wire-args map (for the daemon fire) and a typed `Args`.
- `Decide(*Args) *Result` — pure decision engine, shared by client and daemon. Never returns an error.
- `ParseArgs` / `Run` — daemon-side wire decoder + runner, mirroring every other verb.
- `EncodeClaudeDecision(*Result) ([]byte, error)` — renders `Result` as Claude PreToolUse hook JSON; returns nil for allow (no output).

### [cmd/ash/hook.go](../cmd/ash/hook.go)

The client-only fast path: `runHook` ties `ExtractArgs` → `Decide` → `EncodeClaudeDecision` → async ledger fire.

## Decision rules

Dispatched on `tool_name`:

- **`Grep`** → always deny. Suggests `ash grep --pattern <p> --path <d> [--glob …]`.
- **`Glob`** → always deny. Suggests `ash find --path <d> --glob <p> --type file`.
- **`Edit`** → always deny. Suggests `ash edit --path <p> --old_string <o> --new_string <n>`.
- **`Write`** → always deny. Suggests `ash write --path <p> --content <text>`.
- **`Read`** → deny on text files. Allow on `.png`/`.jpg`/`.jpeg`/`.gif`/`.webp`/`.pdf`/`.ipynb` (file types `ash read` can't render meaningfully).
- **`Bash`** → tokenize the command across shell separators. For each segment, look at the first command word (skipping leading `VAR=value` assignments and `env`/`command`/`exec`/`time`/`nice` prefixes). Deny when:
  - `grep`/`rg`/`egrep`/`fgrep` → suggest `ash grep`
  - `find` → suggest `ash find` (extracts `-name`/`-iname` glob if present)
  - `cat`/`head`/`tail` → suggest `ash read`
  - `ls -R` (or `--recursive`) → suggest `ash find`
  - `stat` → suggest `ash stat`
  - `git status` / `git log` → suggest `ash git --op status` or `ash git --op log`
  - **Allowed git ops** (do not block): `diff`, `blame`, `show`, `add`, `commit`, `push`, `reset`, `rebase`, `checkout`, `branch`, `stash`, `tag`, `fetch`, `pull`, `init`, `remote`, `merge`, `cherry-pick`, `restore`, `switch`. Per `CLAUDE.md`, these stay in bash until the corresponding `ash git --op <name>` ships.
  - Anything else → allow.

Suggested invocations are best-effort — the goal is to give the agent a known-good starting point, not a perfect translation.

The bash command splitter is **literal** (no shell quoting awareness): a `;` inside a quoted string still splits the command. This matches the previous python implementation's behavior and is sufficient for the patterns agents actually emit; it can occasionally trip on content-bearing arguments. The fix is to feed such content via stdin (`--content -`) rather than weaken the splitter.

## A note on Read denial

An earlier iteration of this hook (when it was a Python script) denied `Read` on text files but had to remove that handler: at the time, `ash edit` and `ash write` did not exist, and the harness's `Edit`/`Write` tools refused to act unless they had previously seen a harness `Read` on the same path. Denying `Read` therefore left the agent unable to edit anything without bash workarounds. See [docs/session-notes/2026-05-08-hook-allows-read.md](session-notes/2026-05-08-hook-allows-read.md) for the original incident.

`ash edit` and `ash write` are now live, so the harness's edit workflow no longer needs harness `Read` to function. `Read` is denied symmetrically with the rest, with the image/PDF/notebook exemption preserved.

## What we deliberately did NOT do

- **Auto-rewriting** the call to an `ash` invocation. Block-and-nudge keeps friction visible and feeds session notes. Revisit once `ash report --verb hook` data shows how often agents retry correctly.
- **An escape hatch** (env var or sentinel comment). Per CLAUDE.md's spirit, friction is the feature. Drive any bypass from a real session note, not preemptively.
- **Allowlist-style permission denies** in `settings.json`. They block without a custom reason and don't cover `Bash`-side leakage. The hook is strictly more flexible.
- **Shell aliases.** Don't work for the harness `Bash` tool (non-interactive, no rc) and don't touch `Grep`/`Glob`/`Read` at all.
- **Generalizing "client-only verb" into a framework.** This is the only verb that needs it; resist the urge to abstract until a second one shows up.

## Verification

Build first: `go build -o bin/ash ./cmd/ash && go build -o bin/ashd ./cmd/ashd`.

Smoke test the decision engine by piping JSON payloads to `bin/ash hook`:

- Grep payload should produce a Claude deny pointing at `ash grep`.
- Read on a `.go` file should deny with `ash read --path …` (new behavior).
- Read on a `.png` file should allow (no output, exit 0).
- Bash `git diff` should allow.
- Bash `git status` should deny with `ash git --op status`.
- Bash `cat foo.go` should still deny with `ash read --path foo.go`.

Hook latency: `time bin/ash hook < payload.json` should land in single-digit ms cold (the python it replaces is roughly 2x slower on macOS, more on Linux).

Ledger fire: with `ashd` running, fire a few hook calls. `ash report --verb hook` should show the rows. Stop the daemon — hook still responds at the same speed; new fires silently skip the ledger row. Restart, fires resume.

End-to-end: restart the Claude Code session in this project so the new `settings.json` takes effect (hooks are loaded at session start). In a fresh session, ask Claude to "find all `.go` files under `internal/`" — the `Glob` or `Bash find` attempt should be denied and Claude should retry with `ash find`. After a few sessions, `ash report --verb hook` aggregates which rules are firing — the recursive-development signal we want.

If Claude *fights* the hook (repeated bash subshells, creative escapes), that itself is a finding — write the session note rather than weakening the hook.
