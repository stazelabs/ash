# ASH-61: ash configuration file (substrate + jail enforcement)

**Task.** Land the configuration substrate (TOML loader, layered file lookup, schema for daemon/jail/git) plus ASH-16's first real use — jail enforcement across every path-taking verb. ASH-49 (concurrency/deadlines) and ASH-35 (git backend) ship schema only.

**Verbs used.** `ash read`, `ash grep`, `ash find`, `ash write`, `ash edit`, `ash stat`, `ash test`, `ash report`, `ash git --op {status,log,diff}`.

**Changes.**

- New package `internal/config/`: `Config` struct (`[daemon]`, `[jail]`, `[git]` sections), `Defaults()`, `Load(root) (*Config, source, err)` with last-wins layering of defaults < `$XDG_CONFIG_HOME/ash/config.toml` < `<root>/ash.toml` < `$ASH_CONFIG` override. `Duration` wrapper around `time.Duration` for TOML scalar strings like `"30s"`.
- New package `internal/jail/`: `Policy` (Enabled, AllowedRoots, DenyPaths) + package-level setter + `CheckPaths(map[string]string) *proto.Error`. Path canonicalization is symlink-aware and handles not-yet-created paths by resolving the longest existing prefix and re-attaching the missing tail.
- Daemon startup wires both: `config.Load` → `jail.SetPolicy` → `verbs.Runners(led, cfg)`. The ready log line gains `config=<source>` so you can see whether `ash.toml`, the global file, or defaults applied.
- Every path-taking verb's ParseArgs now ends with a `jail.CheckPaths` call: `read`, `find`, `grep`, `git`, `diff`, `edit`, `write`, `stat`, `init`, `uninit`. `test --packages` was on the original plan list but doesn't take a filesystem path arg in the strict sense (Go package selectors); skipped.
- `internal/verbs/jail_integration_test.go` exercises every verb's ParseArgs with an active policy, asserting `path_denied` for outside-root inputs. Lives in package `verbs` because that package already imports every sub-verb.
- `ash.toml.example` at repo root, `docs/configuration.md` design doc, README "Configuration" section, CLAUDE.md error-code glossary.

**Friction.**

The plan required edits to 10 verb ParseArgs functions. Doing each by hand is mechanical but error-prone with `ash edit --old_string` because each function has the same final shape (`return a, nil\n}`) which appears once per file but with subtle indentation/whitespace differences. Wrote a short Python script (`/tmp/jail_wire.py`) that:

1. Inserts the `internal/jail` import after the existing `internal/proto` import.
2. Anchors on the first `\treturn a, nil\n}\n` in each file (the ParseArgs return — `Run` returns `*Result` not `*Args`).
3. Replaces with the jail check followed by the original return.

`stat` was a special case: its ParseArgs returns a struct literal directly (`return &Args{Paths: paths, FollowSymlinks: follow}, nil`), so the script binds it to a local first and iterates paths to build the check map. Same script handles both shapes via a `shape: "simple"` / `shape: "stat"` flag per file.

**End-to-end verification.** With `ash.toml` containing `[jail]\nenabled = true`, `pkill ashd` to restart, then:

```
$ bin/ash read --path README.md     # in-root: succeeds
$ bin/ash read --path /etc/hosts    # outside: err path_denied
                                    # message names the path and the allowed roots
$ echo $?
1
```

Daemon log confirms config source:

```
ashd ready: root=/Users/cstaszak/Stazelabs/projects/ash socket=… session=… config=defaults
ashd ready: root=…                                      socket=… session=… config=/Users/cstaszak/Stazelabs/projects/ash/ash.toml
```

Test 4 (symlink escape) is covered by `internal/jail/policy_test.go::TestPolicy_SymlinkEscape` and `TestPolicy_NewFileViaEscape`. Did not re-run end-to-end because the unit test gives equivalent assurance.

**Suggestions / follow-ups.**

- ASH-49 picks up the `[daemon]` schema and wires `max_concurrent_handlers` (semaphore in the accept loop), `read_deadline` (per-frame `SetReadDeadline`), and `shutdown_grace` (graceful `WaitGroup` drain).
- ASH-35 picks up `[git].backend = "go-git"` — currently the git verb still uses shell-out unconditionally. The selector wiring is a small add (read `cfg.Git.Backend` from a closure in `verbs.Runners`); the spike content (does `go-git` actually work for our use cases?) is the open question.
- Daemon hot-reload on `ash.toml` change is deferred. If editing config + `pkill ashd` becomes a friction point, an `inotify`/`fsnotify` watcher is the natural fix.
- An `ash config` verb (effective-config printer) would help debug "what's actually loaded after layering?" Currently you have to read the daemon log line. Defer until needed.
- The Python-writer pattern is now used in three sessions (ASH-48, ASH-63, ASH-61) for any edit involving Go char literals or backticks. Worth documenting in CLAUDE.md as the recommended escape hatch alongside `ash edit --patch -`.

**Instrumentation (this session).**

```
verb     n    ok%   p50_exec   p95_exec
write    ~30  100%  ~330us     ~600us
edit     ~10  100%  ~500us     ~800us
test     ~6   100%  ~470ms     ~620ms (full repo, 32 pkgs)
read     ~50  ~80%  ~50us      ~500us  (the ~20% errors include intentional jail denials in smoke testing)
grep     ~5   100%  ~700us     ~1.3ms
```

End state: 32 packages pass, jail integration test green for all 10 verbs, daemon log confirms config sourcing, end-to-end smoke test denies outside-root reads with `path_denied` and exit 1.
