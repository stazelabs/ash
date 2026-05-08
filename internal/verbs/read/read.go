// Package read implements the `read` verb.
//
// Args:
//
//	path        string (required) - relative or absolute path
//	range       string (optional) - "start:end", inclusive on both ends
//	range_kind  string (optional) - "lines" (default) or "bytes"
//	limit_bytes int    (optional) - cap on returned content, default 262144 (256 KiB)
//
// Result fields are documented on the Result struct.
package read

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/stazelabs/ash/internal/proto"
)

const (
	DefaultLimitBytes = 256 << 10 // 256 KiB
	MaxLimitBytes     = 8 << 20   // 8 MiB hard cap
)

type Args struct {
	Path       string
	Range      string
	RangeKind  string
	LimitBytes int
}

type Result struct {
	Path           string `msgpack:"path"`
	Content        string `msgpack:"content"`
	Encoding       string `msgpack:"encoding"` // "utf-8" or "base64"
	Size           int64  `msgpack:"size"`     // size of the file on disk
	Mtime          int64  `msgpack:"mtime"`    // unix nanos
	Lines          int    `msgpack:"lines,omitempty"`
	RangeReturned  string `msgpack:"range_returned,omitempty"`
	Truncated      bool   `msgpack:"truncated,omitempty"`
	TruncationHint string `msgpack:"truncation_hint,omitempty"`
}

// ParseArgs validates and normalizes the loosely-typed args from the wire.
func ParseArgs(in map[string]any) (*Args, *proto.Error) {
	a := &Args{
		LimitBytes: DefaultLimitBytes,
		RangeKind:  "lines",
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
	if v, ok := in["range"]; ok && v != nil {
		s, ok := v.(string)
		if !ok {
			return nil, &proto.Error{Code: "args", Msg: "range must be a string like \"10:50\""}
		}
		a.Range = s
	}
	if v, ok := in["range_kind"]; ok && v != nil {
		s, ok := v.(string)
		if !ok {
			return nil, &proto.Error{Code: "args", Msg: "range_kind must be \"lines\" or \"bytes\""}
		}
		switch s {
		case "lines", "bytes":
			a.RangeKind = s
		default:
			return nil, &proto.Error{Code: "args", Msg: "range_kind must be \"lines\" or \"bytes\""}
		}
	}
	if v, ok := in["limit_bytes"]; ok && v != nil {
		n, ok := toInt(v)
		if !ok || n <= 0 {
			return nil, &proto.Error{Code: "args", Msg: "limit_bytes must be a positive integer"}
		}
		if n > MaxLimitBytes {
			n = MaxLimitBytes
		}
		a.LimitBytes = n
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
	}
	return 0, false
}

// Run executes the read. The path is interpreted as-is (relative paths resolve
// against the daemon's cwd, which is the project root).
func Run(a *Args) (*Result, *proto.Error) {
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
		return nil, &proto.Error{Code: "is_dir", Msg: a.Path + " is a directory; use `find` to list, then `read` a file"}
	}

	body, err := os.ReadFile(a.Path)
	if err != nil {
		return nil, &proto.Error{Code: "read", Msg: err.Error()}
	}

	res := &Result{
		Path:  a.Path,
		Size:  info.Size(),
		Mtime: info.ModTime().UnixNano(),
	}

	slice := body
	if a.Range != "" {
		var rerr *proto.Error
		slice, res.RangeReturned, rerr = applyRange(body, a.Range, a.RangeKind)
		if rerr != nil {
			return nil, rerr
		}
	}

	if len(slice) > a.LimitBytes {
		res.Truncated = true
		res.TruncationHint = fmt.Sprintf(
			"limit_bytes=%d hit at offset %d. narrow with --range or raise --limit_bytes (max %d)",
			a.LimitBytes, len(slice), MaxLimitBytes,
		)
		slice = slice[:a.LimitBytes]
	}

	if utf8.Valid(slice) {
		res.Encoding = "utf-8"
		res.Content = string(slice)
		res.Lines = countLines(slice)
	} else {
		res.Encoding = "base64"
		res.Content = base64.StdEncoding.EncodeToString(slice)
	}
	return res, nil
}

