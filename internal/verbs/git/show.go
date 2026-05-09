package git

import (
	"fmt"
	"strings"
	"time"

	"github.com/stazelabs/ash/internal/proto"
	"github.com/stazelabs/ash/internal/runner"
)

// emptyTreeSHA is the well-known SHA of the empty tree object. Used as
// the "before" side of root-commit diffs (commits with no parent), since
// `git diff <root>~..<root>` is invalid.
const emptyTreeSHA = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

// ShowResult bundles a single commit's metadata with its diff. Reuses
// Commit (from log.go) and DiffResult (from diff.go) so callers don't
// learn a third shape.
type ShowResult struct {
	Commit Commit     `msgpack:"commit"`
	Diff   DiffResult `msgpack:"diff"`
}

// runShow fetches a single commit's metadata + diff. Two subprocess
// calls: one for metadata via `git log -1 --format=...`, one for the
// diff via `git diff <parent>..<ref>` (or empty-tree..ref for root
// commits). Both reuse the parsers from log.go and diff.go.
func runShow(a *Args, tr *proto.Tracer) (*ShowResult, *proto.Error) {
	if a.Ref == "" {
		return nil, &proto.Error{Code: "args", Msg: "show requires --ref"}
	}

	// Metadata: limit=1, no pathspec (we want the commit even if it
	// touched no matching paths). Reuse log's NUL-separated format.
	metaArgs := []string{"-C", a.Path, "log", "-1", "-z", "--format=" + logFormat, a.Ref}
	res, perr := runner.Run("git", metaArgs, runner.Opts{Tracer: tr})
	if perr != nil {
		return nil, perr
	}
	if res.ExitCode != 0 {
		return nil, showRunError(a.Path, res.Stderr, a.Ref)
	}
	logRes, perr := parseLog(res.Stdout, 1)
	if perr != nil {
		return nil, perr
	}
	if len(logRes.Commits) == 0 {
		return nil, &proto.Error{Code: "ref_not_found", Msg: a.Ref + ": resolved but no commit found"}
	}
	commit := logRes.Commits[0]

	// Diff: pick the "before" side based on whether this is a root commit.
	before := emptyTreeSHA
	if len(commit.Parents) > 0 {
		before = commit.Parents[0]
	}
	diff, perr := runShowDiff(a, before, tr)
	if perr != nil {
		return nil, perr
	}

	return &ShowResult{Commit: commit, Diff: *diff}, nil
}

// runShowDiff runs the diff for show — either --numstat (stat mode) or
// the full unified diff. Reuses parseDiffNumstat and parseDiffUnified
// directly so we don't go through runDiff (which has its own arg shape
// for the diff op).
func runShowDiff(a *Args, before string, tr *proto.Tracer) (*DiffResult, *proto.Error) {
	gitArgs := []string{"-C", a.Path, "diff"}
	if a.StatOnly {
		gitArgs = append(gitArgs, "--numstat")
	} else {
		gitArgs = append(gitArgs, "--no-color", fmt.Sprintf("--unified=%d", a.Context))
	}
	gitArgs = append(gitArgs, before+".."+a.Ref)
	if a.Pathspec != "" {
		gitArgs = append(gitArgs, "--", a.Pathspec)
	}

	res, perr := runner.Run("git", gitArgs, runner.Opts{Tracer: tr})
	if perr != nil {
		return nil, perr
	}
	if res.ExitCode != 0 {
		return nil, gitRunError(a.Path, res.Stderr)
	}
	if a.StatOnly {
		return parseDiffNumstat(res.Stdout)
	}
	return parseDiffUnified(res.Stdout, a.LimitBytes)
}

// showRunError maps the metadata-fetch failure into a typed proto.Error.
// Bad-ref messages from git become ref_not_found; everything else falls
// through to the shared gitRunError.
func showRunError(path string, stderr []byte, ref string) *proto.Error {
	msg := strings.TrimSpace(string(stderr))
	lower := strings.ToLower(msg)
	if strings.Contains(lower, "not a git repository") {
		return &proto.Error{Code: "not_a_repo", Msg: path + " is not inside a git repository"}
	}
	if strings.Contains(lower, "bad revision") ||
		strings.Contains(lower, "unknown revision") ||
		strings.Contains(lower, "ambiguous argument") {
		return &proto.Error{Code: "ref_not_found", Msg: ref + ": " + msg}
	}
	if msg == "" {
		msg = "git exited non-zero"
	}
	return &proto.Error{Code: "git_failed", Msg: msg}
}

func prettyShow(s *ShowResult) string {
	if s == nil {
		return "ok\n<empty show>"
	}
	var b strings.Builder
	c := s.Commit
	fmt.Fprintf(&b, "=== ash git show: %s — %s ===\n", c.ShortSHA, c.Subject)
	fmt.Fprintf(&b, "author: %s <%s>  %s\n",
		c.AuthorName, c.AuthorEmail,
		time.Unix(0, c.AuthorTime).UTC().Format("2006-01-02"))
	if len(c.Parents) > 0 {
		short := make([]string, 0, len(c.Parents))
		for _, p := range c.Parents {
			if len(p) >= 7 {
				short = append(short, p[:7])
			} else {
				short = append(short, p)
			}
		}
		fmt.Fprintf(&b, "parents: %s\n", strings.Join(short, " "))
	} else {
		b.WriteString("parents: (root commit)\n")
	}
	if c.Body != "" {
		b.WriteByte('\n')
		b.WriteString(c.Body)
		b.WriteByte('\n')
	}

	d := s.Diff
	fmt.Fprintf(&b, "\n+%d -%d in %d file", d.TotalAdditions, d.TotalDeletions, len(d.Files))
	if len(d.Files) != 1 {
		b.WriteByte('s')
	}
	if d.Truncated {
		b.WriteString(" TRUNCATED")
	}
	b.WriteByte('\n')

	if d.StatOnly {
		for _, f := range d.Files {
			if f.Binary {
				fmt.Fprintf(&b, "  binary          %s\n", f.Path)
			} else {
				fmt.Fprintf(&b, "  +%-5d  -%-5d  %s\n", f.Additions, f.Deletions, f.Path)
			}
		}
	} else {
		b.WriteByte('\n')
		for _, f := range d.Files {
			if f.Patch != "" {
				b.WriteString(f.Patch)
			} else if f.Binary {
				fmt.Fprintf(&b, "[%s %s: binary file]\n", f.Status, f.Path)
			} else {
				fmt.Fprintf(&b, "[%s %s: +%d -%d (patch omitted, byte cap reached)]\n",
					f.Status, f.Path, f.Additions, f.Deletions)
			}
		}
	}

	if d.Truncated && d.TruncationHint != "" {
		b.WriteString("\n[truncation: ")
		b.WriteString(d.TruncationHint)
		b.WriteString("]")
	}
	return strings.TrimRight(b.String(), "\n")
}
