// Package find implements the `find` verb.
//
// Args:
//
//	path                string  (required) - starting directory
//	glob                string  (optional) - doublestar pattern, default "**"
//	type                string  (optional) - "any" | "file" | "dir" | "symlink"
//	depth               int     (optional) - max directory depth; 0 = unlimited
//	limit               int     (optional) - cap on records, default 256, hard cap 4096
//	exclude             string  (optional) - doublestar pattern; matches are skipped
//	hidden              bool    (optional) - include hidden dirs (default false)
//	gi                  bool    (optional) - respect .gitignore (default true)
//	meta                bool    (optional) - include size+mtime in pretty form
//	absolute            bool    (optional) - emit absolute paths (default false)
//
// Path semantics (ASH-71): records carry repo-root-relative paths by default
// regardless of --path form, so the project root never re-appears on every
// line. Pass --absolute true to opt back into the input-mirroring legacy
// form. Paths under jail.allow_paths that sit outside the project root fall
// back to absolute even in default mode (relative-with-".." is not a win).
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
	"github.com/stazelabs/ash/internal/jail"
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
	WithMeta         bool // include size + mtime + type prefix in pretty form
	Absolute         bool // emit absolute paths instead of repo-root-relative
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
	TruncInfo      *proto.TruncInfo `msgpack:"truncation_hint,omitempty"`
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
	if a.Type, perr = parseType(in); perr != nil {
		return nil, perr
	}
	if a.MaxDepth, perr = argutil.OptionalNonNegInt(in, "depth", 0, 0); perr != nil {
		return nil, perr
	}
	if a.Limit, perr = argutil.OptionalPosInt(in, "limit", DefaultLimit, MaxLimit); perr != nil {
		return nil, perr
	}
	if a.Exclude, perr = argutil.OptionalString(in, "exclude", ""); perr != nil {
		return nil, perr
	}
	if a.IncludeHidden, perr = argutil.OptionalBool(in, "hidden", false); perr != nil {
		return nil, perr
	}
	if a.RespectGitignore, perr = argutil.OptionalBool(in, "gi", true); perr != nil {
		return nil, perr
	}
	if a.WithMeta, perr = argutil.OptionalBool(in, "meta", false); perr != nil {
		return nil, perr
	}
	if a.Absolute, perr = argutil.OptionalBool(in, "absolute", false); perr != nil {
		return nil, perr
	}
	if !doublestar.ValidatePathPattern(a.Glob) {
		return nil, &proto.Error{Code: "args", Msg: "glob is not a valid pattern: " + a.Glob}
	}
	if a.Exclude != "" && !doublestar.ValidatePathPattern(a.Exclude) {
		return nil, &proto.Error{Code: "args", Msg: "exclude is not a valid pattern: " + a.Exclude}
	}
	if perr := jail.CheckPaths(map[string]string{
		"path": a.Path,
	}); perr != nil {
		return nil, perr
	}
	return a, nil
}

