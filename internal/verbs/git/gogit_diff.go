package git

// Diff implementation against go-git/v5.
//
// Coverage matrix for this PR:
//
//   --range A..B   (commit-to-commit)   — full patches.
//   --range A      (single rev)         — full patches: A's parent..A.
//   --staged true                       — counts only (Patch="").
//   default        (unstaged worktree)  — counts only (Patch="").
//   --stat true    (any mode)           — full per-file counts.
//
// Working-tree patch text (for --staged or default unstaged) requires
// constructing custom go-git FilePatches and encoding with UnifiedEncoder.
// That is not in scope for this PR; callers who need it set
// [git].backend = "shellout".

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
	"github.com/go-git/go-git/v5/plumbing/format/index"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/sergi/go-diff/diffmatchpatch"
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
		return diffStatFromPatch(patch), nil
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

// diffStatFromPatch walks an object.Patch's FilePatches and accumulates
// counts without producing patch text. Faster + token-cheaper.
func diffStatFromPatch(p *object.Patch) *DiffResult {
	res := &DiffResult{StatOnly: true}
	for _, fp := range p.FilePatches() {
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
				case 1: // diff.Add
					df.Additions += countLines(ch.Content())
				case 2: // diff.Delete
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

// diffGogitStaged returns counts-only DiffFile records for every entry
// that differs between the index and HEAD.
func diffGogitStaged(repo *git.Repository, a *Args) (*DiffResult, *proto.Error) {
	idx, err := repo.Storer.Index()
	if err != nil {
		return nil, &proto.Error{Code: "git_failed", Msg: err.Error()}
	}
	headTree, perr := headTreeOrEmpty(repo)
	if perr != nil {
		return nil, perr
	}

	res := &DiffResult{StatOnly: a.StatOnly}
	seenInIndex := make(map[string]bool, len(idx.Entries))
	for _, e := range idx.Entries {
		seenInIndex[e.Name] = true
		if a.Pathspec != "" && !pathspecMatches(a.Pathspec, e.Name) {
			continue
		}
		df, perr := stagedDeltaForEntry(repo, headTree, e)
		if perr != nil {
			return nil, perr
		}
		if df != nil {
			res.Files = append(res.Files, *df)
			res.TotalAdditions += df.Additions
			res.TotalDeletions += df.Deletions
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
			lines := countLines(content)
			res.Files = append(res.Files, DiffFile{
				Path:      f.Name,
				Status:    "D",
				Deletions: lines,
			})
			res.TotalDeletions += lines
			return nil
		})
		if err != nil {
			return nil, &proto.Error{Code: "git_failed", Msg: err.Error()}
		}
	}
	return res, nil
}

func stagedDeltaForEntry(repo *git.Repository, headTree *object.Tree, e *index.Entry) (*DiffFile, *proto.Error) {
	idxBlob, err := repo.BlobObject(e.Hash)
	if err != nil {
		return nil, &proto.Error{Code: "git_failed", Msg: err.Error()}
	}
	idxContent, err := blobContent(idxBlob)
	if err != nil {
		return nil, &proto.Error{Code: "git_failed", Msg: err.Error()}
	}
	if headTree != nil {
		f, err := headTree.File(e.Name)
		if err == nil {
			headContent, err := f.Contents()
			if err != nil {
				return nil, &proto.Error{Code: "git_failed", Msg: err.Error()}
			}
			if headContent == idxContent {
				return nil, nil
			}
			a, d := lineCounts(headContent, idxContent)
			return &DiffFile{
				Path:      e.Name,
				Status:    "M",
				Additions: a,
				Deletions: d,
			}, nil
		}
	}
	return &DiffFile{
		Path:      e.Name,
		Status:    "A",
		Additions: countLines(idxContent),
	}, nil
}

// diffGogitUnstaged walks the worktree status and reports per-file
// counts for every modified, deleted, or untracked file.
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

	res := &DiffResult{StatOnly: a.StatOnly}
	for path, st := range status {
		if a.Pathspec != "" && !pathspecMatches(a.Pathspec, path) {
			continue
		}
		if st.Worktree == git.Untracked {
			df, perr := untrackedFileDelta(repoRoot, path)
			if perr != nil {
				return nil, perr
			}
			if df != nil {
				res.Files = append(res.Files, *df)
				res.TotalAdditions += df.Additions
			}
			continue
		}
		if st.Worktree == git.Unmodified {
			continue
		}
		df, perr := unstagedDeltaForFile(repo, idx, repoRoot, path, st.Worktree)
		if perr != nil {
			return nil, perr
		}
		if df != nil {
			res.Files = append(res.Files, *df)
			res.TotalAdditions += df.Additions
			res.TotalDeletions += df.Deletions
		}
	}
	return res, nil
}

func untrackedFileDelta(repoRoot, path string) (*DiffFile, *proto.Error) {
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
	return &DiffFile{
		Path:      path,
		Status:    "A",
		Additions: countLines(string(data)),
	}, nil
}

func unstagedDeltaForFile(repo *git.Repository, idx *index.Index, repoRoot, path string, code git.StatusCode) (*DiffFile, *proto.Error) {
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
		return &DiffFile{
			Path:      path,
			Status:    "D",
			Deletions: countLines(beforeContent),
		}, nil
	}
	full := filepath.Join(repoRoot, path)
	data, err := os.ReadFile(full)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, &proto.Error{Code: "git_failed", Msg: err.Error()}
	}
	a, d := lineCounts(beforeContent, string(data))
	return &DiffFile{
		Path:      path,
		Status:    "M",
		Additions: a,
		Deletions: d,
	}, nil
}

// lineCounts returns (additions, deletions) per a line-level diff.
// Uses sergi/go-diff's line preprocessor for speed on large files.
func lineCounts(before, after string) (additions, deletions int) {
	dmp := diffmatchpatch.New()
	a, b, _ := dmp.DiffLinesToChars(before, after)
	diffs := dmp.DiffMain(a, b, false)
	for _, d := range diffs {
		switch d.Type {
		case diffmatchpatch.DiffInsert:
			additions += len(d.Text)
		case diffmatchpatch.DiffDelete:
			deletions += len(d.Text)
		}
	}
	return additions, deletions
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
