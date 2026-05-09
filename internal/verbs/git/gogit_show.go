package git

// Show implementation against go-git/v5. Reuses the diff path for the
// patch portion and produces a Commit record matching log's shape.

import (
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/stazelabs/ash/internal/proto"
)

func runShowGogit(a *Args, tr *proto.Tracer) (*ShowResult, *proto.Error) {
	if a.Ref == "" {
		return nil, &proto.Error{Code: "args", Msg: "show requires --ref"}
	}

	repo, perr := openRepo(a.Path)
	if perr != nil {
		return nil, perr
	}
	t0 := time.Now()
	defer func() { tr.AddIO(time.Since(t0)) }()

	hash, err := repo.ResolveRevision(plumbing.Revision(a.Ref))
	if err != nil {
		return nil, gogitRefError(a.Ref, err)
	}
	commit, err := repo.CommitObject(*hash)
	if err != nil {
		return nil, gogitRefError(a.Ref, err)
	}

	// Reuse the diff range path: "" range means single-rev, parent..rev.
	// Pass the resolved hash string back through resolveDiffEndpoints
	// via the Args.Range field.
	diffArgs := *a
	diffArgs.Range = a.Ref
	diff, perr := diffGogitRange(repo, &diffArgs)
	if perr != nil {
		return nil, perr
	}

	return &ShowResult{
		Commit: commitFromObject(commit),
		Diff:   *diff,
	}, nil
}
