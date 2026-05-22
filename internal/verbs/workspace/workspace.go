// Package workspace implements the `workspace` verb (ASH-110).
//
// workspace is the post-compaction re-orientation tool: one cheap call
// returns where the agent was — recently-touched files, recent searches,
// branch + clean/dirty status, last error. The intent is for an agent
// to call `ash workspace` once after context compaction and resume
// without paying the cost of re-reading files manually.
//
// Args:
//
//	since  string  (optional) - duration window, default "30m"
//	recent int     (optional) - cap on file/search lists, default 10, max 50
//
// Result is sized to render in a few hundred tokens; the per-section
// caps make the worst case (a very busy session) bounded.
package workspace

import (
	"bytes"
	"fmt"
	"strings"
	"time"

	"github.com/stazelabs/ash/internal/jail"
	"github.com/stazelabs/ash/internal/ledger"
	"github.com/stazelabs/ash/internal/proto"
	"github.com/stazelabs/ash/internal/verbs/argutil"
	"github.com/stazelabs/ash/internal/verbs/git"
	"github.com/vmihailenco/msgpack/v5"
)

const (
	defaultSince  = 30 * time.Minute
	maxSince      = 24 * time.Hour
	DefaultRecent = 10
	MaxRecent     = 50
)

type Args struct {
	Since  time.Duration
	Recent int
}

type FileEntry struct {
	Path string `msgpack:"path"`
	// LastVerb is the most recent verb that touched the file (read,
	// edit, write, grep, etc.); useful for the agent to decide what
	// it was doing.
	LastVerb string `msgpack:"last_verb"`
	// LastTs is the most recent touch time (UnixNano).
	LastTs int64 `msgpack:"last_ts"`
}

type SearchEntry struct {
	Pattern string `msgpack:"pattern"`
	Path    string `msgpack:"path,omitempty"`
	// Hits is the number of grep matches reported by the call (-1 if
	// unknown — we read it from the call's tokens_out histogram).
	Hits int `msgpack:"hits,omitempty"`
}

type GitStatus struct {
	Branch    string `msgpack:"branch,omitempty"`
	Head      string `msgpack:"head,omitempty"`
	Clean     bool   `msgpack:"clean"`
	Staged    int    `msgpack:"staged,omitempty"`
	Unstaged  int    `msgpack:"unstaged,omitempty"`
	Untracked int    `msgpack:"untracked,omitempty"`
	// Available is false when no git repo was detected; the rest of
	// the struct is then meaningless and PrettyResponse skips the line.
	Available bool   `msgpack:"available"`
	Error     string `msgpack:"error,omitempty"`
}

type LastError struct {
	Verb string `msgpack:"verb"`
	Code string `msgpack:"code"`
	Msg  string `msgpack:"msg,omitempty"`
	Ts   int64  `msgpack:"ts"`
}

type Result struct {
	Since    string        `msgpack:"since"`
	Calls    int           `msgpack:"calls"`
	Git      GitStatus     `msgpack:"git"`
	Files    []FileEntry   `msgpack:"files,omitempty"`
	Searches []SearchEntry `msgpack:"searches,omitempty"`
	LastErr  *LastError    `msgpack:"last_err,omitempty"`
}

func ParseArgs(in map[string]any) (*Args, *proto.Error) {
	a := &Args{Since: defaultSince, Recent: DefaultRecent}
	if since, perr := argutil.OptionalString(in, "since", ""); perr != nil {
		return nil, perr
	} else if since != "" {
		d, err := argutil.ParseDuration(since)
		if err != nil {
			return nil, &proto.Error{Code: "args", Msg: "since: " + err.Error()}
		}
		if d > maxSince {
			d = maxSince
		}
		a.Since = d
	}
	var perr *proto.Error
	if a.Recent, perr = argutil.OptionalPosInt(in, "recent", DefaultRecent, MaxRecent); perr != nil {
		return nil, perr
	}
	return a, nil
}

// RunWithLedger queries the current daemon session and assembles a
// re-orientation snapshot. The git-status piece is best-effort: a
// non-git repo, or a git failure, leaves Git.Available=false but does
// not fail the whole call — the rest of the snapshot is still useful.
func RunWithLedger(led *ledger.Ledger, a *Args) (*Result, *proto.Error) {
	calls, err := led.QueryWindow(ledger.QueryOpts{
		SessionID: "current",
		Since:     time.Now().Add(-a.Since),
	})
	if err != nil {
		return nil, &proto.Error{Code: "ledger", Msg: err.Error()}
	}
	r := &Result{Since: a.Since.String(), Calls: len(calls)}
	r.Files = relevantFiles(calls, a.Recent)
	r.Searches = recentSearches(calls, a.Recent)
	r.LastErr = mostRecentError(calls)
	r.Git = gitSnapshot()
	return r, nil
}