// Run walks the tree and produces records matching the args. tr may be
// nil; tests pass nil to skip phase timing.
func Run(a *Args, tr *proto.Tracer) (*Result, *proto.Error) {
	info, err := os.Stat(a.Path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, &proto.Error{Code: "not_found", Msg: jail.PrettyPath(a.Path) + ": no such path"}
		}
		if errors.Is(err, fs.ErrPermission) {
			return nil, &proto.Error{Code: "permission", Msg: err.Error()}
		}
		return nil, &proto.Error{Code: "stat", Msg: err.Error()}
	}
	if !info.IsDir() {
		return nil, &proto.Error{Code: "not_dir", Msg: jail.PrettyPath(a.Path) + ": not a directory", Hint: "use 'ash read' for files"}
	}

	res := &Result{Records: make([]Record, 0, 32)}
	limitHit := false

	ctx := tr.Context()
	walkStart := time.Now()
	walkErr := walker.Walk(a.Path, walker.Options{
		Glob:             a.Glob,
		Exclude:          a.Exclude,
		MaxDepth:         a.MaxDepth,
		IncludeHidden:    a.IncludeHidden,
		RespectGitignore: a.RespectGitignore,
		WantInfo:         true,
	}, func(e walker.Entry) (walker.Action, error) {
		// ASH-106: honor mid-stream cancellation. Non-streaming callers
		// see ctx == context.Background() and never trigger this path.
		if ctx.Err() != nil {
			return walker.Stop, nil
		}
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
		tr.Emit(rec)
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

	if !a.Absolute {
		rel := jail.NewProjectRelativizer(a.Path)
		for i := range res.Records {
			res.Records[i].Path = rel.Apply(res.Records[i].Path)
		}
	}

	res.Count = len(res.Records)
	if limitHit {
		res.Truncated = true
		res.TruncInfo = &proto.TruncInfo{Trunc: 1, Limit: a.Limit, Max: MaxLimit}
	}
	return res, nil
}

// findTruncHint reconstructs the human-readable truncation message from
// structured TruncInfo. Limit==Max signals the hard cap: raising is
// not possible. ASH-76.
func findTruncHint(ti *proto.TruncInfo) string {
	if ti == nil {
		return ""
	}
	if ti.Limit >= ti.Max {
		return fmt.Sprintf(
			"hit hard cap of %d records. --glob/--type/--depth/--exclude — --limit cannot go higher.",
			ti.Max,
		)
	}
	return fmt.Sprintf(
		"hit limit of %d records. --glob/--type/--depth/--exclude/--limit.",
		ti.Limit,
	)
}

func typeMatches(want, got string) bool {
	if want == "any" {
		return true
	}
	return want == got
}

// typeAliases maps common variant forms of --type to canonical values.
// Driven by ledger evidence (ASH-183): agents reflexively reach for
// POSIX-style (`f`/`d`/`l`) or pluralized (`files`/`directories`) forms.
var typeAliases = map[string]string{
	"any":         "any",
	"file":        "file",
	"files":       "file",
	"f":           "file",
	"dir":         "dir",
	"directory":   "dir",
	"directories": "dir",
	"d":           "dir",
	"symlink":     "symlink",
	"symlinks":    "symlink",
	"link":        "symlink",
	"links":       "symlink",
	"l":           "symlink",
}

// parseType reads --type with variant acceptance and an actionable error
// when the value is unrecognized. Replaces argutil.OptionalEnum to fix
// the highest-frequency error class in the 30d ledger (ASH-183).
func parseType(in map[string]any) (string, *proto.Error) {
	v, ok := in["type"]
	if !ok || v == nil {
		return "any", nil
	}
	s, ok := argutil.ToString(v)
	if !ok {
		return "", &proto.Error{Code: "args", Msg: "type must be a string"}
	}
	if canon, ok := typeAliases[strings.ToLower(strings.TrimSpace(s))]; ok {
		return canon, nil
	}
	if strings.ContainsAny(s, "*?[") {
		return "", &proto.Error{
			Code: "args",
			Msg:  fmt.Sprintf("type %q looks like a glob — did you mean --glob=%s?", s, s),
		}
	}
	return "", &proto.Error{
		Code: "args",
		Msg:  fmt.Sprintf("type %q must be one of: any, file, dir, symlink", s),
	}
}

// PrettyResponse renders the find response in canonical line-oriented form.
// Used both for client display and daemon-side token counting.
//
// Default ("lean") form is one path per line, with a trailing `/` on
// directory entries to disambiguate them from files. Size + mtime
// metadata is omitted by default — agents that want it pass
// `--with_meta true` (or follow up with `ash stat` for selected paths).
//
// When jail.allow_paths entries are configured and --absolute is not set,
// a compact alias table is prepended (ASH-85):
//
//	@0 = /Users/me/scratch
//	internal/foo.go
//	@0/notes.md
func PrettyResponse(req *proto.Request, rsp *proto.Response) string {
	if !rsp.OK {
		return proto.PrettyResponseHeader(rsp)
	}
	var r Result
	if err := proto.UnmarshalData(rsp, &r); err != nil {
		return "ok\n<unrecognized find result>"
	}
	withMeta := false
	absolute := false
	if req != nil {
		if v, ok := req.Args["meta"]; ok {
			if b, ok := argutil.ToBool(v); ok {
				withMeta = b
			}
		}
		if v, ok := req.Args["absolute"]; ok {
			if b, ok := argutil.ToBool(v); ok {
				absolute = b
			}
		}
	}

	aliases := jail.NewPrefixAliasTable()
	if absolute {
		aliases = nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "§find: %d results", r.Count)
	// Note: the request args are deliberately not echoed in the header
	// (the agent already has them); only Count and TRUNCATED are novel.
	// scopeFromArgs() remains in this file for potential future use
	// (e.g. --meta=true diagnostic output).
	if r.Truncated {
		b.WriteString(" TRUNCATED")
	}
	b.WriteString("\n")
	if !aliases.Empty() {
		b.WriteString(aliases.Header())
	}
	for _, rec := range r.Records {
		rec.Path = aliases.Apply(rec.Path)
		if withMeta {
			writeRecordFull(&b, rec)
		} else {
			writeRecordLean(&b, rec)
		}
	}
	if r.Truncated && r.TruncInfo != nil {
		b.WriteString("\n[truncation: ")
		b.WriteString(findTruncHint(r.TruncInfo))
		b.WriteString("]")
	}
	return strings.TrimRight(b.String(), "\n")
}

// writeRecordLean is the default pretty row: just the path, with a
// trailing `/` for dirs. Symlinks render as the bare path; the agent
// can `ash stat` to learn the link target if needed.
func writeRecordLean(b *strings.Builder, r Record) {
	b.WriteString(r.Path)
	if r.Type == "dir" {
		b.WriteByte('/')
	}
	b.WriteByte('\n')
}

// writeRecordFull is the verbose row used when --with_meta=true:
// `<F|D|L> <size> <yyyy-mm-dd> <path>`.
func writeRecordFull(b *strings.Builder, r Record) {
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
		parts = append(parts, "path="+jail.PrettyPath(v))
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
	if v, ok := req.Args["gi"]; ok {
		if b, ok := argutil.ToBool(v); ok && !b {
			parts = append(parts, "gi=false")
		}
	}
	if v, ok := req.Args["hidden"]; ok {
		if b, ok := argutil.ToBool(v); ok && b {
			parts = append(parts, "hidden=true")
		}
	}
	return strings.Join(parts, ", ")
}

func CompactResponse(rsp *proto.Response) (any, error) {
	if !rsp.OK {
		return nil, nil
	}
	var r Result
	if err := proto.UnmarshalData(rsp, &r); err != nil {
		return nil, err
	}
	cd := proto.CompactData{
		K: []string{"path", "type", "size", "mtime"},
		R: make([][]any, len(r.Records)),
	}
	for i, rec := range r.Records {
		cd.R[i] = []any{rec.Path, rec.Type, rec.Size, rec.Mtime}
	}
	return cd, nil
}
