// Package config loads ashd's structured configuration from TOML files
// at the project root and the user-global directory, layered against
// compiled defaults.
//
// File locations and layering are documented in docs/configuration.md.
// In short: defaults < $XDG_CONFIG_HOME/ash/config.toml < <root>/ash.toml
// < $ASH_CONFIG (an explicit-path override). Each layer's non-zero
// fields shadow the layers below it; absent fields keep the lower
// layer's value.
//
// This package only parses and represents config. Enforcement lives in
// the consumers — internal/jail wires the [jail] section into per-verb
// path checks; [daemon] and [git] sections are accepted today but their
// enforcement ships under ASH-49 and ASH-35 respectively.
package config

import (
	"fmt"
	"time"
)

// Config is the top-level layout written to ash.toml. Sub-structs map
// 1:1 to TOML tables.
type Config struct {
	Daemon DaemonConfig `toml:"daemon"`
	Jail   JailConfig   `toml:"jail"`
	Git    GitConfig    `toml:"git"`
	Ledger LedgerConfig `toml:"ledger"`
}

// DaemonConfig collects daemon-process knobs. None of these are
// enforced today; ASH-49 will pick them up.
type DaemonConfig struct {
	// MaxConcurrentHandlers caps in-flight verb dispatches. 0 = unlimited.
	MaxConcurrentHandlers int `toml:"max_concurrent_handlers"`
	// ReadDeadline is the per-frame read timeout on a connection. 0 = no deadline.
	ReadDeadline Duration `toml:"read_deadline"`
	// ShutdownGrace is how long the daemon waits for in-flight handlers
	// after the listener closes. 0 = exit immediately (current behavior).
	ShutdownGrace Duration `toml:"shutdown_grace"`
}

// JailConfig governs path validation across path-taking verbs.
//
// When Enabled is true, every absolute or canonicalized verb path arg
// must resolve under the project root or one of AllowPaths, and must
// not match any DenyPaths. A symlink whose target escapes the allowed
// roots is also rejected (jail.Check resolves with EvalSymlinks before
// comparing).
type JailConfig struct {
	// Enabled is the master switch. Defaults to false so existing repos
	// behave identically with no ash.toml present.
	Enabled bool `toml:"enabled"`
	// AllowPaths lists additional canonical roots paths may reside under,
	// beyond the project root.
	AllowPaths []string `toml:"allow_paths"`
	// DenyPaths lists canonical paths to reject even if they sit inside
	// the project root or AllowPaths. Useful for e.g. .env or secrets
	// directories.
	DenyPaths []string `toml:"deny_paths"`
}

// GitConfig selects the git verb's backend.
//
// "shellout" (default) preserves today's behavior — fork+exec to system
// git and parse machine-readable output. "go-git" is reserved for
// ASH-35 and currently returns a typed not_implemented error from the
// git verb so the schema is exercisable without the backend being live.
type GitConfig struct {
	Backend string `toml:"backend"`
}

// LedgerConfig controls automatic cleanup of the per-project SQLite ledger.
// Cleanup runs once at daemon startup before the accept loop opens.
type LedgerConfig struct {
	// MaxAge is how long to retain call rows. 0 = no age limit (unbounded growth).
	MaxAge Duration `toml:"max_age"`
	// MaxRows caps the total number of call rows kept after age-based cleanup.
	// 0 = no row limit.
	MaxRows int `toml:"max_rows"`
	// Vacuum runs PRAGMA VACUUM after cleanup. This rewrites the DB file and
	// reclaims disk space but is slow on large ledgers. Default false; PRAGMA
	// optimize runs instead, which is cheap and sufficient for routine maintenance.
	Vacuum bool `toml:"vacuum"`
}

const (
	// GitBackendShellout is the default backend value. Everything that
	// works today continues to work without an ash.toml entry.
	GitBackendShellout = "shellout"
	// GitBackendGoGit reserves the in-process backend selector for
	// ASH-35. Setting backend = "go-git" today makes the git verb
	// return not_implemented; the schema is otherwise live.
	GitBackendGoGit = "go-git"
)

// DefaultReadDeadline is the per-frame socket read timeout used when
// ash.toml does not override it. Generous enough that any legit client
// pause is fine; aggressive enough that a connection that never sends
// (or is half-closed) cleans up promptly instead of pinning a goroutine
// forever. Override via [daemon].read_deadline.
const DefaultReadDeadline = 30 * time.Second

// DefaultShutdownGrace is the bounded wait for in-flight handlers after
// SIGTERM closes the listener. 5s is enough for any verb in the current
// surface to finish; long-running ones (test, bench) are best killed
// with SIGKILL if the user wanted them gone.
const DefaultShutdownGrace = 5 * time.Second

// DefaultLedgerMaxAge is the default retention window for ledger call rows.
// Rows older than this are deleted at each daemon startup.
const DefaultLedgerMaxAge = 30 * 24 * time.Hour

// Defaults returns the compiled-in baseline, used as the lowest layer
// when nothing is configured.
func Defaults() *Config {
	return &Config{
		Daemon: DaemonConfig{
			MaxConcurrentHandlers: 0, // unlimited; cap is opt-in.
			ReadDeadline:          Duration(DefaultReadDeadline),
			ShutdownGrace:         Duration(DefaultShutdownGrace),
		},
		Jail:   JailConfig{},
		Git:    GitConfig{Backend: GitBackendGoGit},
		Ledger: LedgerConfig{MaxAge: Duration(DefaultLedgerMaxAge)},
	}
}

// Duration wraps time.Duration so TOML strings like "30s" parse via the
// standard library's time.ParseDuration. Zero value means "unset"; the
// consumer interprets that as "no deadline" / "default behavior".
type Duration time.Duration

// UnmarshalText is called by BurntSushi/toml for any string-typed
// scalar bound to a Duration field. Empty strings parse to zero.
func (d *Duration) UnmarshalText(text []byte) error {
	s := string(text)
	if s == "" {
		*d = 0
		return nil
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	*d = Duration(v)
	return nil
}

// MarshalText emits the canonical Go duration string (e.g. "30s").
// Round-trips cleanly through TOML.
func (d Duration) MarshalText() ([]byte, error) {
	return []byte(time.Duration(d).String()), nil
}

// AsDuration converts back to a plain time.Duration for use at call
// sites that don't care about the wire type.
func (d Duration) AsDuration() time.Duration { return time.Duration(d) }
