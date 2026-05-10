// Package write implements the `write` verb.
//
// Args:
//
//	path         string (required) — file to write; parent dirs created by default
//	content      string (required) — UTF-8 text or base64-encoded bytes
//	encoding     string (optional) — "utf-8" (default) or "base64"
//	mkdir        bool   (optional) — create missing parent directories (default true)
//	create_only  bool   (optional) — fail if the file already exists (default false)
//
// The verb writes content atomically via a temp file + rename on the same
// filesystem so a mid-write crash does not leave a partial file. On
// platforms or paths where the rename cannot be atomic (cross-device), it
// falls back to a direct write.
package write

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/stazelabs/ash/internal/atomicwrite"
	"github.com/stazelabs/ash/internal/jail"
	"github.com/stazelabs/ash/internal/proto"
	"github.com/stazelabs/ash/internal/verbs/argutil"
)

type Args struct {
	Path       string
	Content    string
	Encoding   string
	Mkdir      bool
	CreateOnly bool
}

type Result struct {
	Path         string `msgpack:"path"`
	BytesWritten int    `msgpack:"bytes_written"`
	Created      bool   `msgpack:"created"` // true if file did not exist before the write
}

func ParseArgs(in map[string]any) (*Args, *proto.Error) {
	a := &Args{}
	var perr *proto.Error
	if a.Path, perr = argutil.RequireString(in, "path"); perr != nil {
		return nil, perr
	}
	if a.Content, perr = argutil.OptionalString(in, "content", ""); perr != nil {
		return nil, perr
	}
	if a.Encoding, perr = argutil.OptionalEnum(in, "encoding", "utf-8", []string{"utf-8", "base64"}); perr != nil {
		return nil, perr
	}
	if a.Mkdir, perr = argutil.OptionalBool(in, "mkdir", true); perr != nil {
		return nil, perr
	}
	if a.CreateOnly, perr = argutil.OptionalBool(in, "no-clobber", false); perr != nil {
		return nil, perr
	}
	if perr := jail.CheckPaths(map[string]string{
		"path": a.Path,
	}); perr != nil {
		return nil, perr
	}
	return a, nil
}

// Run writes the file. tr may be nil; tests pass nil to skip phase timing.
func Run(a *Args, tr *proto.Tracer) (*Result, *proto.Error) {
	// Decode content.
	var data []byte
	if a.Encoding == "base64" {
		var err error
		data, err = base64.StdEncoding.DecodeString(a.Content)
		if err != nil {
			return nil, &proto.Error{Code: "args", Msg: "content: invalid base64", Hint: err.Error()}
		}
	} else {
		data = []byte(a.Content)
	}

	// Reject paths that point at an existing directory.
	if info, err := os.Stat(a.Path); err == nil && info.IsDir() {
		return nil, &proto.Error{Code: "is_dir", Msg: a.Path + " is a directory"}
	}

	// create_only: refuse if the file already exists.
	created := true
	if _, err := os.Stat(a.Path); err == nil {
		if a.CreateOnly {
			return nil, &proto.Error{Code: "exists", Msg: a.Path + ": already exists", Hint: "pass --no-clobber false to overwrite"}
		}
		created = false
	}

	// Ensure parent directory exists.
	dir := filepath.Dir(a.Path)
	if a.Mkdir {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			if errors.Is(err, os.ErrPermission) {
				return nil, &proto.Error{Code: "permission", Msg: "mkdir: permission denied", Hint: err.Error()}
			}
			return nil, &proto.Error{Code: "mkdir", Msg: err.Error()}
		}
	} else {
		if _, err := os.Stat(dir); err != nil {
			return nil, &proto.Error{Code: "no_parent", Msg: "parent directory does not exist: " + dir, Hint: "pass --mkdir true to create it"}
		}
	}

	ioStart := time.Now()
	err := atomicwrite.Write(a.Path, data, atomicwrite.Options{TmpPrefix: ".ash-write-"})
	tr.AddIO(time.Since(ioStart))
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			return nil, &proto.Error{Code: "permission", Msg: err.Error()}
		}
		return nil, &proto.Error{Code: "write", Msg: err.Error()}
	}

	return &Result{
		Path:         a.Path,
		BytesWritten: len(data),
		Created:      created,
	}, nil
}

func PrettyResponse(req *proto.Request, rsp *proto.Response) string {
	if !rsp.OK {
		return proto.PrettyResponseHeader(rsp)
	}
	var r Result
	if err := proto.UnmarshalData(rsp, &r); err != nil {
		return "ok\n<unrecognized write result>"
	}
	verb := "overwritten"
	if r.Created {
		verb = "created"
	}
	return fmt.Sprintf("=== ash write: %s [%dB, %s] ===", jail.PrettyPath(r.Path), r.BytesWritten, verb)
}
