# Configuration

> Status: design landed under ASH-61. This doc tracks the configuration surface as it ships.

## Context

`ashd` historically had zero structured configuration. Every knob was either a CLI flag (`--root`, `--socket`, `--log` in [cmd/ashd/main.go:24-27](../cmd/ashd/main.go#L24-L27)), an env-var lookup (`XDG_RUNTIME_DIR`, `XDG_CONFIG_HOME` in [internal/session/paths.go](../internal/session/paths.go) and [internal/registry/registry.go](../internal/registry/registry.go)), or a hardcoded constant scattered across each verb. Three open tickets all want a config file as a substrate:

- **ASH-16** — "jail" the daemon to the project root, optionally with extra allow/deny paths. Needs a policy you can write down once and have every path-taking verb honor.
- **ASH-49** — daemon resilience (read deadlines, graceful shutdown, optional concurrency cap). The cap and timeouts are exactly the kind of thing that wants a config file. _Resolved._
- **ASH-35** — option to swap the git verb's shell-out for `go-git`. Backend choice belongs in config, not in a flag the agent has to repeat. _Resolved (default switched to go-git)._

ASH-61 landed the **substrate** (the package, the file format, the schema, the load/layer/wire) plus the **first real use** (ASH-16 jail enforcement) so the substrate is proven end-to-end. ASH-49 followed up to wire the `[daemon]` section. ASH-35 wired `[git].backend` and switched the default to `go-git` (in-process, zero-dep on system git).

## Decisions

- **Format:** TOML. New dep `github.com/BurntSushi/toml` (pure-Go, satisfies the no-CGO rule).
- **Project file:** `ash.toml` at the repo root. Committed by default. No `.ash/` carve-out needed.
- **User-global file:** `$XDG_CONFIG_HOME/ash/config.toml` (with `~/.config/ash/config.toml` fallback, matching the registry path resolver in [internal/registry/registry.go:27-39](../internal/registry/registry.go#L27-L39)).
- **Layering (last wins):** compiled defaults → user-global → project → `ASH_CONFIG` env override (explicit path) → CLI flags (existing `--root`/`--socket`/`--log` only — no new flags this ticket).
- **Jail default:** `enabled = false`. Existing repos behave identically with no `ash.toml` present.
- **Initial enforcement scope:** ASH-61 landed substrate + ASH-16 jail enforcement. ASH-49 wired the `[daemon]` section: per-frame read deadlines, WaitGroup-based graceful shutdown drain, optional concurrency cap. ASH-35 wired `[git].backend` with `go-git` as the default (in-process, no system git required); `shellout` is opt-in for users who want git-CLI semantics. ASH-64 added `[hook].exclude_verbs` so individual verbs can be exempted from PreToolUse hook enforcement without removing the hook entirely.

## Schema

### `internal/config/` — load + layer + represent

```go
type Config struct {
    Daemon DaemonConfig `toml:"daemon"`
    Jail   JailConfig   `toml:"jail"`
    Git    GitConfig    `toml:"git"`
    Ledger LedgerConfig `toml:"ledger"`
    Hook   HookConfig   `toml:"hook"`
}

type DaemonConfig struct {
    MaxConcurrentHandlers int      `toml:"max_concurrent_handlers"` // 0 = unlimited; cap is opt-in
    ReadDeadline          Duration `toml:"read_deadline"`           // default 30s; per-frame socket timeout
    ShutdownGrace         Duration `toml:"shutdown_grace"`          // default 5s; bounded handler drain on SIGTERM
}

type JailConfig struct {
    Enabled    bool     `toml:"enabled"`     // default false
    AllowPaths []string `toml:"allow_paths"` // additional roots beyond project root
    DenyPaths  []string `toml:"deny_paths"`  // explicit denies even inside allowed roots
}

type GitConfig struct {
    Backend string `toml:"backend"` // "go-git" (default, in-process) | "shellout" (forks system git)
}

type LedgerConfig struct {
    MaxAge  Duration `toml:"max_age"`   // default 30d; 0 = no age limit (unbounded growth)
    MaxRows int      `toml:"max_rows"`  // default 0 = no row cap
    Vacuum  bool     `toml:"vacuum"`    // default false; PRAGMA optimize runs instead
}

type HookConfig struct {
    ExcludeVerbs []string `toml:"exclude_verbs"` // ash verb names to exempt from hook enforcement
}
```

`LedgerConfig` governs automatic cleanup at daemon startup. With defaults applied, the ledger retains 30 days of call history. Set `max_age = "0s"` to restore the old unbounded behavior.

`Duration` is a thin wrapper over `time.Duration` with an `UnmarshalText` so TOML strings like `"30s"` parse cleanly.

Files:
- `internal/config/config.go` — struct + `Defaults()` constructor
- `internal/config/load.go` — `Load(root string) (*Config, string, error)` (returns config, effective source path for logging, error)
- `internal/config/paths.go` — global path resolver (mirror [internal/registry/registry.go:27-39](../internal/registry/registry.go#L27-L39))
- `internal/config/config_test.go` — golden round-trip + layering + missing-file + partial-file tests

### `internal/jail/` — policy + check

```go
type Policy struct {
    Enabled    bool
    Root       string   // canonical project root
    AllowPaths []string // canonical extra roots
    DenyPaths  []string // canonical denies
}

func (p *Policy) Check(path string) error // returns nil if allowed (or policy disabled)

// Package-level active policy. Set at daemon startup; nil-safe.
func SetPolicy(p *Policy)
func CheckPaths(keys map[string]string) *proto.Error // returns "path_denied" with which key
```

A package-level setter is used rather than threading through every `ParseArgs`: argutil and every verb's `ParseArgs(in map[string]any) (*Args, *proto.Error)` is a stable signature, so an extra parameter would touch all 16 verbs to add what's effectively process-global state. The single daemon process has exactly one policy at a time. Tests use `jail.SetPolicy(testPolicy); defer jail.SetPolicy(nil)`.

Files:
- `internal/jail/policy.go`
- `internal/jail/policy_test.go` — in-root, out-of-root, allow-listed, deny-listed, symlink-escape (resolve via `filepath.EvalSymlinks` before compare)

## Wiring

**Daemon startup** ([cmd/ashd/main.go:36-44](../cmd/ashd/main.go#L36-L44)):

```go
if err := session.EnsureRuntimeDirs(rootFlag); err != nil { ... }

cfg, cfgPath, err := config.Load(rootFlag)
if err != nil {
    log.Fatalf("ashd: config: %v", err)
}
jail.SetPolicy(cfg.Jail.ToPolicy(rootFlag))

// ... existing log setup ...

runners := verbs.Runners(led, cfg)
```

The daemon-ready log line gains the config source: `ashd ready: root=… socket=… session=… config=ash.toml` (or `config=defaults` when no file present).

**Verb registry** ([internal/verbs/verbs.go:75](../internal/verbs/verbs.go#L75)): `Runners(led *ledger.Ledger)` becomes `Runners(led *ledger.Ledger, cfg *config.Config)`. The git verb is the only consumer this ticket; bench's self-dispatch closure is unchanged. Test harnesses ([cmd/ashd/integration_test.go:42-72](../cmd/ashd/integration_test.go#L42-L72), [cmd/ashd/main_test.go](../cmd/ashd/main_test.go)) pass `config.Defaults()`.

**Per-verb path checks**: each verb that takes a path arg gets a one-liner after parsing:

```go
// pattern, applied at the bottom of ParseArgs after all path fields are set
if perr := jail.CheckPaths(map[string]string{"path": a.Path}); perr != nil {
    return nil, perr
}
```

Verbs to touch (path-arg sites confirmed via `ash grep`):

| Verb | File | Path field(s) |
|---|---|---|
| read | [internal/verbs/read/read.go:55](../internal/verbs/read/read.go#L55) | `path` |
| find | [internal/verbs/find/find.go:76](../internal/verbs/find/find.go#L76) | `path` |
| grep | [internal/verbs/grep/grep.go:108](../internal/verbs/grep/grep.go#L108) | `path` |
| git | [internal/verbs/git/git.go:89](../internal/verbs/git/git.go#L89) | `path` |
| diff | [internal/verbs/diff/diff.go:47-50](../internal/verbs/diff/diff.go#L47-L50) | `path`, `other` |
| edit | [internal/verbs/edit/edit.go:67](../internal/verbs/edit/edit.go#L67) | `path` |
| write | [internal/verbs/write/write.go:47](../internal/verbs/write/write.go#L47) | `path` |
| stat | [internal/verbs/stat/stat.go:53-58](../internal/verbs/stat/stat.go#L53-L58) | every entry in `paths` (already split) |
| test | [internal/verbs/test/test.go:117](../internal/verbs/test/test.go#L117) | `path` |
| init | [internal/verbs/initverb/initverb.go:67](../internal/verbs/initverb/initverb.go#L67) | `path` |
| uninit | [internal/verbs/uninit/uninit.go:51](../internal/verbs/uninit/uninit.go#L51) | `path` |

`hook`, `help`, `metrics`, `report`, `bench` take no FS path arg and are skipped.

## `[hook]` — PreToolUse enforcement exclusions (ASH-64)

`HookConfig.ExcludeVerbs` is a list of ash verb names whose hook enforcement is silenced. When a verb appears in this list the hook returns **allow** for the corresponding harness tool calls and bash equivalents, instead of denying with an ash suggestion.

The allowed decision is recorded in the ledger with `matched_rule = "<rule>:excluded"` so the exclusion is queryable:

```sh
ash report --verb hook     # :excluded suffix visible in the matched_rule column
```

**Verb→rule mapping** (what each name silences):

| `exclude_verbs` entry | Rules silenced |
|---|---|
| `"grep"` | `Grep`, `Bash:grep`, `Bash:rg`, `Bash:egrep`, `Bash:fgrep` |
| `"find"` | `Glob`, `Bash:find`, `Bash:ls-R` |
| `"read"` | `Read`, `Bash:cat`, `Bash:head`, `Bash:tail` |
| `"edit"` | `Edit` |
| `"write"` | `Write` |
| `"stat"` | `Bash:stat` |
| `"git"` | `Bash:git-status`, `Bash:git-log`, `Bash:git-diff`, `Bash:git-show` |
| `"test"` | `Bash:go-test` |

**Wiring**: Unlike `[jail]`, hook exclusions are loaded client-side (not via the daemon) because `ash hook` runs in-process without starting the daemon. `runHook()` in [cmd/ash/hook.go](../cmd/ash/hook.go) calls `config.Load(root)` and injects `ExcludeVerbs` into both the typed `Args` and the fire-and-forget wire map sent to the daemon for ledger instrumentation. No changes to `cmd/ashd/main.go` are required.

## Error code

`path_denied` (proto.Error) with `Msg: "<key>=<path> outside jail"`. The verb call still records to the ledger (with `OK=false, ErrCode="path_denied"`) so deny rate is queryable via `ash report --verb <v>`.

## Sample config file

`ash.toml.example` ships at the repo root with every section commented. Users `cp ash.toml.example ash.toml` to opt in.

## Out of scope (deliberately)

- ~~ASH-49 enforcement: the schema lands but `daemon.max_concurrent_handlers`, `daemon.read_deadline`, `daemon.shutdown_grace` are not yet read by the accept loop. ASH-49 picks them up.~~ _Done — see acceptLoop / drainHandlers in [cmd/ashd/main.go](../cmd/ashd/main.go) and tests in [cmd/ashd/resilience_test.go](../cmd/ashd/resilience_test.go)._
- ~~ASH-35 enforcement: `git.backend = "go-git"` returns a typed `not_implemented` error from the git verb until ASH-35 spikes go-git.~~ _Done — go-git is the default backend. shellout is now the opt-in for callers who need full patch text on `--staged` or unstaged worktree diffs (gogit returns counts only for those modes; range diffs and show have full patch text)._
- Per-verb default-knob overrides (`read.limit_bytes`, `find.limit`, etc.). Easy to add later; not needed for this rollout.
- A new `ash config` verb (effective-config printer). Deferred.
- Hot reload on `ash.toml` change. Deferred.

## Verification

End-to-end smoke test:

```sh
# 1. Build
go build -o bin/ash ./cmd/ash
go build -o bin/ashd ./cmd/ashd

# 2. Default behavior unchanged (no ash.toml present)
bin/ash read --path /etc/hosts          # succeeds today, must still succeed
bin/ash report                          # daemon log line shows "config=defaults"

# 3. Enable jail in this repo
cat > ash.toml <<EOF
[jail]
enabled = true
allow_paths = ["/tmp"]
EOF
bin/ash stop                            # restart daemon to pick up config (no hot-reload v1)
bin/ash read --path README.md           # succeeds (inside root)
bin/ash read --path /etc/hosts          # ERROR path_denied
bin/ash read --path /tmp/whatever.txt   # succeeds (allow-listed)
bin/ash report --verb read              # shows the path_denied row in the ledger

# 4. Symlink escape
ln -s /etc/hosts hosts-link
bin/ash read --path hosts-link          # ERROR path_denied (after EvalSymlinks)

# 5. Schema-only validation
cat >> ash.toml <<EOF
[daemon]
max_concurrent_handlers = 32
[git]
backend = "go-git"
EOF
bin/ash stop && bin/ash git --op status
# git --op status returns not_implemented because go-git path is stubbed.
# daemon log line shows config=ash.toml.

rm ash.toml hosts-link                  # restore clean state
```

Automated:

```sh
go test ./internal/config/...
go test ./internal/jail/...
go test ./...                           # full suite, with the per-verb jail denial tests
```

Cross-check via the ledger that denials produce structured rows:

```sh
bin/ash metrics --verb read              # path_denied calls visible with err_code populated
```
