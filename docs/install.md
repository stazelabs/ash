# Plan — installing ash into target repos for ledger capture

## Context

`ash` is in Phase 2; the recursive-development premise says agents working on
*any* repo should drive `ash` so we collect ledger data and feed the next
phase's design. Today the only repo that benefits is ash itself: there is
no install workflow, the hook config in `.claude/settings.json` hardcodes
`$CLAUDE_PROJECT_DIR/bin/ash`, and re-copying binaries on every rebuild
would dominate the friction.

We want a one-shot `make install` that puts ash on `$PATH` once, plus an
`ash init` verb that bootstraps any target repo (hook config, gitignore,
registry entry) idempotently. Cross-repo report aggregation rounds out
the install→capture→analyze loop so a session in the ash repo can analyze
ledgers captured in other repos.

User decisions (confirmed): **symlink-on-`$PATH`** install model;
**include** cross-repo ledger analysis in this plan.

## Scope

Three pieces, in order of dependency:

1. `make install` / `make uninstall` — symlink binaries onto `$PATH`.
2. `ash init` verb — wire up a target repo's `.claude/settings.json`,
   `.gitignore`, and append to a global registry of installed roots.
3. `ash report --root <p>` and `ash report --all-roots` — read/aggregate
   foreign ledgers using the registry.

Out of scope: the friction note in
[docs/encoding-results.md](encoding-results.md)
about the hook denying writes outside project root — tracked separately.

## 1. `make install` / `make uninstall`

Edit [Makefile](../Makefile). Add two targets:

```make
PREFIX ?= $(HOME)/.local/bin

install: all
	mkdir -p $(PREFIX)
	ln -sf $(CURDIR)/bin/ash  $(PREFIX)/ash
	ln -sf $(CURDIR)/bin/ashd $(PREFIX)/ashd
	@echo "installed: $(PREFIX)/{ash,ashd}"

uninstall:
	rm -f $(PREFIX)/ash $(PREFIX)/ashd
```

