package git

// Log implementation against go-git/v5. Produces the same Commit and
// LogResult shapes as the shellout backend.
//
// Known semantic differences from shellout:
//
//   - --since / --until accept Go-parseable absolute dates (RFC3339,
//     "2006-01-02 15:04:05", "2006-01-02"). Relative forms like
//     "1 week ago" are git-CLI conveniences not implemented here;
//     a caller that needs them can opt back to [git].backend = "shellout".
//   - --author matches as a case-insensitive substring against
//     "Name <email>" rather than git's regex semantics. Sufficient for
//     the typical "filter by my name" agent use; non-trivial regexes
//     will diverge.

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/stazelabs/ash/internal/proto"
)

func runLogGogit(a *Args, tr *proto.Tracer) (*LogResult, *proto.Error) {
	repo, perr := openRepo(a.Path)
	if perr != nil {
		return nil, perr
	}

	t0 := time.Now()
	defer func() { tr.AddIO(time.Since(t0)) }()

	headHash, stop, perr := resolveLogRange(repo, a.Range)
	if perr != nil {
		return nil, perr
	}
	if headHash.IsZero() {
		// Fresh repo with no HEAD — return empty.
		return &LogResult{}, nil
	}

	since, until, perr := parseLogTimeFilters(a.Since, a.Until)
	if perr != nil {
		return nil, perr
	}

	opts := &git.LogOptions{
		From:  headHash,
		Order: git.LogOrderCommitterTime,
	}
	if since != nil {
		opts.Since = since
	}
	if until != nil {
		opts.Until = until
	}
	if a.Pathspec != "" {
		filter := pathspecFilter(a.Pathspec)
		opts.PathFilter = filter
	}

	iter, err := repo.Log(opts)
	if err != nil {
		// Treat "reference not found" on empty repo as empty result.
		if errors.Is(err, plumbing.ErrReferenceNotFound) {
			return &LogResult{}, nil
		}
		return nil, &proto.Error{Code: "git_failed", Msg: err.Error()}
	}
	defer iter.Close()

	res := &LogResult{}
	wantPlusOne := a.Limit + 1 // request +1 so we can detect truncation cheaply.
	authorMatch := authorMatcher(a.Author)

	err = iter.ForEach(func(c *object.Commit) error {
		if !stop.IsZero() && c.Hash == stop {
			return storer.ErrStop
		}
		if !authorMatch(c) {
			return nil
		}
		res.Commits = append(res.Commits, commitFromObject(c))
		if len(res.Commits) >= wantPlusOne {
			return storer.ErrStop
		}
		return nil
	})
	if err != nil && !errors.Is(err, storer.ErrStop) && !errors.Is(err, io.EOF) {
		return nil, &proto.Error{Code: "git_failed", Msg: err.Error()}
	}

	if len(res.Commits) > a.Limit {
		res.Commits = res.Commits[:a.Limit]
		res.Truncated = true
		res.TruncInfo = &proto.TruncInfo{Trunc: 1, Limit: a.Limit, Max: LogMaxLimit}
	}
	res.Count = len(res.Commits)
	return res, nil
}

// resolveLogRange returns (start, stop) hashes for the log walk.
// Range syntax accepted:
//   - "" → HEAD, no stop.
//   - "<rev>" → that rev, no stop.
//   - "<rev1>..<rev2>" → start at rev2, stop at rev1.
func resolveLogRange(repo *git.Repository, rng string) (start, stop plumbing.Hash, perr *proto.Error) {
	if rng == "" {
		head, err := repo.Head()
		if err != nil {
			if errors.Is(err, plumbing.ErrReferenceNotFound) {
				return plumbing.ZeroHash, plumbing.ZeroHash, nil
			}
			return plumbing.ZeroHash, plumbing.ZeroHash, &proto.Error{Code: "git_failed", Msg: err.Error()}
		}
		return head.Hash(), plumbing.ZeroHash, nil
	}

	if idx := strings.Index(rng, ".."); idx >= 0 && !strings.HasPrefix(rng[idx:], "...") {
		left := strings.TrimSpace(rng[:idx])
		right := strings.TrimSpace(rng[idx+2:])
		if left == "" || right == "" {
			return plumbing.ZeroHash, plumbing.ZeroHash, &proto.Error{Code: "args", Msg: "invalid range " + rng}
		}
		startHash, err := repo.ResolveRevision(plumbing.Revision(right))
		if err != nil {
			return plumbing.ZeroHash, plumbing.ZeroHash, gogitRefError(right, err)
		}
		stopHash, err := repo.ResolveRevision(plumbing.Revision(left))
		if err != nil {
			return plumbing.ZeroHash, plumbing.ZeroHash, gogitRefError(left, err)
		}
		return *startHash, *stopHash, nil
	}

	startHash, err := repo.ResolveRevision(plumbing.Revision(rng))
	if err != nil {
		return plumbing.ZeroHash, plumbing.ZeroHash, gogitRefError(rng, err)
	}
	return *startHash, plumbing.ZeroHash, nil
}

