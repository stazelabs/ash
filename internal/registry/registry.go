// Package registry tracks the set of repositories that have been bootstrapped
// with `ash init`. The list lives in $XDG_CONFIG_HOME/ash/installed-repos.txt
// (fallback ~/.config/ash/installed-repos.txt) — one absolute path per line,
// in insertion order, deduplicated.
//
// The registry is the bridge between the install workflow and cross-repo
// analysis: `ash init` adds an entry, `ash uninit` removes one, and
// `ash report --all-roots` walks the file to find every ledger to
// aggregate.
//
// All operations are best-effort: the file is created on first write, and
// a missing file is treated as an empty registry rather than an error.
package registry

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Path returns the registry file path. The directory is not created;
// callers that intend to write should use Add, which creates parents.
func Path() string {
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "ash", "installed-repos.txt")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		// Last-resort fallback; Add will fail loudly if this path is unwritable.
		return filepath.Join(os.TempDir(), "ash-installed-repos.txt")
	}
	return filepath.Join(home, ".config", "ash", "installed-repos.txt")
}

// List returns the registry entries in insertion order. A missing file is
// not an error — it returns an empty slice. Blank lines and lines starting
// with '#' are ignored so the file can be hand-annotated.
func List() ([]string, error) {
	path := Path()
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var entries []string
	seen := map[string]struct{}{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if _, dup := seen[line]; dup {
			continue
		}
		seen[line] = struct{}{}
		entries = append(entries, line)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return entries, nil
}

// Add appends root to the registry if not already present. The file (and
// its parent dir) is created if missing. Returns true if the entry was
// added, false if it was already present.
func Add(root string) (bool, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return false, fmt.Errorf("registry: root must not be empty")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return false, err
	}
	existing, err := List()
	if err != nil {
		return false, err
	}
	for _, e := range existing {
		if e == abs {
			return false, nil
		}
	}
	path := Path()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return false, err
	}
	defer f.Close()
	if _, err := fmt.Fprintln(f, abs); err != nil {
		return false, err
	}
	return true, nil
}

// Remove deletes root from the registry if present. Returns true if a
// matching entry was removed, false otherwise (including the
// missing-file case).
func Remove(root string) (bool, error) {
	abs, err := filepath.Abs(strings.TrimSpace(root))
	if err != nil {
		return false, err
	}
	existing, err := List()
	if err != nil {
		return false, err
	}
	out := make([]string, 0, len(existing))
	removed := false
	for _, e := range existing {
		if e == abs {
			removed = true
			continue
		}
		out = append(out, e)
	}
	if !removed {
		return false, nil
	}
	path := Path()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return false, err
	}
	for _, e := range out {
		if _, err := fmt.Fprintln(f, e); err != nil {
			f.Close()
			os.Remove(tmp)
			return false, err
		}
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return false, err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return false, err
	}
	return true, nil
}
