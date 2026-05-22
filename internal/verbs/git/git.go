// Package git implements the `git` verb, version control as structured
// calls instead of porcelain text-scraping.
//
// Verb shape:
//
//	ash git --op <op> [--path <p>] [op-specific flags...]
//
// Op discriminator (live ops):
//
//	status   summarize repo state — branch, ahead/behind,
//	         staged/unstaged/untracked/conflicts. Optional: --untracked,
//	         --ignored.
//	log      list commits with structured per-commit metadata. Optional:
//	         --limit, --range, --author, --since, --until, --pathspec.
//	diff     structured file-level diff. Optional: --staged, --range,
//	         --pathspec, --stat, --context, --bytes.
//
// Implementation shells out to system `git` and parses documented
// machine-readable formats (porcelain v2 for status, NUL-separated
// custom format for log, unified diff for diff). The agent never sees
// porcelain; they get a typed Result.
//
// The README's example uses positional subcommand syntax (`git diff
// --range ...`); ash's flags-only client rule rules that out, so we use
// `--op <subcommand>` for consistency with every other verb.
package git

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/stazelabs/ash/internal/jail"
	"github.com/stazelabs/ash/internal/proto"
	"github.com/stazelabs/ash/internal/verbs/argutil"
)

// gitRunError maps a non-zero git exit into a proto.Error. It recognises the
// "not a git repository" message and returns the typed not_a_repo code; all
// other failures become git_failed.
func gitRunError(path string, stderr []byte) *proto.Error {
	msg := strings.TrimSpace(string(stderr))
	if msg == "" {
		msg = "git exited non-zero"
	}
	if strings.Contains(strings.ToLower(msg), "not a git repository") {
		return &proto.Error{Code: "not_a_repo", Msg: jail.PrettyPath(path) + " is not inside a git repository"}
	}
	return &proto.Error{Code: "git_failed", Msg: msg}
}

// gitDirArg resolves a --path value to the directory passed to `git -C`.
// git's -C requires a directory, but callers may point --path at a file
// inside the repo — the go-git backend tolerates that via repo
// discovery, so the shellout backend matches by resolving a file path
// to its parent directory. A nonexistent path (or any stat error) is
// returned unchanged so git itself emits the diagnostic (ASH-203).
func gitDirArg(path string) string {
	if fi, err := os.Stat(path); err == nil && !fi.IsDir() {
		return filepath.Dir(path)
	}
	return path
}

// Result is the wire envelope. Op is always set; the populated payload
// depends on which op was requested. Future ops add their own pointer
// field; only one is non-nil per response.
type Result struct {
	Op     string        `msgpack:"op"`
	Status *StatusResult `msgpack:"status,omitempty"`
	Log    *LogResult    `msgpack:"log,omitempty"`
	Diff   *DiffResult   `msgpack:"diff,omitempty"`
	Show   *ShowResult   `msgpack:"show,omitempty"`
	Blame  *BlameResult  `msgpack:"blame,omitempty"`
}

type Args struct {
	Op   string
	Path string
	// status-op flags
	Untracked bool
	Ignored   bool
	// log-op flags
	Limit  int
	Author string
	Since  string
	Until  string
	// log/diff-op flags (shared)
	Range    string
	Pathspec string
	// show-op flags
	Ref string
	// blame-op flags
	Rev   string
	Lines string
	// diff-op flags
	Staged     bool
	Context    int
	StatOnly   bool
	LimitBytes int
}

