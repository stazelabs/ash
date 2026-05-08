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
//
// Implementation shells out to system `git` and parses documented
// machine-readable formats (porcelain v2 for status, NUL-separated
// custom format for log). The agent never sees porcelain; they get a
// typed Result.
//
// The README's example uses positional subcommand syntax (`git diff
// --range ...`); ash's flags-only client rule rules that out, so we use
// `--op <subcommand>` for consistency with every other verb.
package git

import (
	"strings"

	"github.com/stazelabs/ash/internal/proto"
)

// Result is the wire envelope. Op is always set; the populated payload
// depends on which op was requested. Future ops add their own pointer
// field; only one is non-nil per response.
type Result struct {
	Op     string        `msgpack:"op"`
	Status *StatusResult `msgpack:"status,omitempty"`
	Log    *LogResult    `msgpack:"log,omitempty"`
}

type Args struct {
	Op   string
	Path string
	// status-op flags
	Untracked bool
	Ignored   bool
	// log-op flags
	Limit    int
	Range    string
	Author   string
	Since    string
	Until    string
	Pathspec string
}

func ParseArgs(in map[string]any) (*Args, *proto.Error) {
	a := &Args{
		Path:      ".",
		Untracked: true,
		Limit:     LogDefaultLimit,
	}
	opV, ok := in["op"]
	if !ok {
		return nil, &proto.Error{Code: "args", Msg: "missing required arg: op"}
	}
	op, ok := opV.(string)
	if !ok || op == "" {
		return nil, &proto.Error{Code: "args", Msg: "op must be a non-empty string"}
	}
	a.Op = op

	if v, ok := in["path"]; ok && v != nil {
		s, ok := v.(string)
		if !ok || s == "" {
			return nil, &proto.Error{Code: "args", Msg: "path must be a non-empty string"}
		}
		a.Path = s
	}
	if v, ok := in["untracked"]; ok && v != nil {
		b, ok := toBool(v)
		if !ok {
			return nil, &proto.Error{Code: "args", Msg: "untracked must be a bool (true/false)"}
		}
		a.Untracked = b
	}
	if v, ok := in["ignored"]; ok && v != nil {
		b, ok := toBool(v)
		if !ok {
			return nil, &proto.Error{Code: "args", Msg: "ignored must be a bool (true/false)"}
		}
		a.Ignored = b
	}
	// log-op flags. Strings pass through to git unmodified; git itself is
	// the validator for date formats, refspecs, and pathspecs.
	if v, ok := in["limit"]; ok && v != nil {
		n, ok := toInt(v)
		if !ok || n <= 0 {
			return nil, &proto.Error{Code: "args", Msg: "limit must be a positive integer"}
		}
		if n > LogMaxLimit {
			n = LogMaxLimit
		}
		a.Limit = n
	}
	if v, ok := in["range"]; ok && v != nil {
		s, ok := v.(string)
		if !ok {
			return nil, &proto.Error{Code: "args", Msg: "range must be a string"}
		}
		a.Range = s
	}
	if v, ok := in["author"]; ok && v != nil {
		s, ok := v.(string)
		if !ok {
			return nil, &proto.Error{Code: "args", Msg: "author must be a string"}
		}
		a.Author = s
	}
	if v, ok := in["since"]; ok && v != nil {
		s, ok := v.(string)
		if !ok {
			return nil, &proto.Error{Code: "args", Msg: "since must be a string"}
		}
		a.Since = s
	}
	if v, ok := in["until"]; ok && v != nil {
		s, ok := v.(string)
		if !ok {
			return nil, &proto.Error{Code: "args", Msg: "until must be a string"}
		}
		a.Until = s
	}
	if v, ok := in["pathspec"]; ok && v != nil {
		s, ok := v.(string)
		if !ok {
			return nil, &proto.Error{Code: "args", Msg: "pathspec must be a string"}
		}
		a.Pathspec = s
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
	default:
		return nil, &proto.Error{Code: "unknown_op", Msg: "unknown op: " + a.Op + " (live ops: status, log)"}
	}
}

// PrettyResponse routes by op. Each op has its own pretty-renderer next
// to its parser.
func PrettyResponse(req *proto.Request, rsp *proto.Response) string {
	if !rsp.OK {
		return proto.PrettyResponseHeader(rsp)
	}
	r, ok := decodeResult(rsp.Data)
	if !ok {
		return "ok\n<unrecognized git result>"
	}
	switch r.Op {
	case "status":
		return prettyStatus(r.Status)
	case "log":
		return prettyLog(r.Log)
	default:
		return "ok\n<unknown git op: " + r.Op + ">"
	}
}

func decodeResult(data any) (*Result, bool) {
	if r, ok := data.(*Result); ok {
		return r, true
	}
	m, ok := data.(map[string]any)
	if !ok {
		return nil, false
	}
	r := &Result{}
	if v, ok := m["op"].(string); ok {
		r.Op = v
	}
	if sm, ok := m["status"].(map[string]any); ok {
		r.Status = decodeStatus(sm)
	}
	if lm, ok := m["log"].(map[string]any); ok {
		r.Log = decodeLog(lm)
	}
	return r, true
}

func toBool(v any) (bool, bool) {
	switch n := v.(type) {
	case bool:
		return n, true
	case string:
		switch strings.ToLower(n) {
		case "true", "1", "yes":
			return true, true
		case "false", "0", "no":
			return false, true
		}
	}
	return false, false
}

func toInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case uint64:
		return int(n), true
	case float64:
		return int(n), true
	}
	return 0, false
}

func toInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int64:
		return n, true
	case uint64:
		return int64(n), true
	case float64:
		return int64(n), true
	}
	return 0, false
}
