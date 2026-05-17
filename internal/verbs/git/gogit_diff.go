package git

// Diff implementation against go-git/v5.
//
// Coverage matrix:
//
//   --range A..B   (commit-to-commit)   — full patches.
//   --range A      (single rev)         — full patches: A's parent..A.
//   --staged true                       — full patches (ASH-66).
//   default        (unstaged worktree)  — full patches (ASH-66).
//   --stat true    (any mode)           — full per-file counts.
//
// Worktree patch text routes through the custom format/diff.FilePatch
// implementation in gogit_diff_worktree.go (ASH-66): one (before,
// after) pair per changed file → sergi/go-diff line-level diff →
// []diff.Chunk → UnifiedEncoder → parseDiffUnified for cap + per-file
// structure, identical to the range-diff code path.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/diff"
	"github.com/go-git/go-git/v5/plumbing/format/index"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stazelabs/ash/internal/proto"
)

func runDiffGogit(a *Args, tr *proto.Tracer) (*DiffResult, *proto.Error) {
	repo, perr := openRepo(a.Path)
	if perr != nil {
		return nil, perr
	}
	t0 := time.Now()
	defer func() { tr.AddIO(time.Since(t0)) }()

	if a.Range != "" {
		return diffGogitRange(repo, a)
	}
	if a.Staged {
		return diffGogitStaged(repo, a)
	}
	return diffGogitUnstaged(repo, a)
}

// diffGogitRange handles --range A..B and single-rev forms. Single rev R
// becomes R^..R (root commits diff against the empty tree).
func diffGogitRange(repo *git.Repository, a *Args) (*DiffResult, *proto.Error) {
	leftCommit, rightCommit, perr := resolveDiffEndpoints(repo, a.Range)
	if perr != nil {
		return nil, perr
	}

	var leftTree, rightTree *object.Tree
	if leftCommit != nil {
		t, err := leftCommit.Tree()
		if err != nil {
			return nil, &proto.Error{Code: "git_failed", Msg: err.Error()}
		}
		leftTree = t
	} else {
		leftTree = &object.Tree{}
	}
	rightTreeObj, err := rightCommit.Tree()
	if err != nil {
		return nil, &proto.Error{Code: "git_failed", Msg: err.Error()}
	}
	rightTree = rightTreeObj

	patch, err := leftTree.Patch(rightTree)
	if err != nil {
		return nil, &proto.Error{Code: "git_failed", Msg: err.Error()}
	}

	if a.StatOnly {
		return diffStatFromFilePatches(patch.FilePatches()), nil
	}
	// Use Patch.String() to get a unified-diff rendering that our
	// existing parseDiffUnified knows how to consume. This keeps caps
	// + per-file structure on one canonical code path.
	return parseDiffUnified([]byte(patch.String()), a.LimitBytes)
}

// resolveDiffEndpoints returns (leftCommit or nil for empty-tree, rightCommit).
func resolveDiffEndpoints(repo *git.Repository, rng string) (*object.Commit, *object.Commit, *proto.Error) {
	if idx := strings.Index(rng, ".."); idx >= 0 {
		l := strings.TrimSpace(rng[:idx])
		r := strings.TrimSpace(rng[idx+2:])
		lh, err := repo.ResolveRevision(plumbing.Revision(l))
		if err != nil {
			return nil, nil, gogitRefError(l, err)
		}
		rh, err := repo.ResolveRevision(plumbing.Revision(r))
		if err != nil {
			return nil, nil, gogitRefError(r, err)
		}
		left, err := repo.CommitObject(*lh)
		if err != nil {
			return nil, nil, gogitRefError(l, err)
		}
		right, err := repo.CommitObject(*rh)
		if err != nil {
			return nil, nil, gogitRefError(r, err)
		}
		return left, right, nil
	}
	rh, err := repo.ResolveRevision(plumbing.Revision(rng))
	if err != nil {
		return nil, nil, gogitRefError(rng, err)
	}
	right, err := repo.CommitObject(*rh)
	if err != nil {
		return nil, nil, gogitRefError(rng, err)
	}
	if right.NumParents() == 0 {
		return nil, right, nil // root commit → empty-tree on the left.
	}
	parent, err := right.Parent(0)
	if err != nil {
		return nil, nil, &proto.Error{Code: "git_failed", Msg: err.Error()}
	}
	return parent, right, nil
}