func ParseArgs(in map[string]any) (*Args, *proto.Error) {
	a := &Args{}
	var perr *proto.Error
	if a.Op, perr = argutil.RequireString(in, "op"); perr != nil {
		return nil, perr
	}
	if a.Path, perr = argutil.OptionalNonEmptyString(in, "path", "."); perr != nil {
		return nil, perr
	}
	if a.Untracked, perr = argutil.OptionalBool(in, "untracked", true); perr != nil {
		return nil, perr
	}
	if a.Ignored, perr = argutil.OptionalBool(in, "ignored", false); perr != nil {
		return nil, perr
	}
	// log-op flags. String values pass through to git, which validates
	// their content (date formats, refspecs, pathspecs); ParseArgs
	// rejects only a leading '-' — see the ASH-211 guard below.
	if a.Limit, perr = argutil.OptionalPosInt(in, "limit", LogDefaultLimit, LogMaxLimit); perr != nil {
		return nil, perr
	}
	if a.Author, perr = argutil.OptionalString(in, "author", ""); perr != nil {
		return nil, perr
	}
	if a.Since, perr = argutil.OptionalString(in, "since", ""); perr != nil {
		return nil, perr
	}
	if a.Until, perr = argutil.OptionalString(in, "until", ""); perr != nil {
		return nil, perr
	}
	// log/diff shared flags
	if a.Range, perr = argutil.OptionalString(in, "range", ""); perr != nil {
		return nil, perr
	}
	if a.Pathspec, perr = argutil.OptionalString(in, "pathspec", ""); perr != nil {
		return nil, perr
	}
	// show-op flags
	if a.Ref, perr = argutil.OptionalString(in, "ref", ""); perr != nil {
		return nil, perr
	}
	// blame-op flags
	if a.Rev, perr = argutil.OptionalString(in, "rev", ""); perr != nil {
		return nil, perr
	}
	if a.Lines, perr = argutil.OptionalString(in, "lines", ""); perr != nil {
		return nil, perr
	}
	// diff-op flags
	if a.Staged, perr = argutil.OptionalBool(in, "staged", false); perr != nil {
		return nil, perr
	}
	if a.Context, perr = argutil.OptionalPosInt(in, "context", DiffDefaultContext, DiffMaxContext); perr != nil {
		return nil, perr
	}
	if a.StatOnly, perr = argutil.OptionalBool(in, "stat", false); perr != nil {
		return nil, perr
	}
	if a.LimitBytes, perr = argutil.OptionalPosInt(in, "bytes", DiffDefaultLimitBytes, DiffMaxLimitBytes); perr != nil {
		return nil, perr
	}
	// Security (ASH-211): reject revision/pathspec args whose value
	// begins with "-". The shellout backend splices these straight into
	// the git argv, and git would read e.g. --range '--output=/path' as
	// an option — `git log --output=FILE` writes attacker-chosen files.
	// A legitimate ref, range, date, author, or pathspec never starts
	// with "-", so rejection breaks no real call and closes the hole
	// for both the shellout and go-git backends.
	for _, f := range []struct{ name, val string }{
		{"range", a.Range},
		{"author", a.Author},
		{"since", a.Since},
		{"until", a.Until},
		{"pathspec", a.Pathspec},
		{"ref", a.Ref},
		{"rev", a.Rev},
	} {
		if strings.HasPrefix(f.val, "-") {
			return nil, &proto.Error{
				Code: "args",
				Msg:  f.name + " may not begin with '-'",
				Hint: "a git revision, date, author, or pathspec never starts with '-'",
			}
		}
	}
	if perr := jail.CheckPaths(map[string]string{
		"path": a.Path,
	}); perr != nil {
		return nil, perr
	}
	return a, nil
}

// Run dispatches by op. Each op's runner returns its typed result and
// is responsible for its own tracer instrumentation.
func Run(a *Args, tr *proto.Tracer) (*Result, *proto.Error) {
	switch a.Op {
	case "status":
		s, perr := runStatus(a, tr)
		if perr != nil {
			return nil, perr
		}
		return &Result{Op: "status", Status: s}, nil
	case "log":
		l, perr := runLog(a, tr)
		if perr != nil {
			return nil, perr
		}
		return &Result{Op: "log", Log: l}, nil
	case "diff":
		d, perr := runDiff(a, tr)
		if perr != nil {
			return nil, perr
		}
		return &Result{Op: "diff", Diff: d}, nil
	case "show":
		s, perr := runShow(a, tr)
		if perr != nil {
			return nil, perr
		}
		return &Result{Op: "show", Show: s}, nil
	case "blame":
		b, perr := runBlame(a, tr)
		if perr != nil {
			return nil, perr
		}
		return &Result{Op: "blame", Blame: b}, nil
	default:
		return nil, &proto.Error{Code: "unknown_op", Msg: "unknown op: " + a.Op, Hint: "live ops: status, log, diff, show, blame"}
	}
}