// relevantFiles returns the most-recently-touched files in the window,
// in reverse chronological order. Each file appears once; the first
// (most recent) call wins for LastVerb / LastTs.
func relevantFiles(calls []ledger.Call, n int) []FileEntry {
	seen := map[string]int{} // path -> entries[idx]
	var entries []FileEntry
	for _, c := range calls {
		args := decodeArgs(c.ArgsMsgpack)
		if len(args) == 0 {
			continue
		}
		var paths []string
		switch c.Verb {
		case "read", "write", "edit", "diff":
			if p := stringArg(args, "path"); p != "" {
				paths = append(paths, p)
			}
			if c.Verb == "diff" {
				if p := stringArg(args, "other"); p != "" {
					paths = append(paths, p)
				}
			}
		case "stat":
			paths = splitPaths(stringArg(args, "paths"), stringArg(args, "path"))
		}
		if len(paths) == 0 {
			continue
		}
		for _, raw := range paths {
			pp := prettyPath(raw)
			if _, ok := seen[pp]; ok {
				continue
			}
			seen[pp] = len(entries)
			entries = append(entries, FileEntry{
				Path:     pp,
				LastVerb: c.Verb,
				LastTs:   c.Timestamp.UnixNano(),
			})
			if n > 0 && len(entries) >= n {
				return entries
			}
		}
	}
	return entries
}

// recentSearches returns recent grep/find calls in reverse chronological
// order, deduplicated by (pattern, path).
func recentSearches(calls []ledger.Call, n int) []SearchEntry {
	type key struct{ pat, path string }
	seen := map[key]bool{}
	var out []SearchEntry
	for _, c := range calls {
		if c.Verb != "grep" && c.Verb != "find" {
			continue
		}
		args := decodeArgs(c.ArgsMsgpack)
		if len(args) == 0 {
			continue
		}
		path := prettyPath(stringArg(args, "path"))
		pattern := stringArg(args, "pattern")
		if c.Verb == "find" {
			pattern = stringArg(args, "glob")
			if pattern == "" {
				pattern = "**"
			}
		}
		if pattern == "" {
			continue
		}
		k := key{pattern, path}
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, SearchEntry{Pattern: pattern, Path: path})
		if n > 0 && len(out) >= n {
			return out
		}
	}
	return out
}

func mostRecentError(calls []ledger.Call) *LastError {
	for _, c := range calls {
		if c.OK {
			continue
		}
		return &LastError{
			Verb: c.Verb,
			Code: c.ErrCode,
			Msg:  truncate(c.ErrMsg, 200),
			Ts:   c.Timestamp.UnixNano(),
		}
	}
	return nil
}

// gitSnapshot calls git.Run with op=status against the current
// directory. Best-effort: any failure (not a repo, git binary absent)
// returns Available=false and the error string for diagnostic display.
func gitSnapshot() GitStatus {
	gargs := &git.Args{
		Op:        "status",
		Path:      ".",
		Untracked: true,
	}
	res, perr := git.Run(gargs, &proto.Tracer{})
	if perr != nil {
		return GitStatus{Available: false, Error: perr.Code + ": " + perr.Msg}
	}
	if res == nil || res.Status == nil {
		return GitStatus{Available: false}
	}
	s := res.Status
	gs := GitStatus{
		Available: true,
		Branch:    s.Branch,
		Head:      s.Head,
		Clean:     s.Clean,
		Staged:    len(s.Staged),
		Unstaged:  len(s.Unstaged),
		Untracked: len(s.Untracked),
	}
	return gs
}

func PrettyResponse(req *proto.Request, rsp *proto.Response) string {
	if !rsp.OK {
		return proto.PrettyResponseHeader(rsp)
	}
	var r Result
	if err := proto.UnmarshalData(rsp, &r); err != nil {
		return "ok\n<unrecognized workspace result>"
	}
	var b strings.Builder
	header := fmt.Sprintf("§workspace: %d calls, since=%s", r.Calls, r.Since)
	if r.Git.Available {
		state := "clean"
		if !r.Git.Clean {
			parts := []string{}
			if r.Git.Staged > 0 {
				parts = append(parts, fmt.Sprintf("%d staged", r.Git.Staged))
			}
			if r.Git.Unstaged > 0 {
				parts = append(parts, fmt.Sprintf("%d unstaged", r.Git.Unstaged))
			}
			if r.Git.Untracked > 0 {
				parts = append(parts, fmt.Sprintf("%d untracked", r.Git.Untracked))
			}
			if len(parts) > 0 {
				state = strings.Join(parts, ", ")
			} else {
				state = "dirty"
			}
		}
		branch := r.Git.Head
		if branch == "" {
			branch = r.Git.Branch
		}
		if branch == "" {
			branch = "(detached)"
		}
		header += fmt.Sprintf(" — branch=%s (%s)", branch, state)
	}
	b.WriteString(header + "\n")

	if len(r.Files) > 0 {
		fmt.Fprintf(&b, "files (%d):\n", len(r.Files))
		for _, f := range r.Files {
			fmt.Fprintf(&b, "  %s  [%s]\n", f.Path, f.LastVerb)
		}
	}
	if len(r.Searches) > 0 {
		fmt.Fprintf(&b, "searches (%d):\n", len(r.Searches))
		for _, s := range r.Searches {
			if s.Path != "" {
				fmt.Fprintf(&b, "  %s  in %s\n", s.Pattern, s.Path)
			} else {
				fmt.Fprintf(&b, "  %s\n", s.Pattern)
			}
		}
	}
	if r.LastErr != nil {
		fmt.Fprintf(&b, "last error: %s %s", r.LastErr.Verb, r.LastErr.Code)
		if r.LastErr.Msg != "" {
			fmt.Fprintf(&b, " — %s", r.LastErr.Msg)
		}
		b.WriteString("\n")
	}
	if len(r.Files) == 0 && len(r.Searches) == 0 && r.LastErr == nil {
		b.WriteString("(no recent activity)\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// -- helpers (duplicated lightly with recap; kept local so packages
// don't depend on each other) --

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

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
