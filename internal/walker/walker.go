// Package walker is the single tree-walking primitive shared by ash verbs
// that need to traverse the filesystem (today: find, grep). It implements
// the structural filter pipeline once — hidden-dir prune, max_depth,
// gitignore, exclude, glob — and hands accepted entries to a verb-supplied
// Visitor for record production or content search.
//
// Glob is a visit-gate, not a descent-gate: a dir whose path does not
// match the glob is still descended into, just not visited. This matches
// what find/grep did pre-extraction and is what callers expect ("find all
// .go files under src/" requires descending into src/, even though src/
// itself doesn't match `**/*.go`).
//
// Symlinks are reported as their own type and never followed. This is
// filepath.WalkDir's default and prevents loops or escape from the walk
// root.
package walker

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/stazelabs/ash/internal/gitignore"
)

const DefaultGlob = "**"

// Options is the structural filter pipeline. Zero value is a permissive
// walk (matches everything, descends everything, no gitignore). Callers
// typically populate from verb-level args.
type Options struct {
	Glob             string // doublestar pattern; empty or "**" matches all
	Exclude          string // doublestar pattern; matches are pruned (dirs become SkipDir)
	MaxDepth         int    // 0 = unlimited; 1 = direct children only
	IncludeHidden    bool   // false skips dirs starting with "."; leaf dotfiles are always findable
	RespectGitignore bool   // true loads .gitignore at root and applies its rules
	WantInfo         bool   // true populates Entry.Info via d.Info() (one Lstat per entry); leave false if visitor only needs Path/Type
}

// Entry is what the walker hands to the visitor for each accepted entry.
// Path mirrors the input form of root (absolute-in -> absolute-out,
// relative-in -> relative-out). RelPath is always slash-separated and
// relative to root, suitable for the visitor to use in match patterns.
type Entry struct {
	Path     string
	RelPath  string
	Type     string // "file" | "dir" | "symlink"
	DirEntry fs.DirEntry
	Info     fs.FileInfo // d.Info() result; nil if Info() errored
}

// Action is the visitor's response. Continue is the default; SkipDir
// stops descent into the current directory; Stop terminates the walk.
type Action int

const (
	Continue Action = iota
	SkipDir
	Stop
)

// Visitor receives every accepted entry. Returning a non-nil error aborts
// the walk and surfaces the error via Walk's return value.
type Visitor func(e Entry) (Action, error)

// Walk traverses root, applying the filter pipeline in opts and calling
// visit for each accepted entry. The walk root itself is never visited.
// Returns any error from the underlying os walk, the visitor, or
// gitignore loading. Pattern validation happens once at entry; bad
// patterns return an error before any traversal begins.
func Walk(root string, opts Options, visit Visitor) error {
	if opts.Glob == "" {
		opts.Glob = DefaultGlob
	}
	if !doublestar.ValidatePathPattern(opts.Glob) {
		return fmt.Errorf("walker: invalid glob: %q", opts.Glob)
	}
	if opts.Exclude != "" && !doublestar.ValidatePathPattern(opts.Exclude) {
		return fmt.Errorf("walker: invalid exclude: %q", opts.Exclude)
	}

	var gi *gitignore.Matcher
	if opts.RespectGitignore {
		m, err := gitignore.LoadFromDir(root)
		if err != nil {
			return fmt.Errorf("walker: gitignore: %w", err)
		}
		gi = m
	}

	rootIsAbs := filepath.IsAbs(root)
	stop := false

	walkErr := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if stop {
			return fs.SkipAll
		}
		if err != nil {
			// Permission errors are surfaced as skips, never as walk failures.
			// The visitor gets a partial answer; the walk doesn't abort.
			if errors.Is(err, fs.ErrPermission) {
				if d != nil && d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			return err
		}
		if p == root {
			return nil
		}
		base := filepath.Base(p)
		if !opts.IncludeHidden && d.IsDir() && strings.HasPrefix(base, ".") {
			return fs.SkipDir
		}
		// Depth is computed off the relative path so root="." (where
		// WalkDir hands back bare "cmd" instead of "./cmd") and absolute
		// roots agree. Counting separators on the raw callback path is
		// off-by-one for "." root.
		rel, _ := filepath.Rel(root, p)
		relSlash := filepath.ToSlash(rel)
		if opts.MaxDepth > 0 {
			depth := strings.Count(relSlash, "/") + 1
			if depth > opts.MaxDepth {
				if d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
		}
		if gi.Excludes(relSlash, d.IsDir()) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if opts.Exclude != "" {
			if ex, _ := doublestar.Match(opts.Exclude, relSlash); ex {
				if d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
		}
		if opts.Glob != DefaultGlob {
			if m, _ := doublestar.Match(opts.Glob, relSlash); !m {
				return nil
			}
		}

		var typ string
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			typ = "symlink"
		case d.IsDir():
			typ = "dir"
		default:
			typ = "file"
		}

		// d.Info() triggers an Lstat on Darwin and is paid per accepted
		// entry. Visitors that only need Path/Type leave WantInfo false
		// and skip the cost. Visitors that opt in still guard against
		// nil Info, which can happen if the entry vanished mid-walk.
		var info fs.FileInfo
		if opts.WantInfo {
			info, _ = d.Info()
		}

		// Path-form-mirrors-input: hand the visitor the exact form a
		// caller would have asked for.
		outPath := p
		if !rootIsAbs {
			if rel2, err := filepath.Rel(".", p); err == nil {
				outPath = rel2
			}
		}

		action, vErr := visit(Entry{
			Path:     outPath,
			RelPath:  relSlash,
			Type:     typ,
			DirEntry: d,
			Info:     info,
		})
		if vErr != nil {
			return vErr
		}
		switch action {
		case SkipDir:
			return fs.SkipDir
		case Stop:
			stop = true
			return fs.SkipAll
		}
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, fs.SkipAll) {
		return walkErr
	}
	return nil
}
