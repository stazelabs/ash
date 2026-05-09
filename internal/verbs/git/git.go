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
//	         --pathspec, --stat, --context, --limit_bytes.
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
	"strings"

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
		return &proto.Error{Code: "not_a_repo", Msg: path + " is not inside a git repository"}
	}
	return &proto.Error{Code: "git_failed", Msg: msg}
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
	// diff-op flags
	Staged    bool
	Context   int
	StatOnly  bool
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
	// log-op flags. Strings pass through to git unmodified; git itself is
	// the validator for date formats, refspecs, and pathspecs.
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
	if a.LimitBytes, perr = argutil.OptionalPosInt(in, "limit_bytes", DiffDefaultLimitBytes, DiffMaxLimitBytes); perr != nil {
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
	default:
		return nil, &proto.Error{Code: "unknown_op", Msg: "unknown op: " + a.Op + " (live ops: status, log, diff, show)"}
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
	default:
		return "ok\n<unknown git op: " + r.Op + ">"
	}
}
