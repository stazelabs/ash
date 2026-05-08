// Package find implements the `find` verb.
//
// Args:
//
//	path                string  (required) - starting directory
//	glob                string  (optional) - doublestar pattern, default "**"
//	type                string  (optional) - "any" | "file" | "dir" | "symlink", default "any"
//	max_depth           int     (optional) - 0 means unlimited; 0 = path itself only otherwise
//	limit               int     (optional) - cap on records, default 256, hard cap 4096
//	exclude             string  (optional) - doublestar pattern; matches are skipped
//	include_hidden      bool    (optional) - if false (default), directories whose name
//	                                         starts with "." are not recursed into. Leaf
//	                                         files starting with "." are still findable.
//	respect_gitignore   bool    (optional) - if true (default), the .gitignore at the walk
//	                                         root is loaded and its rules exclude matching
//	                                         paths. Nested .gitignore files are NOT yet
//	                                         honored. Pass false for raw filesystem walk.
//
// Path semantics mirror Unix find: the form of paths in results matches the
// form of --path (relative-in -> relative-out, absolute-in -> absolute-out).
//
// Symlinks are reported as their own type and never followed; this prevents
// loops and avoids implicitly escaping the walk root.
package find

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/stazelabs/ash/internal/proto"
	"github.com/stazelabs/ash/internal/verbs/argutil"
	"github.com/stazelabs/ash/internal/walker"
)

const (
	DefaultLimit = 256
	MaxLimit     = 4096
	DefaultGlob  = "**"
)

type Args struct {
	Path             string
	Glob             string
	Type             string
	MaxDepth         int // 0 = unlimited
	Limit            int
	Exclude          string
	IncludeHidden    bool
	RespectGitignore bool
}

type Record struct {
	Path  string `msgpack:"path"`
	Type  string `msgpack:"type"`             // "file" | "dir" | "symlink"
	Size  int64  `msgpack:"size,omitempty"`   // bytes; omitted for dirs and symlinks
	Mtime int64  `msgpack:"mtime"`            // unix nanos
}

type Result struct {
	Records        []Record `msgpack:"records"`
	Count          int      `msgpack:"count"`
	Truncated      bool     `msgpack:"truncated,omitempty"`
	TruncationHint string   `msgpack:"truncation_hint,omitempty"`
}

func ParseArgs(in map[string]any) (*Args, *proto.Error) {
	a := &Args{}
	var perr *proto.Error
	if a.Path, perr = argutil.RequireString(in, "path"); perr != nil {
		return nil, perr
	}
	if a.Glob, perr = argutil.OptionalNonEmptyString(in, "glob", DefaultGlob); perr != nil {
		return nil, perr
	}
	if a.Type, perr = argutil.OptionalEnum(in, "type", "any", []string{"any", "file", "dir", "symlink"}); perr != nil {
		return nil, perr
	}
	if a.MaxDepth, perr = argutil.OptionalNonNegInt(in, "max_depth", 0, 0); perr != nil {
		return nil, perr
	}
	if a.Limit, perr = argutil.OptionalPosInt(in, "limit", DefaultLimit, MaxLimit); perr != nil {
		return nil, perr
	}
	if a.Exclude, perr = argutil.OptionalString(in, "exclude", ""); perr != nil {
		return nil, perr
	}
	if a.IncludeHidden, perr = argutil.OptionalBool(in, "include_hidden", false); perr != nil {
		return nil, perr
	}
	if a.RespectGitignore, perr = argutil.OptionalBool(in, "respect_gitignore", true); perr != nil {
		return nil, perr
	}
	if !doublestar.ValidatePathPattern(a.Glob) {
		return nil, &proto.Error{Code: "args", Msg: "glob is not a valid pattern: " + a.Glob}
	}
	if a.Exclude != "" && !doublestar.ValidatePathPattern(a.Exclude) {
		return nil, &proto.Error{Code: "args", Msg: "exclude is not a valid pattern: " + a.Exclude}
	}
	return a, nil
}

// Run walks the tree and produces records matching the args. tr may be
// nil; tests pass nil to skip phase timing.
func Run(a *Args, tr *proto.Tracer) (*Result, *proto.Error) {
	info, err := os.Stat(a.Path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, &proto.Error{Code: "not_found", Msg: a.Path + ": no such path"}
		}
		if errors.Is(err, fs.ErrPermission) {
			return nil, &proto.Error{Code: "permission", Msg: err.Error()}
		}
		return nil, &proto.Error{Code: "stat", Msg: err.Error()}
	}
	if !info.IsDir() {
		return nil, &proto.Error{Code: "not_dir", Msg: a.Path + " is not a directory; use `read` for files"}
	}

	res := &Result{Records: make([]Record, 0, 32)}
	limitHit := false

	walkStart := time.Now()
	walkErr := walker.Walk(a.Path, walker.Options{
		Glob:             a.Glob,
		Exclude:          a.Exclude,
		MaxDepth:         a.MaxDepth,
		IncludeHidden:    a.IncludeHidden,
		RespectGitignore: a.RespectGitignore,
	}, func(e walker.Entry) (walker.Action, error) {
		if !typeMatches(a.Type, e.Type) {
			return walker.Continue, nil
		}
		// Info may be nil if d.Info() failed mid-walk (entry vanished).
		// Treat as silently un-emittable rather than blowing up the walk.
		if e.Info == nil {
			return walker.Continue, nil
		}
		rec := Record{
			Path:  e.Path,
			Type:  e.Type,
			Mtime: e.Info.ModTime().UnixNano(),
		}
		if e.Type == "file" {
			rec.Size = e.Info.Size()
		}
		res.Records = append(res.Records, rec)
		if len(res.Records) >= a.Limit {
			limitHit = true
			return walker.Stop, nil
		}
		return walker.Continue, nil
	})
	tr.AddWalk(time.Since(walkStart))
	if walkErr != nil {
		return nil, &proto.Error{Code: "walk", Msg: walkErr.Error()}
	}

	res.Count = len(res.Records)
	if limitHit {
		res.Truncated = true
		res.TruncationHint = truncationHint(a.Limit)
	}
	return res, nil
}

