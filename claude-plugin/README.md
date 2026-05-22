# ash — Claude Code plugin

A Claude Code [skill](skills/ash/SKILL.md) that teaches an agent to reach
for [ash](https://github.com/stazelabs/ash) verbs — `grep`, `find`,
`read`, `write`, `edit`, `diff`, `git`, `stat` — for file and code
operations, instead of bash or the built-in file tools.

The skill is **guidance only**. It does not install ash. For the agent to
actually call the verbs you still need either:

- the `ash` CLI on `$PATH` (`brew install stazelabs/tap/ash` — see
  [docs/adoption/install.md](../docs/adoption/install.md)), and/or
- the `ashmcp` MCP server registered, so the verbs appear as `ash_*`
  tools — see [docs/adoption/claude-code.md](../docs/adoption/claude-code.md).

The skill makes the agent *use* what's installed; the MCP server and CLI
are what's installed.

## Install the skill

### Option A — copy the skill directory

Personal (all your projects):

```sh
cp -r claude-plugin/skills/ash ~/.claude/skills/ash
```

Per-project (one repo):

```sh
cp -r claude-plugin/skills/ash /path/to/repo/.claude/skills/ash
```

Claude Code discovers skills under those paths on session start; an
already-running session picks up the new directory after a restart.

### Option B — load as a plugin

Point Claude Code at this directory:

```sh
claude --plugin-dir /path/to/ash/claude-plugin
```

Once the plugin is published to a marketplace, `/plugin install ash` will
be the one-step path. Until then, Option A or `--plugin-dir` is the way.

## Verify

In a Claude Code session, run `/ash` to load the skill manually, or ask
something file-shaped ("search the repo for every TODO") and confirm the
agent reaches for an `ash_grep` / `ash grep` call. `ash report --since 5m`
in the project shows the calls in the ledger.