// diffStatFromFilePatches walks a []diff.FilePatch and accumulates
// counts without producing patch text. Faster + token-cheaper.
// Generalized from the old diffStatFromPatch helper to work with both
// object.Patch (range diffs) and customPatch (worktree diffs, ASH-66).
func diffStatFromFilePatches(fps []diff.FilePatch) *DiffResult {
	res := &DiffResult{StatOnly: true}
	for _, fp := range fps {
		from, to := fp.Files()
		df := DiffFile{Binary: fp.IsBinary()}
		switch {
		case from == nil && to != nil:
			df.Path = to.Path()
			df.Status = "A"
		case from != nil && to == nil:
			df.Path = from.Path()
			df.Status = "D"
		case from != nil && to != nil:
			df.Path = to.Path()
			if from.Path() != to.Path() {
				df.OldPath = from.Path()
				df.Status = "R"
			} else {
				df.Status = "M"
			}
		}
		if !df.Binary {
			for _, ch := range fp.Chunks() {
				switch ch.Type() {
				case diff.Add:
					df.Additions += countLines(ch.Content())
				case diff.Delete:
					df.Deletions += countLines(ch.Content())
				}
			}
			res.TotalAdditions += df.Additions
			res.TotalDeletions += df.Deletions
		}
		res.Files = append(res.Files, df)
	}
	return res
}

// diffGogitStaged builds a unified-diff patch for every entry that
// differs between the index and HEAD, then routes the encoded text
// through parseDiffUnified for cap + per-file structure (same path as
// range diffs). Stat-only mode short-circuits to diffStatFromFilePatches
// to skip the encode pass.
func diffGogitStaged(repo *git.Repository, a *Args) (*DiffResult, *proto.Error) {
	idx, err := repo.Storer.Index()
	if err != nil {
		return nil, &proto.Error{Code: "git_failed", Msg: err.Error()}
	}
	headTree, perr := headTreeOrEmpty(repo)
	if perr != nil {
		return nil, perr
	}

	var fps []diff.FilePatch
	seenInIndex := make(map[string]bool, len(idx.Entries))
	for _, e := range idx.Entries {
		seenInIndex[e.Name] = true
		if a.Pathspec != "" && !pathspecMatches(a.Pathspec, e.Name) {
			continue
		}
		fp, perr := stagedFilePatch(repo, headTree, e)
		if perr != nil {
			return nil, perr
		}
		if fp != nil {
			fps = append(fps, fp)
		}
	}
	// Files in HEAD that the index dropped → staged deletes.
	if headTree != nil {
		err := headTree.Files().ForEach(func(f *object.File) error {
			if seenInIndex[f.Name] {
				return nil
			}
			if a.Pathspec != "" && !pathspecMatches(a.Pathspec, f.Name) {
				return nil
			}
			content, err := f.Contents()
			if err != nil {
				return err
			}
			fps = append(fps, delFilePatch(f.Name, content))
			return nil
		})
		if err != nil {
			return nil, &proto.Error{Code: "git_failed", Msg: err.Error()}
		}
	}
	return finalizeWorktreeResult(a, fps)
}

// stagedFilePatch builds a customFilePatch describing one (HEAD blob →
// index blob) transition for the given index entry. Returns (nil, nil)
// when the entry has the same contents on both sides (no change).
func stagedFilePatch(repo *git.Repository, headTree *object.Tree, e *index.Entry) (*customFilePatch, *proto.Error) {
	idxBlob, err := repo.BlobObject(e.Hash)
	if err != nil {
		return nil, &proto.Error{Code: "git_failed", Msg: err.Error()}
	}
	idxContent, err := blobContent(idxBlob)
	if err != nil {
		return nil, &proto.Error{Code: "git_failed", Msg: err.Error()}
	}
	if headTree != nil {
		if f, err := headTree.File(e.Name); err == nil {
			headContent, err := f.Contents()
			if err != nil {
				return nil, &proto.Error{Code: "git_failed", Msg: err.Error()}
			}
			if headContent == idxContent {
				return nil, nil
			}
			return modFilePatch(e.Name, headContent, idxContent), nil
		}
	}
	return addFilePatch(e.Name, idxContent), nil
}

// finalizeWorktreeResult drives the common tail of diffGogitStaged /
// diffGogitUnstaged: in stat-only mode aggregate counts directly from
// the FilePatches; in patch mode encode + reparse so size caps and
// truncation flow through the same parseDiffUnified code path as range
// diffs.
func finalizeWorktreeResult(a *Args, fps []diff.FilePatch) (*DiffResult, *proto.Error) {
	if a.StatOnly {
		return diffStatFromFilePatches(fps), nil
	}
	encoded, err := encodeCustomPatches(fps, a.Context)
	if err != nil {
		return nil, &proto.Error{Code: "git_failed", Msg: err.Error()}
	}
	return parseDiffUnified([]byte(encoded), a.LimitBytes)
}

