package jail

import (
	"fmt"
	"path/filepath"
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
	// PathPrefixes is already longest-first (its contract), so the first
	// prefix that matches is the longest — strip it and return.
	for _, pref := range prefixes {
		if strings.HasPrefix(p, pref+"/") {
			return p[len(pref)+1:]
		}
	}
	return p
}

// PrefixAliasTable assigns compact @N aliases to jail allow_paths entries
// (the non-project-root roots in AllowedRoots) to reduce per-line token
// cost when pretty responses include paths outside the project root (ASH-85).
// When allow_paths is empty, the table is empty and all methods are no-ops.
//
// Only emitted in pretty output; JSON/msgpack wire data is unchanged.
type PrefixAliasTable struct {
	display  []string   // canonical path shown in @N = ... header line
	variants [][]string // all path forms that map to @N (canonical + macOS /private variant)
}

// NewPrefixAliasTable builds a table from AllowedRoots()[1:] — the
// allow_paths entries beyond the project root. Returns an empty table when
// no allow_paths entries are configured.
func NewPrefixAliasTable() *PrefixAliasTable {
	roots := AllowedRoots()
	if len(roots) <= 1 {
		return &PrefixAliasTable{}
	}
	extra := roots[1:]
	t := &PrefixAliasTable{
		display:  make([]string, len(extra)),
		variants: make([][]string, len(extra)),
	}
	for i, root := range extra {
		t.display[i] = root
		vars := []string{root}
		// Include the /private-stripped form so macOS paths passed by the
		// agent before EvalSymlinks-resolution also match (mirrors PathPrefixes).
		if stripped, ok := strings.CutPrefix(root, "/private"); ok {
			vars = append(vars, stripped)
		}
		t.variants[i] = vars
	}
	return t
}

// Empty reports whether the table has no entries (allow_paths is not configured).
func (t *PrefixAliasTable) Empty() bool {
	return t == nil || len(t.display) == 0
}

// Apply rewrites p to @N or @N/<tail> if p falls under an aliased prefix.
// Returns p unchanged when no alias matches or the table is empty.
func (t *PrefixAliasTable) Apply(p string) string {
	if t.Empty() {
		return p
	}
	for i, vars := range t.variants {
		for _, pref := range vars {
			if p == pref {
				return fmt.Sprintf("@%d", i)
			}
			if strings.HasPrefix(p, pref+"/") {
				return fmt.Sprintf("@%d/%s", i, p[len(pref)+1:])
			}
		}
	}
	return p
}

// Header returns the alias definition block to prepend to a pretty response:
//
//	@0 = /Users/me/scratch
//	@1 = /opt/vendored/foo
//
// Returns an empty string when the table is empty.
func (t *PrefixAliasTable) Header() string {
	if t.Empty() {
		return ""
	}
	var sb strings.Builder
	for i, disp := range t.display {
		fmt.Fprintf(&sb, "@%d = %s\n", i, disp)
	}
	return sb.String()
}