Symlinks (not copies) so a rebuild of ash auto-updates every target. The
existing `killStaleIfNeeded` logic in
[cmd/ash/main.go:399](../cmd/ash/main.go#L399)
already compares the ashd binary mtime against the socket and restarts
the daemon when stale — `os.Stat` follows symlinks, so this Just Works
across all target repos on the next call after a rebuild.

PATH check on install — emit a warning if `$(PREFIX)` isn't on `$PATH`.

## 2. `ash init` verb

New verb, server-handled (so the call instruments itself in the target
repo's ledger). Flags:

- `--path <p>` (default `.`) — target repo root.
- `--force` — overwrite an existing differing PreToolUse entry.
- `--no_registry` — skip writing to the global installed-repos registry
  (escape hatch for ephemeral repos).

Behavior:

1. Resolve `<p>` to an absolute path; validate it's a directory.
2. Read `<p>/.claude/settings.json` if present, else start with `{}`.
3. Merge a PreToolUse entry whose command is exactly `"ash hook"` (PATH
   form). Detection rule: any entry whose `hooks[].command` contains the
   substring `ash hook` and whose matcher is a superset of
   `Grep|Glob|Bash|Edit|Write|Read` is treated as already-installed.
   - Already installed → no-op, exit OK.
   - Different ash command (e.g. `$CLAUDE_PROJECT_DIR/bin/ash hook`) →
     leave untouched and print a warning unless `--force`.
   - Absent → append a new entry.
4. Write `.claude/settings.json` back (`json.MarshalIndent`, 2 spaces).
   Create `.claude/` if missing.
5. If `<p>/.gitignore` exists and lacks a `.ash/` line, append it (with
   a leading newline if the file doesn't end in one). Skip silently if
   no `.gitignore`.
6. Unless `--no_registry`, append the absolute root to
   `$XDG_CONFIG_HOME/ash/installed-repos.txt` (fallback `~/.config/ash/`),
   deduplicated. Create the file if missing.
7. Result envelope:
   - `path` (absolute root)
   - `settings_written` (bool)
   - `gitignore_updated` (bool)
   - `registry_updated` (bool)
   - `already_installed` (bool, set when the merge was a no-op)
   - `warnings []string`

Mirror an `ash uninit --path <p>` verb that removes the hook entry, the
gitignore line, and the registry row. Useful for clean teardown.

### Files to modify / create

- New: `internal/verbs/init/init.go` — verb implementation.
- New: `internal/verbs/init/init_test.go` — unit tests covering
  fresh repo, already-installed repo, conflicting hook entry,
  no-gitignore repo.
- Modify: [internal/server/server.go](../internal/server/server.go)
  (or wherever verbs are registered) — register `init` and `uninit`.
- Modify: [cmd/ash/main.go](../cmd/ash/main.go) — verb argument schema and
  pretty renderer.
- Modify: [internal/proto/proto.go](../internal/proto/proto.go) —
  add `InitArgs` / `InitResult` types.
- Modify: [internal/proto/pretty.go](../internal/proto/pretty.go) —
  pretty form for the result.

### Reuse

- `session.Root(cwd)` from
  [internal/session/paths.go](../internal/session/paths.go)
  for resolving the target root if `--path .`.
- The temp-file + rename pattern already used in
  [internal/verbs/write/](../internal/verbs/write/)
  for atomic settings.json write.

## 3. `ash report --root <p>` and `--all-roots`

Extend the existing `report` verb (no new verb). Flags:

- `--root <p>` — open `<p>/.ash/ledger.db` instead of the daemon's own
  ledger. Mutually exclusive with `--all-roots`.
- `--all-roots` — read the installed-repos registry, open each ledger,
  aggregate per-verb counts/percentiles across all of them; pretty form
  shows a per-repo breakdown column.

Implementation note: the daemon's `report` handler currently opens its
own ledger via the connected `ledger.DB`. Refactor to accept an explicit
ledger path, defaulting to the daemon's. Foreign ledgers open read-only
(`?mode=ro` in the SQLite DSN) so a running daemon in the foreign repo
isn't disturbed.

Files to modify:

- [internal/verbs/report/report.go](../internal/verbs/report/report.go)
  — add flags, foreign-ledger open path, aggregation.
- [cmd/ash/main.go](../cmd/ash/main.go) — argument schema entries.
- New: `internal/registry/registry.go` (or fold into `internal/session/`)
  — read/write `installed-repos.txt`. Shared by `init`, `uninit`, `report`.

## Documentation

- Update [README.md](../README.md) — add an "Installing into a target
  repo" section walking through `make install` →
  `cd ../target && ash init`.
- Update [CLAUDE.md](../CLAUDE.md) — bash whitelist note, and a
  one-liner under "How to invoke ash" pointing at the install verbs.
- The `init` and `uninit` verbs go into the live-verbs section.

## Verification

End-to-end, in order:

1. `make install` from the ash repo. `which ash && which ashd` resolves
   inside `$PREFIX`. `ash help` works from any cwd.
2. `cd ~/some-other-repo && ash init`. Inspect:
   - `.claude/settings.json` contains the PreToolUse hook with command
     `"ash hook"`.
   - `.gitignore` ends with `.ash/`.
   - `~/.config/ash/installed-repos.txt` contains the absolute root.
3. From the same other repo: `ash find --path . --max_depth 1`. Confirm
   `.ash/ledger.db` is created and `ash metrics --last 5` shows the
   call. The hook fires for harness Read/Bash and denies them.
4. Re-run `ash init` — exits with `already_installed=true`, no file
   changes.
5. From the ash repo: `ash report --root ~/some-other-repo` returns the
   foreign repo's per-verb summary; `ash report --all-roots` returns an
   aggregate across both ash and the target.
6. `ash uninit --path ~/some-other-repo` removes the hook entry,
   gitignore line, and registry row. Re-running `ash init` reinstalls
   cleanly.
7. Rebuild ash (`make all`); next `ash` call from any target repo
   restarts that repo's daemon (verify by checking session id in the
   ledger changes). No manual update step in any target.
8. Unit tests: `ash test --packages internal/verbs/init,internal/verbs/report`
   pass, including the conflicting-hook and no-gitignore branches.