func countLines(b []byte) int {
	if len(b) == 0 {
		return 0
	}
	n := 0
	for _, c := range b {
		if c == '\n' {
			n++
		}
	}
	if b[len(b)-1] != '\n' {
		n++
	}
	return n
}

// applyRange returns the slice of body for the requested range, plus a
// canonical string describing what was actually returned (clamped to bounds).
func applyRange(body []byte, spec, kind string) ([]byte, string, *proto.Error) {
	parts := strings.SplitN(spec, ":", 2)
	if len(parts) != 2 {
		return nil, "", &proto.Error{Code: "args", Msg: "range must be \"start:end\""}
	}
	start, err1 := strconv.Atoi(parts[0])
	end, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return nil, "", &proto.Error{Code: "args", Msg: "range bounds must be integers"}
	}
	if start < 1 || end < start {
		return nil, "", &proto.Error{Code: "args", Msg: "range must satisfy 1 <= start <= end"}
	}
	if kind == "bytes" {
		if start > len(body) {
			return body[:0], fmt.Sprintf("%d:%d", start, start-1), nil
		}
		if end > len(body) {
			end = len(body)
		}
		return body[start-1 : end], fmt.Sprintf("%d:%d", start, end), nil
	}
	// lines
	lineStart := -1
	lineEnd := len(body)
	curLine := 1
	cursor := 0
	if curLine == start {
		lineStart = 0
	}
	for i, c := range body {
		if c == '\n' {
			curLine++
			cursor = i + 1
			if curLine == start {
				lineStart = cursor
			}
			if curLine > end {
				lineEnd = i + 1
				break
			}
		}
	}
	if lineStart < 0 {
		return body[:0], fmt.Sprintf("%d:%d", start, start-1), nil
	}
	return body[lineStart:lineEnd], fmt.Sprintf("%d:%d", start, end), nil
}

// PrettyResponse renders a successful read response in the canonical
// agent-facing form. Used both for client display and for daemon-side token
// counting.
func PrettyResponse(req *proto.Request, rsp *proto.Response) string {
	if !rsp.OK {
		return proto.PrettyResponseHeader(rsp)
	}
	r, ok := decodeResult(rsp.Data)
	if !ok {
		return "ok\n<unrecognized read result>"
	}
	var b strings.Builder
	b.WriteString("=== ")
	b.WriteString(r.Path)
	b.WriteString(" [")
	b.WriteString(strconv.FormatInt(r.Size, 10))
	b.WriteString("B")
	if r.Lines > 0 {
		b.WriteString(", ")
		b.WriteString(strconv.Itoa(r.Lines))
		b.WriteString("L")
	}
	b.WriteString(", ")
	b.WriteString(r.Encoding)
	b.WriteString(", mtime=")
	b.WriteString(time.Unix(0, r.Mtime).UTC().Format("2006-01-02T15:04:05Z"))
	if r.RangeReturned != "" {
		b.WriteString(", range=")
		b.WriteString(r.RangeReturned)
	}
	if r.Truncated {
		b.WriteString(", TRUNCATED")
	}
	b.WriteString("] ===\n")
	b.WriteString(r.Content)
	if r.Truncated {
		b.WriteString("\n\n[truncation: ")
		b.WriteString(r.TruncationHint)
		b.WriteString("]")
	}
	return b.String()
}

// decodeResult pulls a Result out of either the typed daemon-side struct
// pointer or the loosely-decoded msgpack map a client receives over the wire.
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
	if v, ok := m["content"].(string); ok {
		r.Content = v
	}
	if v, ok := m["encoding"].(string); ok {
		r.Encoding = v
	}
	if v, ok := toInt64(m["size"]); ok {
		r.Size = v
	}
	if v, ok := toInt64(m["mtime"]); ok {
		r.Mtime = v
	}
	if v, ok := toInt(m["lines"]); ok {
		r.Lines = v
	}
	if v, ok := m["range_returned"].(string); ok {
		r.RangeReturned = v
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
