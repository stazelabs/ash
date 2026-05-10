package jail

import (
	"path/filepath"
	"sort"
	"strings"
)

// ProjectRelativizer rewrites absolute paths under the project root into
// bare repo-root-relative form. Verb-side renderers use it to drop the
// repeated project-root prefix from path-heavy responses (ASH-71).
//
// Construction snapshots:
//
//   - the active policy's project root (canonical, EvalSymlinks-resolved),
//   - the position of the walk's inputPath relative to that root, derived
//     from a canonicalized form of inputPath so macOS symlink quirks like
//     /var/folders -> /private/var/folders do not produce ".." offsets.
//
// Apply is then a pure string operation per call.
type ProjectRelativizer struct {
	inputPath   string
	relFromRoot string
	enabled     bool
}

// NewProjectRelativizer returns a relativizer keyed on inputPath. When no
// jail policy is registered or the walk root sits outside the project
// root, Apply is a no-op (returns its argument unchanged). The verb's
// own --absolute flag should short-circuit before constructing one of
// these.
func NewProjectRelativizer(inputPath string) *ProjectRelativizer {
	roots := AllowedRoots()
	if len(roots) == 0 {
		return &ProjectRelativizer{}
	}
	root := roots[0]

	canonInput := inputPath
	if abs, err := filepath.Abs(inputPath); err == nil {
		canonInput = abs
		if resolved, err := filepath.EvalSymlinks(abs); err == nil {
			canonInput = resolved
		}
	}
	relFromRoot, err := filepath.Rel(root, canonInput)
	if err != nil || strings.HasPrefix(relFromRoot, "..") {
		return &ProjectRelativizer{}
	}
	return &ProjectRelativizer{
		inputPath:   inputPath,
		relFromRoot: relFromRoot,
		enabled:     true,
	}
}

// Apply rewrites p to bare repo-root-relative form when possible. Paths
// that are not under inputPath (or, transitively, the project root) are
// returned unchanged — the agent still needs an unambiguous reference,
// and a "../..." form is not a token win.
//
// Paths the walker emits in relative form (because inputPath was passed
// relative) are already relative-to-cwd, which equals the project root
// (the daemon chdir'd at startup). Apply still goes through Rel for
// consistency; the result is identical to the input in that case.
func (r *ProjectRelativizer) Apply(p string) string {
	if r == nil || !r.enabled {
		return p
	}
	rel, err := filepath.Rel(r.inputPath, p)
	if err != nil || strings.HasPrefix(rel, "..") {
		return p
	}
	var out string
	if r.relFromRoot == "." {
		out = rel
	} else {
		out = filepath.Join(r.relFromRoot, rel)
	}
	if out == "" || out == "." {
		return p
	}
	return filepath.ToSlash(out)
}

// PrettyPath renders p in the most compact form usable in a pretty
// header or scope line. Behavior (ASH-71):
//
//   - exact match against any active path prefix → ".".
//   - otherwise, strip the longest matching prefix (with trailing /).
//   - otherwise, return p unchanged.
//
// Pure string operation; no policy required. With no active policy, p
// is returned unchanged.
func PrettyPath(p string) string {
	prefixes := PathPrefixes()
	if len(prefixes) == 0 || p == "" {
		return p
	}
	for _, pref := range prefixes {
		if p == pref {
			return "."
		}
	}
	// PathPrefixes is already longest-first; iterate in order and stop at
	// the first hit so we strip the longest matching prefix.
	sorted := make([]string, len(prefixes))
	copy(sorted, prefixes)
	sort.Slice(sorted, func(i, j int) bool { return len(sorted[i]) > len(sorted[j]) })
	for _, pref := range sorted {
		if strings.HasPrefix(p, pref+"/") {
			return p[len(pref)+1:]
		}
	}
	return p
}
