package git

// Status implementation against go-git/v5. Mirrors the StatusResult
// shape produced by parseStatus (the porcelain v2 parser) so the wire
// contract and pretty renderer are unchanged.
//
// Known semantic differences from the shellout backend:
//
//   - jail.allow_paths and other porcelain v2 quirks: go-git's Worktree
//     status does not enumerate ignored files separately, so [git
//     --ignored true] returns an empty list. If Ignored coverage matters,
//     [git].backend = "shellout" remains available.
//   - Conflict detection is presence-of-UpdatedButUnmerged on either
//     side; merge-state nuances (REBASE_HEAD etc.) are not reflected.

import (
	"errors"
	"sort"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/stazelabs/ash/internal/proto"
)

func runStatusGogit(a *Args, tr *proto.Tracer) (*StatusResult, *proto.Error) {
	repo, perr := openRepo(a.Path)
	if perr != nil {
		return nil, perr
	}

	s := &StatusResult{}
	populateHeadInfo(repo, s)
	populateUpstream(repo, s)

	wt, err := repo.Worktree()
	if err != nil {
		return nil, &proto.Error{Code: "git_failed", Msg: err.Error()}
	}

	t0 := time.Now()
	status, err := wt.Status()
	tr.AddIO(time.Since(t0))
	if err != nil {
		return nil, &proto.Error{Code: "git_failed", Msg: err.Error()}
	}

	// Sort for deterministic output across runs.
	paths := make([]string, 0, len(status))
	for p := range status {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	for _, path := range paths {
		st := status[path]
		if st.Worktree == git.UpdatedButUnmerged || st.Staging == git.UpdatedButUnmerged {
			s.Conflicts = append(s.Conflicts, path)
			continue
		}
		// Untracked: both sides are '?' in go-git.
		if st.Worktree == git.Untracked || st.Staging == git.Untracked {
			if a.Untracked {
				s.Untracked = append(s.Untracked, path)
			}
			continue
		}
		if st.Staging != git.Unmodified {
			s.Staged = append(s.Staged, FileChange{
				Path:   path,
				Status: string(rune(st.Staging)),
			})
		}
		if st.Worktree != git.Unmodified {
			s.Unstaged = append(s.Unstaged, FileChange{
				Path:   path,
				Status: string(rune(st.Worktree)),
			})
		}
	}

	s.Clean = len(s.Staged) == 0 && len(s.Unstaged) == 0 && len(s.Untracked) == 0 && len(s.Conflicts) == 0
	return s, nil
}

// populateHeadInfo fills Branch / Detached / Initial / Head from the
// repo's HEAD reference.
func populateHeadInfo(repo *git.Repository, s *StatusResult) {
	head, err := repo.Head()
	if err != nil {
		if errors.Is(err, plumbing.ErrReferenceNotFound) {
			// Fresh repo with no commits.
			s.Initial = true
		}
		return
	}
	if head.Name().IsBranch() {
		s.Branch = head.Name().Short()
		s.Head = s.Branch
		return
	}
	// Detached HEAD: head.Name() is "HEAD".
	s.Detached = true
	hash := head.Hash().String()
	if len(hash) >= 7 {
		s.Head = hash[:7]
	} else {
		s.Head = hash
	}
}

// populateUpstream fills Upstream and (when computable) Ahead/Behind.
// Best-effort: any failure leaves the upstream fields zero rather than
// surfacing as a verb error — matching shellout's tolerance for repos
// without an upstream configured.
func populateUpstream(repo *git.Repository, s *StatusResult) {
	if s.Branch == "" {
		return
	}
	cfg, err := repo.Config()
	if err != nil {
		return
	}
	branchCfg, ok := cfg.Branches[s.Branch]
	if !ok || branchCfg.Remote == "" || branchCfg.Merge == "" {
		return
	}
	upstreamShort := branchCfg.Merge.Short()
	s.Upstream = branchCfg.Remote + "/" + upstreamShort

	upstreamRef := plumbing.NewRemoteReferenceName(branchCfg.Remote, upstreamShort)
	upstream, err := repo.Reference(upstreamRef, true)
	if err != nil {
		return
	}
	head, err := repo.Head()
	if err != nil {
		return
	}
	ahead, behind, err := computeAheadBehind(repo, head.Hash(), upstream.Hash())
	if err != nil {
		return
	}
	s.Ahead = ahead
	s.Behind = behind
}

// computeAheadBehind returns the number of commits in local..remote and
// remote..local (i.e. behind = commits unique to remote, ahead = commits
// unique to local).
func computeAheadBehind(repo *git.Repository, local, remote plumbing.Hash) (ahead, behind int, err error) {
	localCommit, err := repo.CommitObject(local)
	if err != nil {
		return 0, 0, err
	}
	remoteCommit, err := repo.CommitObject(remote)
	if err != nil {
		return 0, 0, err
	}
	bases, err := localCommit.MergeBase(remoteCommit)
	if err != nil {
		return 0, 0, err
	}
	var baseHash plumbing.Hash
	if len(bases) > 0 {
		baseHash = bases[0].Hash
	}
	if ahead, err = countCommitsExclBase(repo, local, baseHash); err != nil {
		return 0, 0, err
	}
	if behind, err = countCommitsExclBase(repo, remote, baseHash); err != nil {
		return 0, 0, err
	}
	return ahead, behind, nil
}

// countCommitsExclBase counts commits reachable from head, stopping at base.
// If base is the zero hash, every reachable commit is counted (the
// no-common-ancestor case).
func countCommitsExclBase(repo *git.Repository, head, base plumbing.Hash) (int, error) {
	if !base.IsZero() && head == base {
		return 0, nil
	}
	iter, err := repo.Log(&git.LogOptions{From: head})
	if err != nil {
		return 0, err
	}
	defer iter.Close()
	n := 0
	err = iter.ForEach(func(c *object.Commit) error {
		if !base.IsZero() && c.Hash == base {
			return storer.ErrStop
		}
		n++
		return nil
	})
	return n, err
}
