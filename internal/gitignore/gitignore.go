// Package gitignore wraps the sabhiram/go-gitignore matcher with a tiny API
// shaped for ash verbs that walk the filesystem.
//
// Scope (deliberately limited for now):
//   - One .gitignore file at the load root. Nested .gitignore files are not
//     yet honored. Verbs document this.
//   - No global gitignore (~/.gitconfig core.excludesFile) and no
//     .git/info/exclude. Add when needed.
//
// Compiled matchers are cached by load directory and invalidated by mtime
// + size of the underlying .gitignore file. The daemon is long-lived and
// .gitignore changes rarely, so amortizing the regex-compile cost across
// every Walk is a meaningful win — see ASH-36.
package gitignore

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	ignore "github.com/sabhiram/go-gitignore"
)

// Matcher tests whether a path is excluded by a loaded .gitignore file.
// Match input is normalized to a forward-slash path relative to the
// matcher's load root before testing.
//
// A nil *Matcher is valid and excludes nothing -- callers can hold a *Matcher
// uniformly whether or not a .gitignore was found.
//
// Per-path results are memoized across calls. The matcher is owned by the
// daemon and re-used by every Walk; the underlying .gitignore patterns are
// stable for the matcher's lifetime, so (rel, isDir) -> bool is a pure
// function. Memoization turns the hot per-walk regex loop into a map lookup
// — see ASH-38.
type Matcher struct {
	rules   *ignore.GitIgnore
	root    string
	resCache sync.Map // key = normalized rel path (trailing "/" iff isDir); val = bool
}

// cacheEntry holds a compiled matcher plus the staleness-check fields
// from the .gitignore file at load time.
type cacheEntry struct {
	matcher *Matcher
	mtime   time.Time
	size    int64
}

var (
	cacheMu sync.RWMutex
	cache   = map[string]cacheEntry{}
)

// LoadFromDir reads .gitignore at dir if present. Returns (nil, nil) when no
// .gitignore exists (the common case for many directories). A non-nil error
// only signals filesystem trouble reading an existing file.
//
// Compiled matchers are cached per dir; subsequent calls re-stat the file
// and return the cached matcher when mtime + size are unchanged. This is
// load-bearing for walker perf — every ash find/grep call hits this path.
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

	mtime := info.ModTime()
	size := info.Size()

	cacheMu.RLock()
	entry, ok := cache[dir]
	cacheMu.RUnlock()
	if ok && entry.mtime.Equal(mtime) && entry.size == size {
		return entry.matcher, nil
	}

	rules, err := ignore.CompileIgnoreFile(path)
	if err != nil {
		return nil, err
	}
	m := &Matcher{rules: rules, root: dir}

	cacheMu.Lock()
	cache[dir] = cacheEntry{matcher: m, mtime: mtime, size: size}
	cacheMu.Unlock()

	return m, nil
}

// Excludes reports whether p is excluded by the loaded rules. isDir tells the
// matcher whether the path refers to a directory; this matters because
// directory-only patterns like "bin/" only match when the path is a directory.
// The path is normalized relative to the matcher's load root and converted to
// forward slashes (gitignore semantics are slash-based).
//
// Per-path results are memoized: matchers live for the daemon's lifetime
// (or until .gitignore mtime/size changes), and the answer for a given
// (rel, isDir) tuple is invariant within a matcher. The cold path runs the
// underlying regex matcher; the hot path is a sync.Map load.
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
	// rel ends in "/" iff isDir, so the bare key is unambiguous.
	if v, ok := m.resCache.Load(rel); ok {
		return v.(bool)
	}
	res := m.rules.MatchesPath(rel)
	m.resCache.Store(rel, res)
	return res
}