// diffGogitUnstaged builds a unified-diff patch for every modified,
// deleted, or untracked file in the worktree, then routes through
// parseDiffUnified so size caps and per-file structure flow through
// the same code path as range/staged diffs.
func diffGogitUnstaged(repo *git.Repository, a *Args) (*DiffResult, *proto.Error) {
	wt, err := repo.Worktree()
	if err != nil {
		return nil, &proto.Error{Code: "git_failed", Msg: err.Error()}
	}
	status, err := wt.Status()
	if err != nil {
		return nil, &proto.Error{Code: "git_failed", Msg: err.Error()}
	}
	idx, err := repo.Storer.Index()
	if err != nil {
		return nil, &proto.Error{Code: "git_failed", Msg: err.Error()}
	}
	repoRoot := wt.Filesystem.Root()

	var fps []diff.FilePatch
	for path, st := range status {
		if a.Pathspec != "" && !pathspecMatches(a.Pathspec, path) {
			continue
		}
		if st.Worktree == git.Untracked {
			fp, perr := untrackedFilePatch(repoRoot, path)
			if perr != nil {
				return nil, perr
			}
			if fp != nil {
				fps = append(fps, fp)
			}
			continue
		}
		if st.Worktree == git.Unmodified {
			continue
		}
		fp, perr := unstagedFilePatch(repo, idx, repoRoot, path, st.Worktree)
		if perr != nil {
			return nil, perr
		}
		if fp != nil {
			fps = append(fps, fp)
		}
	}
	return finalizeWorktreeResult(a, fps)
}

// untrackedFilePatch reads a not-yet-staged file from disk and emits
// an "add" FilePatch (from=nil). Returns (nil, nil) when the path
// vanished between status() and stat (rare race).
func untrackedFilePatch(repoRoot, path string) (*customFilePatch, *proto.Error) {
	full := filepath.Join(repoRoot, path)
	info, err := os.Stat(full)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, &proto.Error{Code: "git_failed", Msg: err.Error()}
	}
	if info.IsDir() {
		return nil, nil // status will report individual files inside.
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return nil, &proto.Error{Code: "git_failed", Msg: err.Error()}
	}
	return addFilePatch(path, string(data)), nil
}

// unstagedFilePatch builds a FilePatch for one (index blob → worktree
// content) transition. Handles the deleted-file case (to=nil) too.
func unstagedFilePatch(repo *git.Repository, idx *index.Index, repoRoot, path string, code git.StatusCode) (*customFilePatch, *proto.Error) {
	var beforeContent string
	for _, e := range idx.Entries {
		if e.Name == path {
			blob, err := repo.BlobObject(e.Hash)
			if err != nil {
				return nil, &proto.Error{Code: "git_failed", Msg: err.Error()}
			}
			beforeContent, err = blobContent(blob)
			if err != nil {
				return nil, &proto.Error{Code: "git_failed", Msg: err.Error()}
			}
			break
		}
	}
	if code == git.Deleted {
		return delFilePatch(path, beforeContent), nil
	}
	full := filepath.Join(repoRoot, path)
	data, err := os.ReadFile(full)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, &proto.Error{Code: "git_failed", Msg: err.Error()}
	}
	return modFilePatch(path, beforeContent, string(data)), nil
}

func countLines(s string) int {
	if s == "" {
		return 0
	}
	n := strings.Count(s, "\n")
	if !strings.HasSuffix(s, "\n") {
		n++
	}
	return n
}

func headTreeOrEmpty(repo *git.Repository) (*object.Tree, *proto.Error) {
	head, err := repo.Head()
	if err != nil {
		if errors.Is(err, plumbing.ErrReferenceNotFound) {
			return nil, nil
		}
		return nil, &proto.Error{Code: "git_failed", Msg: err.Error()}
	}
	commit, err := repo.CommitObject(head.Hash())
	if err != nil {
		return nil, &proto.Error{Code: "git_failed", Msg: err.Error()}
	}
	t, err := commit.Tree()
	if err != nil {
		return nil, &proto.Error{Code: "git_failed", Msg: err.Error()}
	}
	return t, nil
}

func pathspecMatches(spec, path string) bool {
	spec = strings.TrimSuffix(spec, "/")
	if path == spec {
		return true
	}
	return strings.HasPrefix(path, spec+"/")
}

func blobContent(b *object.Blob) (string, error) {
	r, err := b.Reader()
	if err != nil {
		return "", err
	}
	defer r.Close()
	data, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// guard against unused-import lints when the codebase trims branches.
var _ = fmt.Sprintf
