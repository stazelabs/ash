// Package stat implements the `stat` verb.
//
// Args:
//
//	paths   string (required) - comma-separated list of paths to inspect
//	path    string (alias)    - accepted as an alias for --paths for single-path ergonomics
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

	"github.com/stazelabs/ash/internal/jail"
	"github.com/stazelabs/ash/internal/proto"
	"github.com/stazelabs/ash/internal/verbs/argutil"
)

type Args struct {
	Paths          []string
	FollowSymlinks bool
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
	// Accept --paths (canonical) or --path (alias for the single-path case).
	raw, perr := argutil.OptionalString(in, "paths", "")
	if perr != nil {
		return nil, perr
	}
	if raw == "" {
		raw, perr = argutil.OptionalString(in, "path", "")
		if perr != nil {
			return nil, perr
		}
		if raw == "" {
			return nil, &proto.Error{Code: "args", Msg: "--paths (or --path) is required"}
		}
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
	follow, perr2 := argutil.OptionalBool(in, "follow", false)
	if perr2 != nil {
		return nil, perr2
	}
	check := make(map[string]string, len(paths))
	for i, p := range paths {
		check[fmt.Sprintf("paths[%d]", i)] = p
	}
	if perr := jail.CheckPaths(check); perr != nil {
		return nil, perr
	}
	return &Args{Paths: paths, FollowSymlinks: follow}, nil
}

// Run stats each path in order. The tracer is unused: each stat is a single
// syscall and there is no walk phase to instrument.
func Run(a *Args, _ *proto.Tracer) (*Result, *proto.Error) {
	res := &Result{Entries: make([]Entry, 0, len(a.Paths))}
	for _, p := range a.Paths {
		e := statOne(p, a.FollowSymlinks)
		res.Entries = append(res.Entries, e)
		if e.Error != "" {
			res.Errors++
		}
	}
	res.Count = len(res.Entries)
	return res, nil
}

func statOne(path string, followSymlinks bool) Entry {
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
		if followSymlinks {
			tinfo, terr := os.Stat(path)
			if terr != nil {
				if errors.Is(terr, fs.ErrNotExist) {
					e.Error = "broken_symlink"
				} else {
					e.Error = "stat"
				}
			} else {
				e.Mtime = tinfo.ModTime().UnixNano()
				e.Mode = fmt.Sprintf("%04o", tinfo.Mode().Perm())
				switch {
				case tinfo.IsDir():
					e.Type = "dir"
				default:
					e.Type = "file"
					e.Size = tinfo.Size()
				}
			}
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
//
// Default ("lean") row: `<F|D|L> <size> <path>` (~5 tokens/row). Mode and
// mtime are dropped — the mtime alone tokenizes as ~9 tokens because
// cl100k splits on every digit/colon, so on bulk stat output the time
// column is the dominant cost. `link_target` for symlinks stays in both
// forms.
//
// `--with_meta true` re-adds mode and mtime: `<F|D|L> <size> <mode>
// <mtime> <path>` (~14 tokens/row, the form before this opt-in).
//
// Wire data (Mtime, Mode) is unchanged — only the rendering changes.
func PrettyResponse(req *proto.Request, rsp *proto.Response) string {
	if !rsp.OK {
		return proto.PrettyResponseHeader(rsp)
	}
	var r Result
	if err := proto.UnmarshalData(rsp, &r); err != nil {
		return "ok\n<unrecognized stat result>"
	}
	withMeta := false
	if req != nil {
		if v, ok := req.Args["meta"]; ok {
			if b, ok := argutil.ToBool(v); ok {
				withMeta = b
			}
		}
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
		writeEntry(&b, e, withMeta)
	}
	return strings.TrimRight(b.String(), "\n")
}

func writeEntry(b *strings.Builder, e Entry, withMeta bool) {
	if e.Error != "" {
		fmt.Fprintf(b, "? - %s [%s]\n", e.Path, e.Error)
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
	if withMeta {
		mtime := time.Unix(0, e.Mtime).UTC().Format("2006-01-02T15:04:05Z")
		if e.LinkTarget != "" {
			fmt.Fprintf(b, "%s %-10s %s %s %s -> %s\n", typeChar, sizeStr, e.Mode, mtime, e.Path, e.LinkTarget)
		} else {
			fmt.Fprintf(b, "%s %-10s %s %s %s\n", typeChar, sizeStr, e.Mode, mtime, e.Path)
		}
		return
	}
	if e.LinkTarget != "" {
		fmt.Fprintf(b, "%s %s %s -> %s\n", typeChar, sizeStr, e.Path, e.LinkTarget)
	} else {
		fmt.Fprintf(b, "%s %s %s\n", typeChar, sizeStr, e.Path)
	}
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
		K: []string{"path", "type", "size", "mtime", "mode", "link", "err"},
		R: make([][]any, len(r.Entries)),
	}
	for i, e := range r.Entries {
		cd.R[i] = []any{e.Path, e.Type, e.Size, e.Mtime, e.Mode, e.LinkTarget, e.Error}
	}
	return cd, nil
}