// truncationHint adapts the message to whether the user hit their own
// --limit (raisable up to MaxLimit) or the hard cap itself (not raisable;
// only narrowing helps). ASH-12.
func truncationHint(limit int) string {
	if limit >= MaxLimit {
		return fmt.Sprintf(
			"hit hard cap of %d records. narrow with --glob, --type, --max_depth, or --exclude — --limit cannot go higher.",
			MaxLimit,
		)
	}
	return fmt.Sprintf(
		"hit limit of %d records. narrow with --glob, --type, --max_depth, or --exclude; or raise --limit (max %d).",
		limit, MaxLimit,
	)
}

func typeMatches(want, got string) bool {
	if want == "any" {
		return true
	}
	return want == got
}

// PrettyResponse renders the find response in canonical line-oriented form.
// Used both for client display and daemon-side token counting.
func PrettyResponse(req *proto.Request, rsp *proto.Response) string {
	if !rsp.OK {
		return proto.PrettyResponseHeader(rsp)
	}
	r, ok := decodeResult(rsp.Data)
	if !ok {
		return "ok\n<unrecognized find result>"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "=== ash find: %d results", r.Count)
	if scope := scopeFromArgs(req); scope != "" {
		fmt.Fprintf(&b, " [%s]", scope)
	}
	if r.Truncated {
		b.WriteString(" TRUNCATED")
	}
	b.WriteString(" ===\n")
	for _, rec := range r.Records {
		writeRecord(&b, rec)
	}
	if r.Truncated {
		b.WriteString("\n[truncation: ")
		b.WriteString(r.TruncationHint)
		b.WriteString("]")
	}
	return strings.TrimRight(b.String(), "\n")
}

func writeRecord(b *strings.Builder, r Record) {
	switch r.Type {
	case "dir":
		b.WriteByte('D')
	case "symlink":
		b.WriteByte('L')
	default:
		b.WriteByte('F')
	}
	b.WriteByte(' ')
	b.WriteString(strconv.FormatInt(r.Size, 10))
	b.WriteByte(' ')
	b.WriteString(time.Unix(0, r.Mtime).UTC().Format("2006-01-02"))
	b.WriteByte(' ')
	b.WriteString(r.Path)
	b.WriteByte('\n')
}

func scopeFromArgs(req *proto.Request) string {
	if req == nil || req.Args == nil {
		return ""
	}
	parts := make([]string, 0, 4)
	if v, ok := req.Args["path"].(string); ok {
		parts = append(parts, "path="+v)
	}
	if v, ok := req.Args["glob"].(string); ok && v != "" && v != DefaultGlob {
		parts = append(parts, "glob="+v)
	}
	if v, ok := req.Args["type"].(string); ok && v != "" && v != "any" {
		parts = append(parts, "type="+v)
	}
	// respect_gitignore is shown only when explicitly disabled, since true is
	// the default. Same idea for include_hidden: hide the default, show the
	// override.
	if v, ok := req.Args["respect_gitignore"]; ok {
		if b, ok := argutil.ToBool(v); ok && !b {
			parts = append(parts, "respect_gitignore=false")
		}
	}
	if v, ok := req.Args["include_hidden"]; ok {
		if b, ok := argutil.ToBool(v); ok && b {
			parts = append(parts, "include_hidden=true")
		}
	}
	return strings.Join(parts, ", ")
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
	if recs, ok := m["records"].([]any); ok {
		for _, x := range recs {
			rm, ok := x.(map[string]any)
			if !ok {
				continue
			}
			rec := Record{}
			if v, ok := rm["path"].(string); ok {
				rec.Path = v
			}
			if v, ok := rm["type"].(string); ok {
				rec.Type = v
			}
			if v, ok := argutil.ToInt64(rm["size"]); ok {
				rec.Size = v
			}
			if v, ok := argutil.ToInt64(rm["mtime"]); ok {
				rec.Mtime = v
			}
			r.Records = append(r.Records, rec)
		}
	}
	if v, ok := argutil.ToInt(m["count"]); ok {
		r.Count = v
	}
	if v, ok := m["truncated"].(bool); ok {
		r.Truncated = v
	}
	if v, ok := m["truncation_hint"].(string); ok {
		r.TruncationHint = v
	}
	return r, true
}

