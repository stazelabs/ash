// Package recap implements the `recap` verb (ASH-110).
//
// recap is a compact session summary: which files the agent has touched,
// which patterns it has searched for, and which writes/edits it has made,
// over a configurable time window. The intent is "where was I?" in one
// cheap call rather than scrolling the ledger raw.
//
// Args:
//
//	since string  (optional) - duration window, e.g. "15m", "1h", "24h", default "1h"
//	top   int     (optional) - cap on entries per section, default 10, max 100
//
// Result is shaped to render in roughly 200-500 tokens regardless of how
// busy the session has been, hence the per-section caps. The verb reads
// the daemon's own ledger; it has no notion of foreign roots.
package recap

import (
	"bytes"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/stazelabs/ash/internal/jail"
	"github.com/stazelabs/ash/internal/ledger"
	"github.com/stazelabs/ash/internal/proto"
	"github.com/stazelabs/ash/internal/verbs/argutil"
	"github.com/vmihailenco/msgpack/v5"
)

const (
	defaultSince = time.Hour
	maxSince     = 7 * 24 * time.Hour
	DefaultTop   = 10
	MaxTop       = 100
)

type Args struct {
	Since time.Duration
	Top   int
}

// FileTouch is one file's read/write/edit/diff aggregate over the window.
type FileTouch struct {
	Path     string `msgpack:"path"`
	Reads    int    `msgpack:"reads,omitempty"`
	Edits    int    `msgpack:"edits,omitempty"` // write+edit calls (any mutation)
	Searches int    `msgpack:"searches,omitempty"` // grep/find calls whose --path matched
}

// Pattern is one grep pattern with its call frequency.
type Pattern struct {
	Pattern string `msgpack:"pattern"`
	Calls   int    `msgpack:"calls"`
}

// Edit is one mutating call (write/edit) with its target path.
type Edit struct {
	Verb string `msgpack:"verb"`
	Path string `msgpack:"path"`
	Ts   int64  `msgpack:"ts"`
}

type Scope struct {
	Since string `msgpack:"since"`
	Top   int    `msgpack:"top"`
}

type Totals struct {
	Calls  int `msgpack:"calls"`
	OK     int `msgpack:"ok"`
	Errors int `msgpack:"errors"`
}

type Result struct {
	Scope    Scope       `msgpack:"scope"`
	Totals   Totals      `msgpack:"totals"`
	Files    []FileTouch `msgpack:"files,omitempty"`
	Patterns []Pattern   `msgpack:"patterns,omitempty"`
	Edits    []Edit      `msgpack:"edits,omitempty"`
}

func ParseArgs(in map[string]any) (*Args, *proto.Error) {
	a := &Args{Since: defaultSince, Top: DefaultTop}
	if since, perr := argutil.OptionalString(in, "since", ""); perr != nil {
		return nil, perr
	} else if since != "" {
		d, err := parseDuration(since)
		if err != nil {
			return nil, &proto.Error{Code: "args", Msg: "since: " + err.Error()}
		}
		if d > maxSince {
			d = maxSince
		}
		a.Since = d
	}
	var perr *proto.Error
	if a.Top, perr = argutil.OptionalPosInt(in, "top", DefaultTop, MaxTop); perr != nil {
		return nil, perr
	}
	return a, nil
}

// RunWithLedger queries the current daemon session and aggregates a
// recap over the configured window.
func RunWithLedger(led *ledger.Ledger, a *Args) (*Result, *proto.Error) {
	calls, err := led.QueryWindow(ledger.QueryOpts{
		SessionID: "current",
		Since:     time.Now().Add(-a.Since),
	})
	if err != nil {
		return nil, &proto.Error{Code: "ledger", Msg: err.Error()}
	}
	r := aggregate(calls, a.Top)
	r.Scope = Scope{Since: a.Since.String(), Top: a.Top}
	return r, nil
}

func aggregate(calls []ledger.Call, top int) *Result {
	totals := Totals{Calls: len(calls)}
	files := map[string]*FileTouch{}
	patterns := map[string]int{}
	var edits []Edit
	for _, c := range calls {
		if c.OK {
			totals.OK++
		} else {
			totals.Errors++
		}
		args := decodeArgs(c.ArgsMsgpack)
		if len(args) == 0 {
			continue
		}
		path := stringArg(args, "path")
		switch c.Verb {
		case "read":
			if path != "" {
				touch(files, path).Reads++
			}
		case "write", "edit":
			if path != "" {
				touch(files, path).Edits++
				edits = append(edits, Edit{Verb: c.Verb, Path: prettyPath(path), Ts: c.Timestamp.UnixNano()})
			}
		case "diff":
			if path != "" {
				touch(files, path).Reads++
			}
			if other := stringArg(args, "other"); other != "" {
				touch(files, other).Reads++
			}
		case "grep":
			if path != "" {
				touch(files, path).Searches++
			}
			if pat := stringArg(args, "pattern"); pat != "" {
				patterns[pat]++
			}
		case "find":
			if path != "" {
				touch(files, path).Searches++
			}
		case "stat":
			for _, p := range splitPaths(stringArg(args, "paths"), stringArg(args, "path")) {
				touch(files, p).Reads++
			}
		}
	}
	r := &Result{Totals: totals}
	r.Files = topFiles(files, top)
	r.Patterns = topPatterns(patterns, top)
	r.Edits = topEdits(edits, top)
	return r
}

