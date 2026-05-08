// Package stat implements the `stat` verb.
//
// Args:
//
//	paths   string (required) - comma-separated list of paths to inspect
//
// Returns one Entry per path. Paths that do not exist or are inaccessible
// produce a non-nil Error field rather than failing the whole call, so a
// single missing path never aborts a bulk lookup.
//
// Lstat is used throughout, so symlinks are reported as their own type
// rather than being silently resolved — consistent with find's behavior.
package stat

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"time"

	"github.com/stazelabs/ash/internal/proto"
	"github.com/stazelabs/ash/internal/verbs/argutil"
)

type Args struct {
	Paths []string
}

// Entry holds the metadata for one path. Fields are omitted from the wire
// when zero/empty so the envelope stays small for bulk calls.
type Entry struct {
	Path       string `msgpack:"path"`
	Type       string `msgpack:"type,omitempty"`        // "file" | "dir" | "symlink"
	Size       int64  `msgpack:"size,omitempty"`        // bytes; only for files
	Mtime      int64  `msgpack:"mtime,omitempty"`       // unix nanos
	Mode       string `msgpack:"mode,omitempty"`        // e.g. "0644"
	LinkTarget string `msgpack:"link_target,omitempty"` // readlink output; only for symlinks
	Error      string `msgpack:"error,omitempty"`       // "not_found" | "permission" | "stat"
}

type Result struct {
	Entries []Entry `msgpack:"entries"`
	Count   int     `msgpack:"count"`
	Errors  int     `msgpack:"errors,omitempty"` // entries with a non-empty Error field
}

func ParseArgs(in map[string]any) (*Args, *proto.Error) {
	raw, perr := argutil.RequireString(in, "paths")
	if perr != nil {
		return nil, perr
	}
	parts := strings.Split(raw, ",")
	paths := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			paths = append(paths, p)
		}
	}
	if len(paths) == 0 {
		return nil, &proto.Error{Code: "args", Msg: "paths must contain at least one non-empty path"}
	}
	return &Args{Paths: paths}, nil
}

// Run stats each path in order. The tracer is unused: each stat is a single
// syscall and there is no walk phase to instrument.
func Run(a *Args, _ *proto.Tracer) (*Result, *proto.Error) {
	res := &Result{Entries: make([]Entry, 0, len(a.Paths))}
	for _, p := range a.Paths {
		e := statOne(p)
		res.Entries = append(res.Entries, e)
		if e.Error != "" {
			res.Errors++
		}
	}
	res.Count = len(res.Entries)
	return res, nil
}

func statOne(path string) Entry {
	info, err := os.Lstat(path)
	if err != nil {
		e := Entry{Path: path}
		switch {
		case errors.Is(err, fs.ErrNotExist):
			e.Error = "not_found"
		case errors.Is(err, fs.ErrPermission):
			e.Error = "permission"
		default:
			e.Error = "stat"
		}
		return e
	}

	e := Entry{
		Path:  path,
		Mtime: info.ModTime().UnixNano(),
		Mode:  fmt.Sprintf("%04o", info.Mode().Perm()),
	}
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		e.Type = "symlink"
		if target, lerr := os.Readlink(path); lerr == nil {
			e.LinkTarget = target
		}
	case info.IsDir():
		e.Type = "dir"
	default:
		e.Type = "file"
		e.Size = info.Size()
	}
	return e
}

// PrettyResponse renders the stat response in a tabular line-per-entry form.
func PrettyResponse(req *proto.Request, rsp *proto.Response) string {
	if !rsp.OK {
		return proto.PrettyResponseHeader(rsp)
	}
	r, ok := decodeResult(rsp.Data)
	if !ok {
		return "ok\n<unrecognized stat result>"
	}
	var b strings.Builder
	header := fmt.Sprintf("=== ash stat: %d path(s)", r.Count)
	if r.Errors > 0 {
		header += fmt.Sprintf(", %d error(s)", r.Errors)
	}
	header += " ==="
	b.WriteString(header)
	b.WriteByte('\n')
	for _, e := range r.Entries {
		writeEntry(&b, e)
	}
	return strings.TrimRight(b.String(), "\n")
}

func writeEntry(b *strings.Builder, e Entry) {
	if e.Error != "" {
		fmt.Fprintf(b, "? %-10s %-4s %-20s %s [%s]\n", "-", "-", "-", e.Path, e.Error)
		return
	}
	typeChar := "F"
	switch e.Type {
	case "dir":
		typeChar = "D"
	case "symlink":
		typeChar = "L"
	}
	sizeStr := "-"
	if e.Type == "file" {
		sizeStr = fmt.Sprintf("%d", e.Size)
	}
	mtime := time.Unix(0, e.Mtime).UTC().Format("2006-01-02T15:04:05Z")
	if e.LinkTarget != "" {
		fmt.Fprintf(b, "%s %-10s %s %s %s -> %s\n", typeChar, sizeStr, e.Mode, mtime, e.Path, e.LinkTarget)
	} else {
		fmt.Fprintf(b, "%s %-10s %s %s %s\n", typeChar, sizeStr, e.Mode, mtime, e.Path)
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
	if raw, ok := m["entries"].([]any); ok {
		for _, x := range raw {
			em, ok := x.(map[string]any)
			if !ok {
				continue
			}
			var e Entry
			if v, ok := em["path"].(string); ok {
				e.Path = v
			}
			if v, ok := em["type"].(string); ok {
				e.Type = v
			}
			if v, ok := argutil.ToInt64(em["size"]); ok {
				e.Size = v
			}
			if v, ok := argutil.ToInt64(em["mtime"]); ok {
				e.Mtime = v
			}
			if v, ok := em["mode"].(string); ok {
				e.Mode = v
			}
			if v, ok := em["link_target"].(string); ok {
				e.LinkTarget = v
			}
			if v, ok := em["error"].(string); ok {
				e.Error = v
			}
			r.Entries = append(r.Entries, e)
		}
	}
	if v, ok := argutil.ToInt(m["count"]); ok {
		r.Count = v
	}
	if v, ok := argutil.ToInt(m["errors"]); ok {
		r.Errors = v
	}
	return r, true
}