// parseLogTimeFilters parses --since / --until strings.
func parseLogTimeFilters(since, until string) (s, u *time.Time, perr *proto.Error) {
	if since != "" {
		t, err := parseFlexibleTime(since)
		if err != nil {
			return nil, nil, &proto.Error{Code: "args", Msg: "invalid --since: " + err.Error()}
		}
		s = &t
	}
	if until != "" {
		t, err := parseFlexibleTime(until)
		if err != nil {
			return nil, nil, &proto.Error{Code: "args", Msg: "invalid --until: " + err.Error()}
		}
		u = &t
	}
	return s, u, nil
}

// parseFlexibleTime accepts a few common formats. Relative forms ("1
// week ago") are not supported — see file-level doc comment.
func parseFlexibleTime(s string) (time.Time, error) {
	for _, layout := range []string{
		time.RFC3339,
		time.RFC3339Nano,
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05",
		"2006-01-02",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("could not parse %q (try RFC3339, \"2006-01-02 15:04:05\", or \"2006-01-02\"; or use [git].backend = \"shellout\" for relative forms like \"1 week ago\")", s)
}

// pathspecFilter returns a predicate matching paths whose canonical form
// equals the spec or has it as a leading directory. This is a reduced
// subset of git's pathspec — sufficient for "directory or file" use,
// which is the common case.
func pathspecFilter(spec string) func(string) bool {
	spec = strings.TrimSuffix(spec, "/")
	prefix := spec + "/"
	return func(p string) bool {
		if p == spec {
			return true
		}
		return strings.HasPrefix(p, prefix)
	}
}

// authorMatcher returns a predicate that returns true if a is empty
// (no filter), or if the commit's author "Name <email>" contains a
// case-insensitively.
func authorMatcher(a string) func(*object.Commit) bool {
	if a == "" {
		return func(*object.Commit) bool { return true }
	}
	needle := strings.ToLower(a)
	return func(c *object.Commit) bool {
		hay := strings.ToLower(c.Author.Name + " <" + c.Author.Email + ">")
		return strings.Contains(hay, needle)
	}
}

// commitFromObject produces a Commit record matching the shellout
// shape. AuthorTime / CommitterTime are unix nanoseconds.
func commitFromObject(c *object.Commit) Commit {
	subj, body := splitMessage(c.Message)
	parents := make([]string, 0, c.NumParents())
	for _, p := range c.ParentHashes {
		parents = append(parents, p.String())
	}
	return Commit{
		SHA:            c.Hash.String(),
		ShortSHA:       c.Hash.String()[:7],
		AuthorName:     c.Author.Name,
		AuthorEmail:    c.Author.Email,
		AuthorTime:     c.Author.When.UnixNano(),
		CommitterName:  c.Committer.Name,
		CommitterEmail: c.Committer.Email,
		CommitterTime:  c.Committer.When.UnixNano(),
		Subject:        subj,
		Body:           body,
		Parents:        parents,
	}
}

// splitMessage matches `git log --format=%s%n%b` semantics: subject is
// the first line, body is everything after the first blank line (with
// any trailing newlines trimmed).
func splitMessage(msg string) (subject, body string) {
	msg = strings.TrimRight(msg, "\n")
	if msg == "" {
		return "", ""
	}
	if i := strings.Index(msg, "\n"); i >= 0 {
		subject = msg[:i]
		rest := msg[i+1:]
		// Drop a single blank separator line between subject and body
		// to match shellout's NUL-separated %s%n%b output.
		rest = strings.TrimPrefix(rest, "\n")
		body = strings.TrimRight(rest, "\n")
		return subject, body
	}
	return msg, ""
}
