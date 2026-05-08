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
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/stazelabs/ash/internal/gitignore"
	"github.com/stazelabs/ash/internal/proto"
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
	a := &Args{
		Glob:             DefaultGlob,
		Type:             "any",
		Limit:            DefaultLimit,
		RespectGitignore: true,
	}
	pv, ok := in["path"]
	if !ok {
		return nil, &proto.Error{Code: "args", Msg: "missing required arg: path"}
	}
	ps, ok := pv.(string)
	if !ok || ps == "" {
		return nil, &proto.Error{Code: "args", Msg: "path must be a non-empty string"}
	}
	a.Path = ps

	if v, ok := in["glob"]; ok && v != nil {
		s, ok := v.(string)
		if !ok || s == "" {
			return nil, &proto.Error{Code: "args", Msg: "glob must be a non-empty string"}
		}
		a.Glob = s
	}
	if v, ok := in["type"]; ok && v != nil {
		s, ok := v.(string)
		if !ok {
			return nil, &proto.Error{Code: "args", Msg: "type must be a string"}
		}
		switch s {
		case "any", "file", "dir", "symlink":
			a.Type = s
		default:
			return nil, &proto.Error{Code: "args", Msg: `type must be one of: any, file, dir, symlink`}
		}
	}
	if v, ok := in["max_depth"]; ok && v != nil {
		n, ok := toInt(v)
		if !ok || n < 0 {
			return nil, &proto.Error{Code: "args", Msg: "max_depth must be a non-negative integer"}
		}
		a.MaxDepth = n
	}
	if v, ok := in["limit"]; ok && v != nil {
		n, ok := toInt(v)
		if !ok || n <= 0 {
			return nil, &proto.Error{Code: "args", Msg: "limit must be a positive integer"}
		}
		if n > MaxLimit {
			n = MaxLimit
		}
		a.Limit = n
	}
	if v, ok := in["exclude"]; ok && v != nil {
		s, ok := v.(string)
		if !ok {
			return nil, &proto.Error{Code: "args", Msg: "exclude must be a string"}
		}
		a.Exclude = s
	}
	if v, ok := in["include_hidden"]; ok && v != nil {
		b, ok := toBool(v)
		if !ok {
			return nil, &proto.Error{Code: "args", Msg: "include_hidden must be a bool (true/false)"}
		}
		a.IncludeHidden = b
	}
	if v, ok := in["respect_gitignore"]; ok && v != nil {
		b, ok := toBool(v)
		if !ok {
			return nil, &proto.Error{Code: "args", Msg: "respect_gitignore must be a bool (true/false)"}
		}
		a.RespectGitignore = b
	}
	if !doublestar.ValidatePathPattern(a.Glob) {
		return nil, &proto.Error{Code: "args", Msg: "glob is not a valid pattern: " + a.Glob}
	}
	if a.Exclude != "" && !doublestar.ValidatePathPattern(a.Exclude) {
		return nil, &proto.Error{Code: "args", Msg: "exclude is not a valid pattern: " + a.Exclude}
	}
	return a, nil
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
	case string:
		i, err := strconv.Atoi(n)
		return i, err == nil
	}
	return 0, false
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

func Run(a *Args) (*Result, *proto.Error) {
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

	// rootDepth lets us compare relative depth without re-counting separators.
	rootSep := strings.Count(filepath.Clean(a.Path), string(filepath.Separator))
	limitHit := false

	// gitignore matcher is loaded from the walk root if respect_gitignore is on.
	// nil matcher (no .gitignore present, or feature disabled) excludes nothing.
	var gi *gitignore.Matcher
	if a.RespectGitignore {
		m, err := gitignore.LoadFromDir(a.Path)
		if err != nil {
			return nil, &proto.Error{Code: "gitignore", Msg: err.Error()}
		}
		gi = m
	}

	walkErr := filepath.WalkDir(a.Path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			// Surface permission errors as skips, not failures. The walk
			// keeps going; the agent gets a partial answer rather than an
			// abort.
			if errors.Is(err, fs.ErrPermission) {
				if d != nil && d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			return err
		}
		if p == a.Path {
			return nil // never include the walk root in results
		}
		base := filepath.Base(p)
		// Hidden directory skip applies to directories only; leaf dotfiles
		// remain findable. The walk root is already excluded above so this
		// only fires for descendants.
		if !a.IncludeHidden && d.IsDir() && strings.HasPrefix(base, ".") {
			return fs.SkipDir
		}
		// Depth guard: max_depth=0 means unlimited; 1 means direct children only.
		if a.MaxDepth > 0 {
			depth := strings.Count(filepath.Clean(p), string(filepath.Separator)) - rootSep
			if depth > a.MaxDepth {
				if d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
		}

		rel, _ := filepath.Rel(a.Path, p)
		matchPath := filepath.ToSlash(rel)

		if gi.Excludes(matchPath, d.IsDir()) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}

		if a.Exclude != "" {
			ex, _ := doublestar.Match(a.Exclude, matchPath)
			if ex {
				if d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
		}

		// Determine entry type.
		var typ string
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			typ = "symlink"
		case d.IsDir():
			typ = "dir"
		default:
			typ = "file"
		}

		if !typeMatches(a.Type, typ) {
			return nil
		}

		matched, _ := doublestar.Match(a.Glob, matchPath)
		if !matched {
			return nil
		}

		fi, err := d.Info()
		if err != nil {
			return nil
		}
		rec := Record{
			Path:  outputPath(a.Path, p),
			Type:  typ,
			Mtime: fi.ModTime().UnixNano(),
		}
		if typ == "file" {
			rec.Size = fi.Size()
		}
		res.Records = append(res.Records, rec)
		if len(res.Records) >= a.Limit {
			limitHit = true
			return fs.SkipAll
		}
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, fs.SkipAll) {
		return nil, &proto.Error{Code: "walk", Msg: walkErr.Error()}
	}

	res.Count = len(res.Records)
	if limitHit {
		res.Truncated = true
		res.TruncationHint = fmt.Sprintf(
			"hit limit of %d records. narrow with --glob, --type, --max_depth, or --exclude; or raise --limit (max %d).",
			a.Limit, MaxLimit,
		)
	}
	return res, nil
}

// outputPath returns the path in the form that matches the form of --path:
// relative-in produces relative-out, absolute-in produces absolute-out.
func outputPath(root, full string) string {
	if filepath.IsAbs(root) {
		return full
	}
	rel, err := filepath.Rel(".", full)
	if err != nil {
		return full
	}
	return rel
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
		if b, ok := toBool(v); ok && !b {
			parts = append(parts, "respect_gitignore=false")
		}
	}
	if v, ok := req.Args["include_hidden"]; ok {
		if b, ok := toBool(v); ok && b {
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
			if v, ok := toInt64(rm["size"]); ok {
				rec.Size = v
			}
			if v, ok := toInt64(rm["mtime"]); ok {
				rec.Mtime = v
			}
			r.Records = append(r.Records, rec)
		}
	}
	if v, ok := toInt(m["count"]); ok {
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
