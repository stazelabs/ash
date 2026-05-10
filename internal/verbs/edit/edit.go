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
// Patch mode (requires patch):
//
//	patch        string (required) — unified diff to apply; pass "-" to read from stdin
//
// Exactly one of old_string, range, or patch must be provided.
// The write is atomic (temp-file + rename on the same filesystem).
// With dry_run=true the replacement is computed but not written; Patch contains
// a unified diff of what would change.
package edit

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/stazelabs/ash/internal/atomicwrite"
	"github.com/stazelabs/ash/internal/diff"
	"github.com/stazelabs/ash/internal/jail"
	"github.com/stazelabs/ash/internal/proto"
	"github.com/stazelabs/ash/internal/verbs/argutil"
)

type Args struct {
	Path       string
	OldString  string
	NewString  string
	Range      string
	NewContent string
	Patch      string // unified diff to apply; "-" means caller resolved stdin already
	ReplaceAll bool
	DryRun     bool
}

type Result struct {
	Path         string `msgpack:"path"`
	BytesWritten int    `msgpack:"bytes_written"`
	LinesTotal   int    `msgpack:"lines_total"`
	Occurrences  int    `msgpack:"occurrences"` // replacements made; hunk count in patch mode
	DryRun       bool   `msgpack:"dry_run,omitempty"`
	Patch        string `msgpack:"patch,omitempty"` // unified diff when dry_run=true
}

