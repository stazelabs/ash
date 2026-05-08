// Package gitignore wraps the sabhiram/go-gitignore matcher with a tiny API
// shaped for ash verbs that walk the filesystem.
//
// Scope (deliberately limited for now):
//   - One .gitignore file at the load root. Nested .gitignore files are not
//     yet honored. Verbs document this.
//   - No global gitignore (~/.gitconfig core.excludesFile) and no
//     .git/info/exclude. Add when needed.
package gitignore

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	ignore "github.com/sabhiram/go-gitignore"
)

// Matcher tests whether a path is excluded by a loaded .gitignore file.
// Match input is normalized to a forward-slash path relative to the
// matcher's load root before testing.
//
// A nil *Matcher is valid and excludes nothing -- callers can hold a *Matcher
// uniformly whether or not a .gitignore was found.
type Matcher struct {
	rules *ignore.GitIgnore
	root  string
}

// LoadFromDir reads .gitignore at dir if present. Returns (nil, nil) when no
// .gitignore exists (the common case for many directories). A non-nil error
// only signals filesystem trouble reading an existing file.
func LoadFromDir(dir string) (*Matcher, error) {
	path := filepath.Join(dir, ".gitignore")
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	if info.IsDir() {
		return nil, nil
	}
	rules, err := ignore.CompileIgnoreFile(path)
	if err != nil {
		return nil, err
	}
	return &Matcher{rules: rules, root: dir}, nil
}

// Excludes reports whether p is excluded by the loaded rules. isDir tells the
// matcher whether the path refers to a directory; this matters because
// directory-only patterns like "bin/" only match when the path is a directory.
// The path is normalized relative to the matcher's load root and converted to
// forward slashes (gitignore semantics are slash-based).
func (m *Matcher) Excludes(p string, isDir bool) bool {
	if m == nil || m.rules == nil {
		return false
	}
	rel := p
	if filepath.IsAbs(p) {
		if r, err := filepath.Rel(m.root, p); err == nil {
			rel = r
		}
	}
	rel = filepath.ToSlash(rel)
	// sabhiram/go-gitignore lacks an isDir hint: pattern "bin/" matches
	// "bin/foo" but not bare "bin". Append the trailing slash for dirs so
	// directory-only patterns fire on the directory entry itself.
	if isDir && rel != "" && rel[len(rel)-1] != '/' {
		rel += "/"
	}
	return m.rules.MatchesPath(rel)
}
