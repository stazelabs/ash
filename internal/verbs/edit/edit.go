// Package edit implements the `edit` verb.
//
// Args:
//
//	path         string (required) — file to edit
//
// String-replacement mode (requires old_string):
//
//	old_string   string (required) — exact text to find; must be non-empty
//	new_string   string (optional, default "") — replacement; empty = deletion
//	replace_all  bool   (optional, default false) — replace all occurrences;
//	                    if false, errors when old_string appears more than once
//
// Line-range mode (requires range):
//
//	range        string (required) — "start:end" line range, 1-based inclusive
//	new_content  string (optional, default "") — replacement text; empty = deletion
//
// Exactly one of old_string or range must be provided.
// The write is atomic (temp-file + rename on the same filesystem).
// With dry_run=true the replacement is computed but not written; Patch contains
// a unified diff of what would change.
package edit

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/stazelabs/ash/internal/diff"
	"github.com/stazelabs/ash/internal/proto"
	"github.com/stazelabs/ash/internal/verbs/argutil"
)

type Args struct {
	Path       string
	OldString  string
	NewString  string
	Range      string
	NewContent string
	ReplaceAll bool
	DryRun     bool
}

type Result struct {
	Path         string `msgpack:"path"`
	BytesWritten int    `msgpack:"bytes_written"`
	LinesTotal   int    `msgpack:"lines_total"`
	Occurrences  int    `msgpack:"occurrences"` // replacements made; always 1 for range mode
	DryRun       bool   `msgpack:"dry_run,omitempty"`
	Patch        string `msgpack:"patch,omitempty"` // unified diff when dry_run=true
}

func ParseArgs(in map[string]any) (*Args, *proto.Error) {
	a := &Args{}
	var perr *proto.Error

	if a.Path, perr = argutil.RequireString(in, "path"); perr != nil {
		return nil, perr
	}
	if a.OldString, perr = argutil.OptionalString(in, "old_string", ""); perr != nil {
		return nil, perr
	}
	if a.NewString, perr = argutil.OptionalString(in, "new_string", ""); perr != nil {
		return nil, perr
	}
	if a.Range, perr = argutil.OptionalString(in, "range", ""); perr != nil {
		return nil, perr
	}
	if a.NewContent, perr = argutil.OptionalString(in, "new_content", ""); perr != nil {
		return nil, perr
	}
	if a.ReplaceAll, perr = argutil.OptionalBool(in, "replace_all", false); perr != nil {
		return nil, perr
	}
	if a.DryRun, perr = argutil.OptionalBool(in, "dry_run", false); perr != nil {
		return nil, perr
	}

	hasOld := a.OldString != ""
	hasRange := a.Range != ""
	switch {
	case hasOld && hasRange:
		return nil, &proto.Error{Code: "args", Msg: "specify either old_string or range, not both"}
	case !hasOld && !hasRange:
		return nil, &proto.Error{Code: "args", Msg: "one of old_string or range is required"}
	}
	return a, nil
}

// Run executes the edit. tr may be nil.
func Run(a *Args, tr *proto.Tracer) (*Result, *proto.Error) {
	info, err := os.Stat(a.Path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, &proto.Error{Code: "not_found", Msg: a.Path + ": no such file"}
		}
		if errors.Is(err, os.ErrPermission) {
			return nil, &proto.Error{Code: "permission", Msg: err.Error()}
		}
		return nil, &proto.Error{Code: "stat", Msg: err.Error()}
	}
	if info.IsDir() {
		return nil, &proto.Error{Code: "is_dir", Msg: a.Path + " is a directory"}
	}

	ioStart := time.Now()
	raw, err := os.ReadFile(a.Path)
	tr.AddIO(time.Since(ioStart))
	if err != nil {
		return nil, &proto.Error{Code: "read", Msg: err.Error()}
	}

	content := string(raw)
	var newContent string
	var occurrences int

	if a.OldString != "" {
		var perr *proto.Error
		newContent, occurrences, perr = applyStringReplace(content, a.OldString, a.NewString, a.ReplaceAll)
		if perr != nil {
			return nil, perr
		}
	} else {
		var perr *proto.Error
		newContent, perr = applyLineRange(content, a.Range, a.NewContent)
		if perr != nil {
			return nil, perr
		}
		occurrences = 1
	}

	if a.DryRun {
		patch := computePatch(content, newContent, a.Path)
		return &Result{
			Path:        a.Path,
			LinesTotal:  countLines(newContent),
			Occurrences: occurrences,
			DryRun:      true,
			Patch:       patch,
		}, nil
	}

	data := []byte(newContent)
	ioStart = time.Now()
	writeErr := writeAtomic(a.Path, data)
	tr.AddIO(time.Since(ioStart))
	if writeErr != nil {
		if errors.Is(writeErr, os.ErrPermission) {
			return nil, &proto.Error{Code: "permission", Msg: writeErr.Error()}
		}
		return nil, &proto.Error{Code: "write", Msg: writeErr.Error()}
	}

	return &Result{
		Path:         a.Path,
		BytesWritten: len(data),
		LinesTotal:   countLines(newContent),
		Occurrences:  occurrences,
	}, nil
}

