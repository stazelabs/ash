# Installing ash

The fastest path to a working `ash` — no clone, no `make`. Homebrew
installs all four binaries (`ash`, `ashd`, `ashmcp`, `ashd-clean`) from a
single tagged release, so the client, daemon, and MCP adapter never
disagree on the wire protocol.

Building from source is still supported and is the right path for
contributors — see [README §Quick start](../../README.md#quick-start).

## Homebrew (macOS / Linux)

```sh
brew install stazelabs/tap/ash
```

That one command taps `stazelabs/homebrew-tap` and installs the cask.
Binaries land on the Homebrew prefix's `bin` — `/opt/homebrew/bin`
(Apple Silicon), `/usr/local/bin` (Intel macOS), or
`/home/linuxbrew/.linuxbrew/bin` (Linux) — all of which are on `$PATH`
by default.

To upgrade later:

```sh
brew upgrade ash
```

## Verify

```sh
ash --version          # prints the release version
ash help               # lists every live verb
```

`ash` auto-starts the daemon (`ashd`) on first use; `ashd` and `ashmcp`
are resolved as siblings of `ash` in the same `bin` directory, so no
extra `$PATH` setup is needed.

## Per-project setup

`ash` works in any directory without configuration. To add the
PreToolUse hook that steers a coding agent's built-in tools to `ash`,
run once per repo:

```sh
cd /path/to/your/project
ash init               # adds the hook to .claude/settings.json,
                       # appends .ash/ to .gitignore, registers the root
```

`ash init` also drops an `ash.toml` you can edit for jail policy, git
backend, and the `[runner]` test/build commands. Restart the daemon with
`ash stop` after editing it. To remove the hook later: `ash uninit`.

## Wire ash into Claude Code (MCP)

`ashmcp` exposes the read- and write-side verbs as MCP tools
(`ash_grep`, `ash_read`, `ash_write`, `ash_edit`, …) alongside Claude
Code's built-ins. Register it once, user-scoped:

```sh
claude mcp add --scope user ash "$(which ashmcp)"
```

Or, to scope it to a single repository, write a `.mcp.json` at the repo
root:

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

Use the **absolute path** from `which ashmcp` — Claude Code does not
expand environment variables in an MCP `command` field, and launches
MCP servers without your interactive shell's `$PATH`. See
[claude-code.md](claude-code.md) for the full walkthrough, the
`tools/list` shape, and troubleshooting.

## Version skew

The cask installs `ash`, `ashd`, `ashmcp`, and `ashd-clean` together, so
a `brew`-managed install is always self-consistent. Skew happens only
when you **mix install methods** — e.g. a `brew`-installed `ash` finds a
`make`-built `ashd` earlier on `$PATH`.

Symptom: a verb call fails with a protocol-version error, or the daemon
log (`.ash/ashd.log`) reports a version mismatch on the handshake.

Fix: don't straddle install methods. Either go all-Homebrew, or all
source build. If you build from source for development, keep that `bin/`
off `$PATH` for shells that should use the Homebrew install. `ash stop`
clears a stale daemon; the next call auto-starts the right one.

## Troubleshooting

- **macOS: "cannot be opened because the developer cannot be verified".**
  The release binaries are not yet code-signed. `brew install` strips the
  quarantine attribute from the cask's binaries, so the Homebrew path is
  unaffected — but a binary downloaded straight from the GitHub Release
  and run by hand will be blocked. Clear the attribute on the extracted
  binaries:

  ```sh
  xattr -dr com.apple.quarantine <dir-with-extracted-binaries>
  ```

- **`brew install` can't find the cask.** Confirm the tap is reachable:
  `brew tap stazelabs/tap` then retry. A private tap repo needs a
  GitHub login that can read it (`brew` uses your `gh`/`git` credentials).

- **`ash: command not found` after install.** Confirm the Homebrew
  prefix `bin` is on `$PATH` (`brew --prefix`/bin). Open a fresh shell.

## Uninstall

```sh
brew uninstall ash
brew untap stazelabs/tap   # optional — drops the tap entirely
```

Per-project `.ash/` ledger directories are left in place for
retroactive analysis; remove them by hand if you want them gone.