func ParseArgs(in map[string]any) (*Args, *proto.Error) {
	a := &Args{}
	var perr *proto.Error

	if a.Path, perr = argutil.RequireString(in, "path"); perr != nil {
		return nil, perr
	}
	if a.OldString, perr = argutil.OptionalString(in, "old", ""); perr != nil {
		return nil, perr
	}
	// --new serves both string mode (new_string) and range mode (new_content).
	newVal, perr2 := argutil.OptionalString(in, "new", "")
	if perr2 != nil {
		return nil, perr2
	}
	a.NewString = newVal
	a.NewContent = newVal
	if a.Range, perr = argutil.OptionalString(in, "range", ""); perr != nil {
		return nil, perr
	}
	if a.Patch, perr = argutil.OptionalString(in, "patch", ""); perr != nil {
		return nil, perr
	}
	if a.ReplaceAll, perr = argutil.OptionalBool(in, "all", false); perr != nil {
		return nil, perr
	}
	if a.DryRun, perr = argutil.OptionalBool(in, "dry", false); perr != nil {
		return nil, perr
	}

	hasOld := a.OldString != ""
	hasRange := a.Range != ""
	hasPatch := a.Patch != ""
	modeCount := 0
	if hasOld {
		modeCount++
	}
	if hasRange {
		modeCount++
	}
	if hasPatch {
		modeCount++
	}
	switch {
	case modeCount > 1:
		return nil, &proto.Error{Code: "args", Msg: "specify exactly one of: old, range, or patch"}
	case modeCount == 0:
		return nil, &proto.Error{Code: "args", Msg: "one of old, range, or patch is required"}
	}
	if perr := jail.CheckPaths(map[string]string{
		"path": a.Path,
	}); perr != nil {
		return nil, perr
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

	switch {
	case a.OldString != "":
		var perr *proto.Error
		newContent, occurrences, perr = applyStringReplace(content, a.OldString, a.NewString, a.ReplaceAll)
		if perr != nil {
			return nil, perr
		}
	case a.Range != "":
		var perr *proto.Error
		newContent, perr = applyLineRange(content, a.Range, a.NewContent)
		if perr != nil {
			return nil, perr
		}
		occurrences = 1
	default: // patch mode
		var perr *proto.Error
		newContent, occurrences, perr = applyPatch(content, a.Patch)
		if perr != nil {
			return nil, perr
		}
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
	writeErr := atomicwrite.Write(a.Path, data, atomicwrite.Options{TmpPrefix: ".ash-edit-", PreserveMode: true})
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

// patchHunk is one hunk parsed from a unified diff.
type patchHunk struct {
	oldStart int
	oldCount int
	newStart int
	newCount int
	lines    []string // body lines including prefix char (' ', '-', '+')
}

// parseHunkHeader extracts (oldStart, oldCount, newStart, newCount) from a
// unified diff hunk header line of the form "@@ -N[,M] +N[,M] @@".
func parseHunkHeader(line string) (oldStart, oldCount, newStart, newCount int, err error) {
	if !strings.HasPrefix(line, "@@ ") {
		return 0, 0, 0, 0, fmt.Errorf("not a hunk header: %q", line)
	}
	rest := line[3:]

	// Parse the old-file range: -start[,count]
	oldPart, rest2, ok := strings.Cut(rest, " ")
	if !ok || !strings.HasPrefix(oldPart, "-") {
		return 0, 0, 0, 0, fmt.Errorf("malformed hunk header (missing -range): %q", line)
	}
	oldPart = oldPart[1:]
	if comma := strings.IndexByte(oldPart, ','); comma >= 0 {
		if oldStart, err = strconv.Atoi(oldPart[:comma]); err != nil {
			return 0, 0, 0, 0, fmt.Errorf("malformed hunk header (old start): %q", line)
		}
		if oldCount, err = strconv.Atoi(oldPart[comma+1:]); err != nil {
			return 0, 0, 0, 0, fmt.Errorf("malformed hunk header (old count): %q", line)
		}
	} else {
		if oldStart, err = strconv.Atoi(oldPart); err != nil {
			return 0, 0, 0, 0, fmt.Errorf("malformed hunk header (old start): %q", line)
		}
		oldCount = 1
	}

	// Parse the new-file range: +start[,count]
	newPart, _, _ := strings.Cut(rest2, " ")
	if !strings.HasPrefix(newPart, "+") {
		return 0, 0, 0, 0, fmt.Errorf("malformed hunk header (missing +range): %q", line)
	}
	newPart = newPart[1:]
	if comma := strings.IndexByte(newPart, ','); comma >= 0 {
		if newStart, err = strconv.Atoi(newPart[:comma]); err != nil {
			return 0, 0, 0, 0, fmt.Errorf("malformed hunk header (new start): %q", line)
		}
		if newCount, err = strconv.Atoi(newPart[comma+1:]); err != nil {
			return 0, 0, 0, 0, fmt.Errorf("malformed hunk header (new count): %q", line)
		}
	} else {
		if newStart, err = strconv.Atoi(newPart); err != nil {
			return 0, 0, 0, 0, fmt.Errorf("malformed hunk header (new start): %q", line)
		}
		newCount = 1
	}
	return
}

// parsePatch parses a unified diff into hunks. Accepts diffs produced by
// ash diff / internal/diff as well as standard GNU unified-diff format.
func parsePatch(patchText string) ([]patchHunk, error) {
	if strings.TrimSpace(patchText) == "" {
		return nil, fmt.Errorf("patch is empty")
	}

	rawLines := strings.Split(patchText, "\n")
	if len(rawLines) > 0 && rawLines[len(rawLines)-1] == "" {
		rawLines = rawLines[:len(rawLines)-1]
	}

	// Skip preamble (--- / +++ header lines and any leading context)
	i := 0
	for i < len(rawLines) && !strings.HasPrefix(rawLines[i], "@@") {
		i++
	}
	if i >= len(rawLines) {
		return nil, fmt.Errorf("no hunks found in patch")
	}

	var hunks []patchHunk
	for i < len(rawLines) {
		line := rawLines[i]
		if !strings.HasPrefix(line, "@@") {
			break
		}
		oldStart, oldCount, newStart, newCount, err := parseHunkHeader(line)
		if err != nil {
			return nil, fmt.Errorf("patch_parse_error: %w", err)
		}
		i++

		var bodyLines []string
		for i < len(rawLines) && !strings.HasPrefix(rawLines[i], "@@") {
			bl := rawLines[i]
			// Skip "\ No newline at end of file" markers from external tools.
			if strings.HasPrefix(bl, `\ `) {
				i++
				continue
			}
			bodyLines = append(bodyLines, bl)
			i++
		}

		hunks = append(hunks, patchHunk{
			oldStart: oldStart, oldCount: oldCount,
			newStart: newStart, newCount: newCount,
			lines:    bodyLines,
		})
	}

	if len(hunks) == 0 {
		return nil, fmt.Errorf("no hunks found in patch")
	}
	return hunks, nil
}

// applyPatch applies a unified diff to content and returns (newContent,
// hunksApplied, error). Error codes: patch_parse_error, patch_failed.
func applyPatch(content, patchText string) (string, int, *proto.Error) {
	hunks, err := parsePatch(patchText)
	if err != nil {
		return "", 0, &proto.Error{Code: "patch_parse_error", Msg: err.Error()}
	}

	// Split file into lines without trailing newlines (mirrors internal/diff.SplitLines).
	var fileLines []string
	if content != "" {
		parts := strings.Split(content, "\n")
		if len(parts) > 0 && parts[len(parts)-1] == "" {
			parts = parts[:len(parts)-1]
		}
		fileLines = parts
	}

	var out []string
	srcPos := 0 // 0-indexed cursor into fileLines

	for _, h := range hunks {
		// hunkStart is 0-indexed; oldStart=0 means insert before first line.
		hunkStart := h.oldStart - 1
		if hunkStart < 0 {
			hunkStart = 0
		}

		// Copy untouched lines up to this hunk's start.
		for srcPos < hunkStart {
			if srcPos >= len(fileLines) {
				return "", 0, &proto.Error{
					Code: "patch_failed",
					Msg:  fmt.Sprintf("hunk at line %d extends beyond file length (%d lines)", h.oldStart, len(fileLines)),
				}
			}
			out = append(out, fileLines[srcPos])
			srcPos++
		}

		// Apply hunk body.
		for _, bl := range h.lines {
			// An empty body line represents a context line with empty content
			// (some tools strip the trailing space from " \n").
			if len(bl) == 0 {
				if srcPos >= len(fileLines) || fileLines[srcPos] != "" {
					got := "<EOF>"
					if srcPos < len(fileLines) {
						got = fmt.Sprintf("%q", fileLines[srcPos])
					}
					return "", 0, &proto.Error{
						Code: "patch_failed",
						Msg:  fmt.Sprintf("hunk mismatch at line %d: expected empty context, got %s", srcPos+1, got),
					}
				}
				out = append(out, "")
				srcPos++
				continue
			}
			prefix := bl[0]
			lineContent := bl[1:]
			switch prefix {
			case ' ': // context — must match
				if srcPos >= len(fileLines) || fileLines[srcPos] != lineContent {
					got := "<EOF>"
					if srcPos < len(fileLines) {
						got = fmt.Sprintf("%q", fileLines[srcPos])
					}
					return "", 0, &proto.Error{
						Code: "patch_failed",
						Msg:  fmt.Sprintf("hunk mismatch at line %d: expected context %q, got %s", srcPos+1, lineContent, got),
					}
				}
				out = append(out, lineContent)
				srcPos++
			case '-': // delete — consume without emitting
				if srcPos >= len(fileLines) || fileLines[srcPos] != lineContent {
					got := "<EOF>"
					if srcPos < len(fileLines) {
						got = fmt.Sprintf("%q", fileLines[srcPos])
					}
					return "", 0, &proto.Error{
						Code: "patch_failed",
						Msg:  fmt.Sprintf("hunk mismatch at line %d: expected to delete %q, got %s", srcPos+1, lineContent, got),
					}
				}
				srcPos++
			case '+': // insert
				out = append(out, lineContent)
			default:
				return "", 0, &proto.Error{
					Code: "patch_parse_error",
					Msg:  fmt.Sprintf("unexpected line prefix %q in hunk body", string(prefix)),
				}
			}
		}
	}

	// Copy any remaining lines after the last hunk.
	out = append(out, fileLines[srcPos:]...)

	if len(out) == 0 {
		return "", len(hunks), nil
	}
	return strings.Join(out, "\n") + "\n", len(hunks), nil
}

func applyStringReplace(content, oldStr, newStr string, replaceAll bool) (string, int, *proto.Error) {
	n := strings.Count(content, oldStr)
	if n == 0 {
		return "", 0, &proto.Error{Code: "match_not_found", Msg: "old_string not found in file"}
	}
	if n > 1 && !replaceAll {
		return "", 0, &proto.Error{
			Code: "ambiguous",
			Msg:  fmt.Sprintf("old_string matched %d times", n),
			Hint: "pass --all true to replace all, or use a more specific old string",
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

// PrettyResponse renders the post-edit acknowledgement. It is intentionally
// chatty (~20 tokens for the success line) where the bash equivalent (`sed
// -i …`) is silent on success: bytes_written + occurrence count are
// load-bearing for the agent's next move, and a follow-up `stat`/`read`
// would cost more tokens than the inlined ack.
func PrettyResponse(req *proto.Request, rsp *proto.Response) string {
	if !rsp.OK {
		return proto.PrettyResponseHeader(rsp)
	}
	var r Result
	if err := proto.UnmarshalData(rsp, &r); err != nil {
		return "ok\n<unrecognized edit result>"
	}

	// Detect mode from request args for descriptive labels.
	patchMode := req != nil && req.Args["patch"] != nil && req.Args["patch"] != ""

	if r.DryRun {
		var detail string
		if patchMode {
			detail = hunkLabel(r.Occurrences)
		} else if req != nil {
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
		header := fmt.Sprintf("=== ash edit --dry_run: %s [%d lines, %s — not written] ===", jail.PrettyPath(r.Path), r.LinesTotal, detail)
		if r.Patch == "" {
			return header + "\n(no changes)"
		}
		return header + "\n" + r.Patch
	}

	var detail string
	if patchMode {
		detail = hunkLabel(r.Occurrences) + " applied"
	} else if req != nil {
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
	return fmt.Sprintf("=== ash edit: %s [%dB, %s] ===", jail.PrettyPath(r.Path), r.BytesWritten, detail)
}

func hunkLabel(n int) string {
	if n == 1 {
		return "1 hunk"
	}
	return fmt.Sprintf("%d hunks", n)
}