// PrettyResponse routes by op. Each op has its own pretty-renderer next
// to its parser.
func PrettyResponse(req *proto.Request, rsp *proto.Response) string {
	if !rsp.OK {
		return proto.PrettyResponseHeader(rsp)
	}
	var r Result
	if err := proto.UnmarshalData(rsp, &r); err != nil {
		return "ok\n<unrecognized git result>"
	}
	switch r.Op {
	case "status":
		return prettyStatus(r.Status)
	case "log":
		return prettyLog(r.Log)
	case "diff":
		return prettyDiff(r.Diff)
	case "show":
		return prettyShow(r.Show)
	case "blame":
		return prettyBlame(r.Blame)
	default:
		return "ok\n<unknown git op: " + r.Op + ">"
	}
}

// CompactResponse returns an array-of-arrays for row-shaped git ops (log, diff,
// show). Status is not row-shaped so it falls back to the json-decoded object.
func CompactResponse(rsp *proto.Response) (any, error) {
	if !rsp.OK {
		return nil, nil
	}
	var r Result
	if err := proto.UnmarshalData(rsp, &r); err != nil {
		return nil, err
	}
	switch r.Op {
	case "log":
		if r.Log == nil {
			return nil, nil
		}
		cd := proto.CompactData{
			K: []string{"sha", "short", "aname", "aemail", "atime", "cname", "cemail", "ctime", "subj", "body", "parents"},
			R: make([][]any, len(r.Log.Commits)),
		}
		for i, c := range r.Log.Commits {
			cd.R[i] = []any{
				c.SHA, c.ShortSHA,
				c.AuthorName, c.AuthorEmail, c.AuthorTime,
				c.CommitterName, c.CommitterEmail, c.CommitterTime,
				c.Subject, c.Body, c.Parents,
			}
		}
		return map[string]any{"op": "log", "k": cd.K, "r": cd.R}, nil
	case "diff":
		if r.Diff == nil {
			return nil, nil
		}
		return compactDiffResult("diff", r.Diff), nil
	case "show":
		if r.Show == nil {
			return nil, nil
		}
		m := compactDiffResult("show", &r.Show.Diff).(map[string]any)
		m["commit"] = r.Show.Commit
		return m, nil
	case "blame":
		if r.Blame == nil {
			return nil, nil
		}
		cd := proto.CompactData{
			K: []string{"sha", "short", "aname", "atime", "start", "lines"},
			R: make([][]any, len(r.Blame.Hunks)),
		}
		for i, h := range r.Blame.Hunks {
			cd.R[i] = []any{h.SHA, h.ShortSHA, h.AuthorName, h.AuthorTime, h.StartLine, h.Lines}
		}
		return map[string]any{
			"op":   "blame",
			"path": r.Blame.Path,
			"rev":  r.Blame.Rev,
			"k":    cd.K,
			"r":    cd.R,
		}, nil
	default:
		// status and unknown ops: fall back to json-decoded object
		return nil, nil
	}
}

func compactDiffResult(op string, d *DiffResult) any {
	cd := proto.CompactData{
		K: []string{"path", "old", "status", "bin", "add", "del", "patch"},
		R: make([][]any, len(d.Files)),
	}
	for i, f := range d.Files {
		cd.R[i] = []any{f.Path, f.OldPath, f.Status, f.Binary, f.Additions, f.Deletions, f.Patch}
	}
	return map[string]any{
		"op":              op,
		"k":               cd.K,
		"r":               cd.R,
		"total_additions": d.TotalAdditions,
		"total_deletions": d.TotalDeletions,
		"stat_only":       d.StatOnly,
	}
}