func touch(m map[string]*FileTouch, path string) *FileTouch {
	pp := prettyPath(path)
	if t, ok := m[pp]; ok {
		return t
	}
	t := &FileTouch{Path: pp}
	m[pp] = t
	return t
}

func topFiles(m map[string]*FileTouch, n int) []FileTouch {
	if len(m) == 0 {
		return nil
	}
	out := make([]FileTouch, 0, len(m))
	for _, v := range m {
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool {
		ai := out[i].Reads + out[i].Edits + out[i].Searches
		aj := out[j].Reads + out[j].Edits + out[j].Searches
		if ai != aj {
			return ai > aj
		}
		return out[i].Path < out[j].Path
	})
	if n > 0 && len(out) > n {
		out = out[:n]
	}
	return out
}

func topPatterns(m map[string]int, n int) []Pattern {
	if len(m) == 0 {
		return nil
	}
	out := make([]Pattern, 0, len(m))
	for p, c := range m {
		out = append(out, Pattern{Pattern: p, Calls: c})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Calls != out[j].Calls {
			return out[i].Calls > out[j].Calls
		}
		return out[i].Pattern < out[j].Pattern
	})
	if n > 0 && len(out) > n {
		out = out[:n]
	}
	return out
}

func topEdits(edits []Edit, n int) []Edit {
	if len(edits) == 0 {
		return nil
	}
	if n > 0 && len(edits) > n {
		edits = edits[:n]
	}
	return edits
}

func PrettyResponse(req *proto.Request, rsp *proto.Response) string {
	if !rsp.OK {
		return proto.PrettyResponseHeader(rsp)
	}
	var r Result
	if err := proto.UnmarshalData(rsp, &r); err != nil {
		return "ok\n<unrecognized recap result>"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "§recap: %d calls, since=%s (ok=%d err=%d)\n",
		r.Totals.Calls, r.Scope.Since, r.Totals.OK, r.Totals.Errors)
	if len(r.Files) == 0 && len(r.Patterns) == 0 && len(r.Edits) == 0 {
		b.WriteString("(no activity)")
		return strings.TrimRight(b.String(), "\n")
	}
	if len(r.Files) > 0 {
		fmt.Fprintf(&b, "files (%d):\n", len(r.Files))
		for _, f := range r.Files {
			fmt.Fprintf(&b, "  %s  R×%d W×%d S×%d\n", f.Path, f.Reads, f.Edits, f.Searches)
		}
	}
	if len(r.Patterns) > 0 {
		fmt.Fprintf(&b, "patterns (%d):\n", len(r.Patterns))
		for _, p := range r.Patterns {
			fmt.Fprintf(&b, "  %s  ×%d\n", p.Pattern, p.Calls)
		}
	}
	if len(r.Edits) > 0 {
		fmt.Fprintf(&b, "edits (%d):\n", len(r.Edits))
		for _, e := range r.Edits {
			fmt.Fprintf(&b, "  %s %s\n", e.Verb, e.Path)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// -- helpers --

func decodeArgs(blob []byte) map[string]any {
	if len(blob) == 0 {
		return nil
	}
	dec := msgpack.NewDecoder(bytes.NewReader(blob))
	dec.UseLooseInterfaceDecoding(true)
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil
	}
	return m
}

func stringArg(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return s
}

func splitPaths(paths, path string) []string {
	var out []string
	for _, src := range []string{paths, path} {
		if src == "" {
			continue
		}
		for _, p := range strings.Split(src, ",") {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			out = append(out, p)
		}
	}
	return out
}

func prettyPath(p string) string {
	pp := jail.PrettyPath(p)
	if pp == "" {
		return "."
	}
	return pp
}

// parseDuration extends time.ParseDuration to support day units.
func parseDuration(s string) (time.Duration, error) {
	if strings.HasSuffix(s, "d") {
		var n int
		_, err := fmt.Sscanf(s[:len(s)-1], "%d", &n)
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("invalid day value %q", s)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	return time.ParseDuration(s)
}
