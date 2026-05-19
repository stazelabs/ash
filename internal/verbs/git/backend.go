package git

// Backend selects between the shell-out implementation (forks system git)
// and the in-process go-git implementation. Wiring lives here so the
// dispatcher in git.go and per-op runners stay backend-agnostic.
//
// Default is go-git: agents working in fresh environments may not have
// system git on PATH, and the original motivation for ash is "remove
// dependencies on bash-shaped tooling." Setting [git].backend = "shellout"
// in ash.toml restores the system-git behavior, including its native
// rename detection thresholds and porcelain v2 semantics for cases
// where they matter.

import (
	"fmt"
	"sync/atomic"

	"github.com/stazelabs/ash/internal/proto"
)

type backendKind int32

const (
	backendGogit backendKind = iota
	backendShellout
)

// active is the daemon-wide backend selector. Atomic so that tests can
// flip it without a mutex; in production it is set once at daemon
// startup. Default zero value is go-git.
var active atomic.Int32

// SetBackend selects the active backend by config-string name. Returns
// an error for unknown names so the daemon can fail loudly at startup
// rather than silently fall through to a default.
//
// Accepted values:
//   - "go-git" (or "gogit") → in-process via github.com/go-git/go-git/v5.
//   - "shellout" → system git subprocess.
//
// Empty string is treated as "go-git" so a missing [git].backend in
// ash.toml resolves to the default cleanly.
func SetBackend(name string) error {
	switch name {
	case "", "go-git", "gogit":
		active.Store(int32(backendGogit))
	case "shellout":
		active.Store(int32(backendShellout))
	default:
		return fmt.Errorf("unknown git backend %q (expected \"go-git\" or \"shellout\")", name)
	}
	return nil
}

// currentBackend returns the active backend kind. Used by per-op
// dispatchers below.
func currentBackend() backendKind {
	return backendKind(active.Load())
}

// notImplementedBackend returns the typed error for an op a backend
// genuinely cannot perform. ASH-190: blame is the first live op with a
// real backend split — go-git implements it, shellout does not (no
// demand to justify a porcelain-v2 parser yet).
func notImplementedBackend(op string) *proto.Error {
	return &proto.Error{Code: "not_implemented", Msg: op + " is not implemented for the active git backend"}
}

// runStatus dispatches by active backend.
func runStatus(a *Args, tr *proto.Tracer) (*StatusResult, *proto.Error) {
	switch currentBackend() {
	case backendShellout:
		return runStatusShellout(a, tr)
	default:
		return runStatusGogit(a, tr)
	}
}

func runLog(a *Args, tr *proto.Tracer) (*LogResult, *proto.Error) {
	switch currentBackend() {
	case backendShellout:
		return runLogShellout(a, tr)
	default:
		return runLogGogit(a, tr)
	}
}

func runDiff(a *Args, tr *proto.Tracer) (*DiffResult, *proto.Error) {
	switch currentBackend() {
	case backendShellout:
		return runDiffShellout(a, tr)
	default:
		return runDiffGogit(a, tr)
	}
}

func runShow(a *Args, tr *proto.Tracer) (*ShowResult, *proto.Error) {
	switch currentBackend() {
	case backendShellout:
		return runShowShellout(a, tr)
	default:
		return runShowGogit(a, tr)
	}
}

func runBlame(a *Args, tr *proto.Tracer) (*BlameResult, *proto.Error) {
	switch currentBackend() {
	case backendShellout:
		return nil, notImplementedBackend("blame")
	default:
		return runBlameGogit(a, tr)
	}
}
