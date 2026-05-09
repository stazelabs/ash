// Package jail enforces an optional path policy across path-taking verbs.
//
// The daemon constructs a Policy from the [jail] section of ash.toml at
// startup and registers it with SetPolicy. Verb ParseArgs functions
// then call CheckPaths after extracting their path-typed args; any
// path that resolves outside the policy returns a typed proto.Error
// with code "path_denied", recorded by the ledger like any other verb
// failure.
//
// The active policy is package-level state because:
//
//   - Every verb's ParseArgs has the stable signature
//     ParseArgs(in map[string]any) (*Args, *proto.Error). Threading a
//     policy through that signature would touch every verb purely to
//     pass through state that is process-global anyway.
//   - The daemon process has exactly one policy at a time. Reload-on-
//     change is deliberately deferred (see docs/configuration.md).
//
// Tests use SetPolicy(p) and defer SetPolicy(nil) to scope a policy to
// a single test; CheckPaths is nil-safe and does nothing when no policy
// is active (matches the no-config / disabled-config default).
//
// Path resolution is symlink-aware: a path that lexically lives inside
// the project root but symlinks out is rejected. Paths that do not yet
// exist (the common case for `ash write` and `ash edit` of new files)
// resolve via EvalSymlinks of the longest existing prefix plus a clean
// lexical join of the remaining components. This is the only correct
// way to validate a path that hasn't been created yet without racing.
package jail

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/stazelabs/ash/internal/proto"
)

// Policy is the daemon's resolved path policy. Construct via FromConfig
// (preferred) so all paths are canonicalized once at startup.
type Policy struct {
	// Enabled toggles enforcement. Disabled policies always allow.
	Enabled bool
	// AllowedRoots is the canonical set of directories paths may live
	// under. The project root and any [jail].allow_paths from config
	// are folded into this list.
	AllowedRoots []string
	// DenyPaths is the canonical set of paths that are denied even when
	// they fall under an AllowedRoots entry. Subpaths of a deny entry
	// are also denied.
	DenyPaths []string
}

// FromConfig builds a Policy from a config.JailConfig analogue and a
// project root. The root and allow paths are canonicalized via
// filepath.Abs + EvalSymlinks (best-effort: paths that don't exist
// fall back to filepath.Abs only, which is the lexical answer).
//
// Callers should pass the JailConfig directly; we accept the fields
// rather than the type to avoid an import cycle with internal/config.
func FromConfig(enabled bool, root string, allowPaths, denyPaths []string) *Policy {
	canonRoot := canonicalize(root)
	roots := []string{canonRoot}
	for _, p := range allowPaths {
		if c := canonicalize(p); c != "" {
			roots = append(roots, c)
		}
	}
	denies := make([]string, 0, len(denyPaths))
	for _, p := range denyPaths {
		if c := canonicalize(p); c != "" {
			denies = append(denies, c)
		}
	}
	return &Policy{Enabled: enabled, AllowedRoots: roots, DenyPaths: denies}
}

// Check returns nil if path is allowed under the policy. A nil or
// disabled policy is always permissive — that is the no-config default.
//
// Errors carry enough context for the *proto.Error wrapper: the
// returned message names the path, but the caller should prepend the
// arg key for full agent-readable form.
func (p *Policy) Check(path string) error {
	if p == nil || !p.Enabled {
		return nil
	}
	abs := canonicalize(path)
	if abs == "" {
		return fmt.Errorf("could not resolve %q", path)
	}
	for _, deny := range p.DenyPaths {
		if pathInside(abs, deny) {
			return fmt.Errorf("%s is in jail.deny_paths (%s)", abs, deny)
		}
	}
	for _, root := range p.AllowedRoots {
		if pathInside(abs, root) {
			return nil
		}
	}
	return fmt.Errorf("%s is outside jail (allowed roots: %s)", abs, strings.Join(p.AllowedRoots, ", "))
}

// active is the daemon-wide policy. Read by CheckPaths, set by
// SetPolicy. nil means no enforcement.
var (
	mu     sync.RWMutex
	active *Policy
)

// SetPolicy installs the daemon-wide policy. Pass nil to clear (used
// by tests in cleanup defers).
func SetPolicy(p *Policy) {
	mu.Lock()
	defer mu.Unlock()
	active = p
}

// CheckPaths validates a set of {arg-key -> path} entries against the
// active policy. Returns the first denied entry as a proto.Error with
// code "path_denied" and a message naming the offending key and path.
// A nil or disabled active policy is a no-op (returns nil).
//
// The map shape is chosen to keep verb-side call sites short:
//
//   if perr := jail.CheckPaths(map[string]string{
//       "path":  a.Path,
//       "other": a.Other,
//   }); perr != nil {
//       return nil, perr
//   }
//
// Empty values in the map are skipped — verbs use empty string to mean
// "not provided" for optional path args.
func CheckPaths(paths map[string]string) *proto.Error {
	mu.RLock()
	p := active
	mu.RUnlock()
	if p == nil || !p.Enabled {
		return nil
	}
	for key, path := range paths {
		if path == "" {
			continue
		}
		if err := p.Check(path); err != nil {
			return &proto.Error{
				Code: "path_denied",
				Msg:  fmt.Sprintf("%s=%q denied: %v", key, path, err),
			}
		}
	}
	return nil
}

// canonicalize returns the absolute, symlink-resolved form of path.
// For a path that does not yet exist, the longest existing prefix is
// resolved and the remaining components are joined lexically. Returns
// an empty string only if filepath.Abs fails — which means the path is
// fundamentally malformed (very rare) and the caller should treat that
// as a denial.
func canonicalize(path string) string {
	if path == "" {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return filepath.Clean(resolved)
	}
	// Path doesn't fully exist. Resolve the longest existing prefix,
	// then re-attach the missing tail. This is correct for `ash write`
	// of a new file inside an existing directory.
	parent := abs
	tail := ""
	for {
		if _, err := os.Stat(parent); err == nil {
			if resolved, err := filepath.EvalSymlinks(parent); err == nil {
				if tail == "" {
					return filepath.Clean(resolved)
				}
				return filepath.Clean(filepath.Join(resolved, tail))
			}
			return filepath.Clean(abs)
		}
		next := filepath.Dir(parent)
		if next == parent {
			// Walked off the top — nothing exists.
			return filepath.Clean(abs)
		}
		base := filepath.Base(parent)
		if tail == "" {
			tail = base
		} else {
			tail = filepath.Join(base, tail)
		}
		parent = next
	}
}

// pathInside reports whether child is exactly equal to or sits beneath
// parent, after both have been canonicalized. Comparison is purely
// string-based on canonical paths — symlink resolution is the caller's
// responsibility (Check does it via canonicalize before calling).
func pathInside(child, parent string) bool {
	if child == parent {
		return true
	}
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	if strings.HasPrefix(rel, "..") {
		return false
	}
	if strings.Contains(rel, string(filepath.Separator)+".."+string(filepath.Separator)) {
		return false
	}
	return true
}