// computePatch generates a unified diff from old to new content.
func computePatch(oldContent, newContent, path string) string {
	a := diff.SplitLines(oldContent)
	b := diff.SplitLines(newContent)
	edits, err := diff.Lines(a, b)
	if err != nil {
		return fmt.Sprintf("(diff unavailable: %v)", err)
	}
	return diff.Unified(edits, path, path, diff.DefaultContext)
}

func applyStringReplace(content, oldStr, newStr string, replaceAll bool) (string, int, *proto.Error) {
	n := strings.Count(content, oldStr)
	if n == 0 {
		return "", 0, &proto.Error{Code: "match_not_found", Msg: "old_string not found in file"}
	}
	if n > 1 && !replaceAll {
		return "", 0, &proto.Error{
			Code: "ambiguous",
			Msg:  fmt.Sprintf("old_string found %d times; pass --replace_all true to replace all, or use a more specific string", n),
		}
	}
	if replaceAll {
		return strings.ReplaceAll(content, oldStr, newStr), n, nil
	}
	return strings.Replace(content, oldStr, newStr, 1), 1, nil
}

func applyLineRange(content, spec, newContent string) (string, *proto.Error) {
	parts := strings.SplitN(spec, ":", 2)
	if len(parts) != 2 {
		return "", &proto.Error{Code: "args", Msg: `range must be "start:end"`}
	}
	start, err1 := strconv.Atoi(parts[0])
	end, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return "", &proto.Error{Code: "args", Msg: "range bounds must be integers"}
	}
	if start < 1 || end < start {
		return "", &proto.Error{Code: "args", Msg: "range must satisfy 1 <= start <= end"}
	}

	offsets := lineStartOffsets(content)
	nLines := len(offsets)
	if nLines == 0 {
		return "", &proto.Error{Code: "range_out_of_bounds", Msg: "file is empty"}
	}
	if start > nLines {
		return "", &proto.Error{
			Code: "range_out_of_bounds",
			Msg:  fmt.Sprintf("start=%d exceeds file length (%d lines)", start, nLines),
		}
	}
	if end > nLines {
		end = nLines
	}

	startByte := offsets[start-1]
	var endByte int
	if end < nLines {
		endByte = offsets[end]
	} else {
		endByte = len(content)
	}

	var b strings.Builder
	b.WriteString(content[:startByte])
	if newContent != "" {
		b.WriteString(newContent)
		if newContent[len(newContent)-1] != '\n' {
			b.WriteByte('\n')
		}
	}
	b.WriteString(content[endByte:])
	return b.String(), nil
}

// lineStartOffsets returns the byte offset where each line begins (1-based
// line N starts at offset lineStartOffsets[N-1]). An empty string returns nil.
func lineStartOffsets(s string) []int {
	if s == "" {
		return nil
	}
	offsets := []int{0}
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' && i+1 < len(s) {
			offsets = append(offsets, i+1)
		}
	}
	return offsets
}

func countLines(s string) int {
	if s == "" {
		return 0
	}
	n := strings.Count(s, "\n")
	if s[len(s)-1] != '\n' {
		n++
	}
	return n
}

func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".ash-edit-*")
	if err != nil {
		return os.WriteFile(path, data, 0o644)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	// Preserve permissions of the original file.
	if info, err := os.Stat(path); err == nil {
		os.Chmod(tmpName, info.Mode())
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return os.WriteFile(path, data, 0o644)
	}
	return nil
}

func PrettyResponse(req *proto.Request, rsp *proto.Response) string {
	if !rsp.OK {
		return proto.PrettyResponseHeader(rsp)
	}
	r, ok := decodeResult(rsp.Data)
	if !ok {
		return "ok\n<unrecognized edit result>"
	}

	if r.DryRun {
		var detail string
		if req != nil {
			if rangeVal, _ := req.Args["range"].(string); rangeVal != "" {
				detail = fmt.Sprintf("lines %s", rangeVal)
			}
		}
		if detail == "" {
			if r.Occurrences == 1 {
				detail = "1 replacement"
			} else {
				detail = fmt.Sprintf("%d replacements", r.Occurrences)
			}
		}
		header := fmt.Sprintf("=== ash edit --dry_run: %s [%d lines, %s — not written] ===", r.Path, r.LinesTotal, detail)
		if r.Patch == "" {
			return header + "\n(no changes)"
		}
		return header + "\n" + r.Patch
	}

	var detail string
	if req != nil {
		if rangeVal, _ := req.Args["range"].(string); rangeVal != "" {
			detail = fmt.Sprintf("lines %s replaced", rangeVal)
		}
	}
	if detail == "" {
		if r.Occurrences == 1 {
			detail = "1 replacement"
		} else {
			detail = fmt.Sprintf("%d replacements", r.Occurrences)
		}
	}
	return fmt.Sprintf("=== ash edit: %s [%dB, %s] ===", r.Path, r.BytesWritten, detail)
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
	if v, ok := m["path"].(string); ok {
		r.Path = v
	}
	if v, ok := argutil.ToInt(m["bytes_written"]); ok {
		r.BytesWritten = v
	}
	if v, ok := argutil.ToInt(m["lines_total"]); ok {
		r.LinesTotal = v
	}
	if v, ok := argutil.ToInt(m["occurrences"]); ok {
		r.Occurrences = v
	}
	if v, ok := m["dry_run"].(bool); ok {
		r.DryRun = v
	}
	if v, ok := m["patch"].(string); ok {
		r.Patch = v
	}
	return r, true
}
