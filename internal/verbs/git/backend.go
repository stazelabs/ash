package git

// Backend selects between the shell-out implementation (forks system
// git) and the in-process go-git implementation. Wiring lives here so
// the dispatcher in git.go and per-op runners stay backend-agnostic.
//
// Three states, set from [git].backend in ash.toml:
//
//   - unset (backendDefault) — the recommended default. log/show/blame
//     run in-process via go-git; status and diff prefer the shellout
//     path when system git is on PATH and fall back to go-git when it
//     is not. go-git's status/diff walk the entire worktree, including
//     large gitignored trees like node_modules, and run ~25x slower
//     than system git on JS-scale repos (ASH-203) — so the two
//     worktree-walking ops delegate to git when it is available.
//   - "go-git" — every op in-process, no system-git dependency, even
//     where it is slower. For hermetic environments or when go-git's
//     semantics are specifically wanted.
//   - "shellout" — every op via the system git subprocess, including
//     its native rename thresholds and porcelain v2 semantics. blame
//     is not_implemented (no porcelain-v2 blame parser yet).

import (
	"fmt"
	"os/exec"
	"sync/atomic"

	"github.com/stazelabs/ash/internal/proto"
)

type backendKind int32

const (
	// backendDefault is the zero value — no [git].backend in ash.toml.
	// status/diff prefer shellout when git is on PATH (ASH-203);
	// log/show/blame run on go-git.
	backendDefault backendKind = iota
	// backendGogit — [git].backend = "go-git": every op in-process.
	backendGogit
	// backendShellout — [git].backend = "shellout": every op via the
	// system git subprocess.
	backendShellout
)

// active is the daemon-wide backend selector. Atomic so that tests can
// flip it without a mutex; in production it is set once at daemon
// startup. Default zero value is backendDefault.
var active atomic.Int32

// gitOnPath records whether system `git` resolved on PATH. Evaluated
// once at package load — the daemon's PATH is stable for its lifetime,
// so the LookPath cost is paid a single time. Tests may override it to
// exercise the no-system-git fallback. ASH-203.
var gitOnPath = func() bool {
	_, err := exec.LookPath("git")
	return err == nil
}()

// SetBackend selects the active backend by config-string name. Returns
// an error for unknown names so the daemon can fail loudly at startup
// rather than silently fall through to a default.
//
// Accepted values:
//   - "" → backendDefault: status/diff prefer shellout when system git
//     is available, every other op go-git. What a missing [git].backend
//     resolves to, and the recommended default.
//   - "go-git" (or "gogit") → every op in-process via go-git.
//   - "shellout" → every op via the system git subprocess.
func SetBackend(name string) error {
	switch name {
	case "":
		active.Store(int32(backendDefault))
	case "go-git", "gogit":
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

// statusDiffShellout decides the backend for status and diff — the two
// ops whose cost is dominated by walking the worktree. Explicit
// "shellout" always shells out; explicit "go-git" always stays
// in-process; the default config shells out when system git is on PATH
// and falls back to go-git when it is not (ASH-203).
func statusDiffShellout() bool {
	switch currentBackend() {
	case backendShellout:
		return true
	case backendGogit:
		return false
	default: // backendDefault
		return gitOnPath
	}
}

// runStatus dispatches the status op. See statusDiffShellout.
func runStatus(a *Args, tr *proto.Tracer) (*StatusResult, *proto.Error) {
	if statusDiffShellout() {
		return runStatusShellout(a, tr)
	}
	return runStatusGogit(a, tr)
}

// runDiff dispatches the diff op. See statusDiffShellout.
func runDiff(a *Args, tr *proto.Tracer) (*DiffResult, *proto.Error) {
	if statusDiffShellout() {
		return runDiffShellout(a, tr)
	}
	return runDiffGogit(a, tr)
}

func runLog(a *Args, tr *proto.Tracer) (*LogResult, *proto.Error) {
	switch currentBackend() {
	case backendShellout:
		return runLogShellout(a, tr)
	default:
		return runLogGogit(a, tr)
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
