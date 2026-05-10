// Package diff implements the `diff` verb.
//
// Args:
//
//	path         string (required) — file to use as the "before" side
//	other        string — second file to use as the "after" side
//	content      string — inline "after" text (alternative to --other)
//	context      int    (optional, default 3) — context lines per hunk
//
// Exactly one of other or content must be provided.
// Returns a unified diff, additions count, and deletions count.
// Both inputs are capped at diff.MaxLines (2000) lines.
package diff

import (
	"errors"
	"fmt"
	"os"
	"time"

	idiff "github.com/stazelabs/ash/internal/diff"
	"github.com/stazelabs/ash/internal/jail"
	"github.com/stazelabs/ash/internal/proto"
	"github.com/stazelabs/ash/internal/verbs/argutil"
)

type Args struct {
	Path    string
	Other   string
	Content string
	Context int
	Stat    bool
}

type Result struct {
	PathA      string `msgpack:"path_a"`
	PathB      string `msgpack:"path_b"`
	Additions  int    `msgpack:"additions"`
	Deletions  int    `msgpack:"deletions"`
	Patch      string `msgpack:"patch"`
	Unchanged  bool   `msgpack:"unchanged,omitempty"` // true when the two sides are identical
}

func ParseArgs(in map[string]any) (*Args, *proto.Error) {
	a := &Args{}
	var perr *proto.Error

	if a.Path, perr = argutil.RequireString(in, "path"); perr != nil {
		return nil, perr
	}
	if a.Other, perr = argutil.OptionalString(in, "other", ""); perr != nil {
		return nil, perr
	}
	if a.Content, perr = argutil.OptionalString(in, "content", ""); perr != nil {
		return nil, perr
	}
	if a.Context, perr = argutil.OptionalPosInt(in, "context", idiff.DefaultContext, 50); perr != nil {
		return nil, perr
	}
	if a.Stat, perr = argutil.OptionalBool(in, "stat", false); perr != nil {
		return nil, perr
	}

	hasOther := a.Other != ""
	hasContent := in["content"] != nil // distinguish "not provided" from empty string
	switch {
	case hasOther && hasContent:
		return nil, &proto.Error{Code: "args", Msg: "specify either other or content, not both"}
	case !hasOther && !hasContent:
		return nil, &proto.Error{Code: "args", Msg: "one of other or content is required"}
	}
	if perr := jail.CheckPaths(map[string]string{
		"path": a.Path,
		"other": a.Other,
	}); perr != nil {
		return nil, perr
	}
	return a, nil
}

// Run executes the diff. tr may be nil.
func Run(a *Args, tr *proto.Tracer) (*Result, *proto.Error) {
	// Read the "before" file.
	t0 := time.Now()
	rawA, err := os.ReadFile(a.Path)
	tr.AddIO(time.Since(t0))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, &proto.Error{Code: "not_found", Msg: a.Path + ": no such file"}
		}
		if errors.Is(err, os.ErrPermission) {
			return nil, &proto.Error{Code: "permission", Msg: err.Error()}
		}
		return nil, &proto.Error{Code: "read", Msg: err.Error()}
	}
	contentA := string(rawA)

	// Determine the "after" side.
	var contentB, pathB string
	if a.Other != "" {
		t1 := time.Now()
		rawB, err := os.ReadFile(a.Other)
		tr.AddIO(time.Since(t1))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil, &proto.Error{Code: "not_found", Msg: a.Other + ": no such file"}
			}
			if errors.Is(err, os.ErrPermission) {
				return nil, &proto.Error{Code: "permission", Msg: err.Error()}
			}
			return nil, &proto.Error{Code: "read", Msg: err.Error()}
		}
		contentB = string(rawB)
		pathB = a.Other
	} else {
		contentB = a.Content
		pathB = a.Path + " (new)"
	}

	linesA := idiff.SplitLines(contentA)
	linesB := idiff.SplitLines(contentB)

	edits, err := idiff.Lines(linesA, linesB)
	if err != nil {
		return nil, &proto.Error{Code: "too_large", Msg: err.Error()}
	}

	add, del := idiff.Stats(edits)
	if a.Stat {
		return &Result{
			PathA:     a.Path,
			PathB:     pathB,
			Additions: add,
			Deletions: del,
			Unchanged: add == 0 && del == 0,
		}, nil
	}
	patch := idiff.Unified(edits, a.Path, pathB, a.Context)
	return &Result{
		PathA:     a.Path,
		PathB:     pathB,
		Additions: add,
		Deletions: del,
		Patch:     patch,
		Unchanged: patch == "",
	}, nil
}

func PrettyResponse(req *proto.Request, rsp *proto.Response) string {
	if !rsp.OK {
		return proto.PrettyResponseHeader(rsp)
	}
	var r Result
	if err := proto.UnmarshalData(rsp, &r); err != nil {
		return "ok\n<unrecognized diff result>"
	}
	// ASH-71: header echoes the file paths in repo-relative form when
	// they sit under the project root.
	a, c := jail.PrettyPath(r.PathA), jail.PrettyPath(r.PathB)
	if r.Unchanged {
		return fmt.Sprintf("=== ash diff: %s vs %s [identical] ===", a, c)
	}
	if r.Patch == "" {
		return fmt.Sprintf("=== ash diff: %s vs %s [+%d -%d] ===", a, c, r.Additions, r.Deletions)
	}
	header := fmt.Sprintf("=== ash diff: %s vs %s [+%d -%d] ===\n", a, c, r.Additions, r.Deletions)
	return header + r.Patch
}
