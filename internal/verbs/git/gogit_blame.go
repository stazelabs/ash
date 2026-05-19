// Blame implementation against go-git/v5.
//
// Path resolution: --path points at a single file (not a directory).
// openRepo runs from the file's directory and DetectDotGit traverses
// upward to find .git; the relative-to-worktree path is what go-git's
// object.Blame consumes.
//
// Known divergence from system git: go-git does not follow file
// renames during blame. For rename-following blame, fall back to
// shellout — though see backend.go: blame is gogit-only in v1, so
// rename-following blame currently means using system git directly
// outside ash.

package git

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stazelabs/ash/internal/proto"
)

func runBlameGogit(a *Args, tr *proto.Tracer) (*BlameResult, *proto.Error) {
	if strings.TrimSpace(a.Path) == "" || a.Path == "." {
		return nil, &proto.Error{Code: "args", Msg: "blame requires --path pointing at a file"}
	}

	absPath, err := filepath.Abs(a.Path)
	if err != nil {
		return nil, &proto.Error{Code: "args", Msg: "could not resolve --path: " + err.Error()}
	}
	info, err := os.Stat(absPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, &proto.Error{Code: "path_not_found", Msg: a.Path + ": no such file"}
		}
		return nil, &proto.Error{Code: "git_failed", Msg: err.Error()}
	}
	if info.IsDir() {
		return nil, &proto.Error{Code: "args", Msg: "blame requires a file, not a directory: " + a.Path}
	}

	repo, perr := openRepo(filepath.Dir(absPath))
	if perr != nil {
		return nil, perr
	}
	t0 := time.Now()
	defer func() { tr.AddIO(time.Since(t0)) }()

	wt, err := repo.Worktree()
	if err != nil {
		return nil, &proto.Error{Code: "git_failed", Msg: "worktree: " + err.Error()}
	}
	// EvalSymlinks both sides: on macOS t.TempDir often returns /var/...
	// while the resolved worktree root is /private/var/..., which makes
	// filepath.Rel produce a ../.. escape path.
	resolvedRoot, err := filepath.EvalSymlinks(wt.Filesystem.Root())
	if err != nil {
		return nil, &proto.Error{Code: "git_failed", Msg: "worktree root: " + err.Error()}
	}
	resolvedPath, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		return nil, &proto.Error{Code: "git_failed", Msg: "path: " + err.Error()}
	}
	relPath, err := filepath.Rel(resolvedRoot, resolvedPath)
	if err != nil {
		return nil, &proto.Error{Code: "git_failed", Msg: "rel path: " + err.Error()}
	}
	relPath = filepath.ToSlash(relPath)
	if strings.HasPrefix(relPath, "../") || relPath == ".." {
		return nil, &proto.Error{Code: "args", Msg: "path is outside the repository: " + a.Path}
	}

	rev := a.Rev
	if rev == "" {
		rev = "HEAD"
	}
	hash, err := repo.ResolveRevision(plumbing.Revision(rev))
	if err != nil {
		return nil, gogitRefError(rev, err)
	}
	commit, err := repo.CommitObject(*hash)
	if err != nil {
		return nil, gogitRefError(rev, err)
	}

	br, err := git.Blame(commit, relPath)
	if err != nil {
		return nil, blameError(relPath, rev, err)
	}

	start, end, hasRange, perr := parseBlameLines(a.Lines)
	if perr != nil {
		return nil, perr
	}

	lines := br.Lines
	startOffset := 1
	if hasRange {
		// 1-based inclusive endpoints. Open endpoints default to file bounds.
		lo := start
		if lo < 1 {
			lo = 1
		}
		hi := end
		if hi < 1 || hi > len(lines) {
			hi = len(lines)
		}
		if lo > len(lines) {
			return nil, &proto.Error{Code: "range_out_of_bounds",
				Msg: a.Lines + ": start exceeds file length " + strconv.Itoa(len(lines))}
		}
		lines = lines[lo-1 : hi]
		startOffset = lo
	}

	if len(lines) > BlameMaxLines {
		return &BlameResult{
			Path:      relPath,
			Rev:       hash.String(),
			Truncated: true,
			TruncInfo: &proto.TruncInfo{Trunc: len(lines), Limit: BlameMaxLines, Max: BlameMaxLines},
		}, nil
	}

	compact := make([]blameLine, len(lines))
	for i, ln := range lines {
		compact[i] = blameLine{
			SHA:        ln.Hash.String(),
			AuthorName: ln.AuthorName,
			AuthorTime: ln.Date.UnixNano(),
			Text:       ln.Text,
		}
	}
	hunks := compactBlameLines(compact, startOffset)

	byteCap := a.LimitBytes
	if byteCap <= 0 {
		byteCap = BlameDefaultBytes
	}
	hunks, truncated, ti := applyBlameByteCap(hunks, byteCap)

	return &BlameResult{
		Path:      relPath,
		Rev:       hash.String(),
		Hunks:     hunks,
		Truncated: truncated,
		TruncInfo: ti,
	}, nil
}

// blameError maps go-git's Blame errors into the verb's typed code set.
// "file not found" / "object not found" → path_not_in_rev so callers
// can distinguish "the file isn't in this revision" from "the rev
// doesn't exist."
func blameError(path, rev string, err error) *proto.Error {
	if err == nil {
		return nil
	}
	if errors.Is(err, object.ErrFileNotFound) {
		return &proto.Error{Code: "path_not_in_rev", Msg: path + " not found at " + rev}
	}
	msg := err.Error()
	lower := strings.ToLower(msg)
	if strings.Contains(lower, "file not found") ||
		strings.Contains(lower, "object not found") ||
		strings.Contains(lower, "entry not found") {
		return &proto.Error{Code: "path_not_in_rev", Msg: path + " not found at " + rev}
	}
	return &proto.Error{Code: "git_failed", Msg: msg}
}

