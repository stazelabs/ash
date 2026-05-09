// Package help implements the `help` verb.
//
// Args:
//
//	verb   string  (optional) - return schema for a specific verb; omit for all verbs
//
// Returns structured argument schemas for all live verbs (or one verb).
// The schema is static — it mirrors what ParseArgs in each verb package enforces.
package help

import (
	"fmt"
	"strings"

	"github.com/stazelabs/ash/internal/proto"
	"github.com/stazelabs/ash/internal/verbs/argutil"
)

type ArgSchema struct {
	Name        string   `msgpack:"name"`
	Type        string   `msgpack:"type"`             // "string" | "int" | "bool"
	Required    bool     `msgpack:"required,omitempty"`
	Default     string   `msgpack:"default,omitempty"`
	Description string   `msgpack:"description"`
	Values      []string `msgpack:"values,omitempty"` // valid enum values
}

type VerbSchema struct {
	Verb        string      `msgpack:"verb"`
	Description string      `msgpack:"description"`
	Args        []ArgSchema `msgpack:"args"`
}

type Result struct {
	Verbs []VerbSchema `msgpack:"verbs"`
	Count int          `msgpack:"count"`
}

type Args struct {
	Verb string // empty = all verbs
}

var registry = []VerbSchema{
	{
		Verb:        "read",
		Description: "Read a file (or a byte/line range of one). UTF-8 is returned as-is; binary is base64-encoded.",
		Args: []ArgSchema{
			{Name: "path", Type: "string", Required: true, Description: "Absolute or relative path to the file."},
			{Name: "range", Type: "string", Default: "", Description: "Range to read, formatted as start:end (e.g. 1:100). 1-based, inclusive on both ends. End is clamped to file length."},
			{Name: "range_kind", Type: "string", Default: "lines", Values: []string{"lines", "bytes"}, Description: "Unit for the range argument."},
			{Name: "limit_bytes", Type: "int", Default: "262144", Description: "Maximum bytes to return. Default 256 KiB; hard cap 8 MiB. The truncation hint in the response shows how to narrow with --range or raise the cap."},
			{Name: "with_meta", Type: "bool", Default: "false", Description: "When true the pretty header includes encoding + mtime. Default lean header omits both (encoding only surfaces when non-utf-8). Wire data always carries them."},
		},
	},
	{
		Verb:        "find",
		Description: "Walk a directory tree and return matching paths. Respects .gitignore by default.",
		Args: []ArgSchema{
			{Name: "path", Type: "string", Required: true, Description: "Starting directory for the walk."},
			{Name: "glob", Type: "string", Default: "**", Description: "Doublestar glob pattern; matched against the path relative to --path."},
			{Name: "type", Type: "string", Default: "any", Values: []string{"any", "file", "dir", "symlink"}, Description: "Filter by entry type."},
			{Name: "max_depth", Type: "int", Default: "0", Description: "Maximum directory depth to descend. 0 means unlimited; 1 = direct children of --path only."},
			{Name: "limit", Type: "int", Default: "256", Description: "Maximum number of results. Hard cap is 4096."},
			{Name: "exclude", Type: "string", Default: "", Description: "Doublestar pattern; matching entries are skipped entirely. Matched against path relative to --path (same as --glob)."},
			{Name: "include_hidden", Type: "bool", Default: "false", Description: "When false, directories starting with '.' are skipped. Leaf dotfiles remain findable."},
			{Name: "respect_gitignore", Type: "bool", Default: "true", Description: "When true, .gitignore at the walk root (--path) is loaded and applied. Pass false for a raw walk."},
			{Name: "with_meta", Type: "bool", Default: "false", Description: "When true, each pretty-form row shows '<F|D|L> <size> <yyyy-mm-dd> <path>'. Default is path-only (with trailing '/' for dirs); use 'ash stat' for size/mtime."},
		},
	},
	{
		Verb:        "grep",
		Description: "Search files for an RE2 pattern. Skips binary files and files >16 MiB. Respects .gitignore by default.",
		Args: []ArgSchema{
			{Name: "pattern", Type: "string", Required: true, Description: "RE2 regex (or literal text when fixed_string=true)."},
			{Name: "path", Type: "string", Required: true, Description: "File or directory to search. Returned match paths mirror the input form: absolute input yields absolute paths, relative input yields relative paths."},
			{Name: "glob", Type: "string", Default: "**", Description: "Doublestar pattern; only files matching this are scanned. Matched against the path relative to --path (the walk root)."},
			{Name: "case", Type: "string", Default: "smart", Values: []string{"smart", "sensitive", "insensitive"}, Description: "Case sensitivity. smart = insensitive unless pattern has an uppercase letter."},
			{Name: "fixed_string", Type: "bool", Default: "false", Description: "Treat pattern as literal text instead of a regex."},
			{Name: "word", Type: "bool", Default: "false", Description: "Require word boundaries (\\b) around the pattern."},
			{Name: "max_matches", Type: "int", Default: "256", Description: "Cap on total match records. Hard cap is 4096."},
			{Name: "max_per_file", Type: "int", Default: "0", Description: "Cap on records per file. 0 means unlimited."},
			{Name: "context_before", Type: "int", Default: "0", Description: "Lines of context before each match. Max 50. Context lines are deduplicated across overlapping matches."},
			{Name: "context_after", Type: "int", Default: "0", Description: "Lines of context after each match. Max 50. Context lines are deduplicated across overlapping matches."},
			{Name: "files_only", Type: "bool", Default: "false", Description: "Return only the paths of files containing at least one match."},
			{Name: "no_text", Type: "bool", Default: "false", Description: "Omit the matched line text from records; pretty form renders path:line:col only."},
			{Name: "exclude", Type: "string", Default: "", Description: "Doublestar pattern; matching paths are skipped. Matched against path relative to --path (the walk root)."},
			{Name: "max_depth", Type: "int", Default: "0", Description: "Maximum directory depth to descend. 0 means unlimited."},
			{Name: "include_hidden", Type: "bool", Default: "false", Description: "When false, directories starting with '.' are skipped."},
			{Name: "respect_gitignore", Type: "bool", Default: "true", Description: "When true, .gitignore at the walk root (--path) is loaded and applied."},
		},
	},
	{
		Verb:        "git",
		Description: "Version control as structured calls. Single verb with --op discriminator. Live ops: status, log, diff, show. Shells out to system git.",
		Args: []ArgSchema{
			{Name: "op", Type: "string", Required: true, Values: []string{"status", "log", "diff", "show"}, Description: "Subcommand to run."},
			{Name: "path", Type: "string", Default: ".", Description: "Repository path (any path inside a git work tree). Note: returned file paths are always repo-root-relative regardless of how --path was passed. This departs from find/grep where paths mirror the --path form."},
			{Name: "untracked", Type: "bool", Default: "true", Description: "[status] include untracked files. Pass false to suppress."},
			{Name: "ignored", Type: "bool", Default: "false", Description: "[status] include gitignored files."},
			{Name: "limit", Type: "int", Default: "20", Description: "[log] maximum commits to return. Hard cap is 200."},
			{Name: "author", Type: "string", Default: "", Description: "[log] filter commits by author name/email substring."},
			{Name: "since", Type: "string", Default: "", Description: "[log] only commits after this date (any format git --since accepts, e.g. '1 week ago')."},
			{Name: "until", Type: "string", Default: "", Description: "[log] only commits before this date."},
			{Name: "range", Type: "string", Default: "", Description: "[log/diff] git revision range (e.g. 'main..feature', 'HEAD~10..HEAD', or single commit 'HEAD~1')."},
			{Name: "pathspec", Type: "string", Default: "", Description: "[log/diff] restrict to a single path (passed after -- to git). Interpreted relative to the repo root, not relative to --path."},
			{Name: "staged", Type: "bool", Default: "false", Description: "[diff] diff index vs HEAD (--cached). Default diffs worktree vs index."},
			{Name: "stat", Type: "bool", Default: "false", Description: "[diff] return per-file addition/deletion counts only (no patch text). Much cheaper in tokens."},
			{Name: "context", Type: "int", Default: "3", Description: "[diff] unified diff context lines. Max 50."},
			{Name: "limit_bytes", Type: "int", Default: "262144", Description: "[diff/show] cap on total patch bytes returned. Files beyond cap have patch omitted but stats preserved. Max 4 MiB."},
			{Name: "ref", Type: "string", Default: "", Description: "[show] commit ref to inspect (SHA, HEAD, HEAD~1, branch, tag, etc.). Required for show. Root commits diff against the empty tree."},
		},
	},
	{
		Verb:        "write",
		Description: "Write content to a file. Creates parent directories by default. Atomic write via temp-file+rename to avoid partial files on crash.",
		Args: []ArgSchema{
			{Name: "path", Type: "string", Required: true, Description: "File path to write. Absolute or relative to the daemon's project root."},
			{Name: "content", Type: "string", Required: true, Description: "File content. UTF-8 text by default; base64-encoded bytes when encoding=base64. Pass '-' to read from stdin."},
			{Name: "encoding", Type: "string", Default: "utf-8", Values: []string{"utf-8", "base64"}, Description: "Content encoding. Use base64 for binary files."},
			{Name: "mkdir", Type: "bool", Default: "true", Description: "Create missing parent directories. Pass false to require the parent to already exist."},
			{Name: "create_only", Type: "bool", Default: "false", Description: "Fail with 'exists' error if the file already exists. Useful as a safety guard against accidental overwrites."},
		},
	},
	{
		Verb:        "metrics",
		Description: "Query recent call history from the ledger without shelling out to sqlite3.",
		Args: []ArgSchema{
			{Name: "last", Type: "int", Default: "20", Description: "Number of most-recent calls to return. Maximum is 200."},
			{Name: "verb", Type: "string", Default: "", Description: "Filter results to calls for a specific verb (e.g. 'find')."},
		},
	},
	{
		Verb:        "report",
		Description: "Aggregate per-verb summary across ledger calls: n, ok%, p50/p95 latency, p50/p95 tokens_out, trunc%.",
		Args: []ArgSchema{
			{Name: "session", Type: "string", Default: "current", Description: "Session scope: 'current' (this daemon session), 'all', or an explicit session ID."},
			{Name: "since", Type: "string", Default: "", Description: "Time window, e.g. '15m', '1h', '24h', '7d'. Supports Go duration syntax plus 'd' for days."},
			{Name: "last", Type: "int", Default: "", Description: "Row cap applied after session/since filters. Maximum is 5000."},
			{Name: "verb", Type: "string", Default: "", Description: "Restrict aggregation to calls for a specific verb."},
			{Name: "top", Type: "int", Default: "5", Description: "Max entries shown in truncation hotspots and error histogram sections. Maximum is 100."},
		},
	},
	{
		Verb:        "help",
		Description: "Return the structured argument schema for one verb or all verbs.",
		Args: []ArgSchema{
			{Name: "verb", Type: "string", Default: "", Description: "Verb name to describe. Omit to return schemas for all verbs."},
		},
	},
	{
		Verb:        "hook",
		Description: "Claude Code PreToolUse decision engine. Reads a hook payload from stdin (when invoked as `ash hook` from the harness) and returns a deny/allow decision steering the agent toward ash equivalents. Daemon-side dispatch uses tool_name and tool-specific fields directly. Not normally invoked manually.",
		Args: []ArgSchema{
			{Name: "tool_name", Type: "string", Description: "Harness tool the agent attempted (e.g. Grep, Bash, Edit, Write, Read)."},
			{Name: "command", Type: "string", Description: "[Bash] command line."},
			{Name: "pattern", Type: "string", Description: "[Grep] regex / [Glob] pattern."},
			{Name: "path", Type: "string", Description: "[Grep/Glob] search root or [Read fallback] path."},
			{Name: "glob", Type: "string", Description: "[Grep] file glob filter."},
			{Name: "file_path", Type: "string", Description: "[Read/Edit/Write] target file path (harness key)."},
			{Name: "old_string", Type: "string", Description: "[Edit] text to replace."},
			{Name: "new_string", Type: "string", Description: "[Edit] replacement text."},
			{Name: "content", Type: "string", Description: "[Write] new file content."},
		},
	},
	{
		Verb:        "stat",
		Description: "Return filesystem metadata for one or more explicit paths. Uses lstat, so symlinks are reported as their own type. Missing paths produce a per-entry error rather than failing the whole call.",
		Args: []ArgSchema{
			{Name: "paths", Type: "string", Description: "Comma-separated list of paths to inspect (e.g. 'cmd/ash/main.go,internal/'). One of --paths or --path is required."},
			{Name: "path", Type: "string", Description: "Single-path alias for --paths (e.g. --path cmd/ash/main.go). One of --paths or --path is required."},
			{Name: "follow_symlinks", Type: "bool", Default: "false", Description: "When true, resolve each symlink with os.Stat and report the target's type/size/mtime/mode. link_target is preserved for traceability. Broken symlinks produce error=broken_symlink rather than failing the call."},
		},
	},
	{
		Verb:        "edit",
		Description: "In-place file mutation. String-replacement mode (old_string/new_string) or line-range mode (range/new_content). Atomic write via temp-file+rename.",
		Args: []ArgSchema{
			{Name: "path", Type: "string", Required: true, Description: "File to edit."},
			{Name: "old_string", Type: "string", Description: "[string mode] Exact text to find (required if range not provided). Must appear exactly once unless replace_all=true."},
			{Name: "new_string", Type: "string", Default: "", Description: "[string mode] Replacement text. Empty string deletes the matched text."},
			{Name: "replace_all", Type: "bool", Default: "false", Description: "[string mode] Replace every occurrence of old_string. If false, errors when old_string appears more than once."},
			{Name: "range", Type: "string", Description: "[range mode] Line range to replace, formatted as start:end, 1-based inclusive (required if old_string not provided)."},
			{Name: "new_content", Type: "string", Default: "", Description: "[range mode] Replacement text for the specified lines. Empty string deletes the lines."},
			{Name: "dry_run", Type: "bool", Default: "false", Description: "Compute the replacement but do not write. Result includes a unified diff in the patch field."},
		},
	},
	{
		Verb:        "diff",
		Description: "Compute a unified diff between a file and another file or inline content. Both inputs capped at 2000 lines. Returns additions, deletions, and the patch text.",
		Args: []ArgSchema{
			{Name: "path", Type: "string", Required: true, Description: "File to use as the before (a) side."},
			{Name: "other", Type: "string", Description: "Second file for the after (b) side. Mutually exclusive with content."},
			{Name: "content", Type: "string", Description: "Inline after-side text. Mutually exclusive with other. Pass '-' to read from stdin."},
			{Name: "context", Type: "int", Default: "3", Description: "Context lines per hunk. Max 50."},
			{Name: "stat", Type: "bool", Default: "false", Description: "Return counts only (additions, deletions, unchanged). Omits patch text. Much cheaper in tokens when you only need to know if or how much changed."},
		},
	},
	{
		Verb:        "bench",
		Description: "Run a canonical case list against ash and the bash equivalent the agent would otherwise have used; tokenize both with the same encoder and report tokens/latency deltas per case.",
		Args: []ArgSchema{
			{Name: "verb", Type: "string", Default: "", Description: "Restrict to cases for one verb (e.g. 'grep')."},
			{Name: "case", Type: "string", Default: "", Description: "Run a single named case (e.g. 'grep_todo_repo'). Overrides --verb."},
			{Name: "limit", Type: "int", Default: "0", Description: "Cap number of cases run after filters. 0 means no cap."},
		},
	},
	{
		Verb:        "test",
		Description: "Run Go tests via `go test -json` and return structured per-package/per-test results. Result.OK=true means no failures; the verb still completes successfully when tests fail. Build failures surface as Status=build_failed.",
		Args: []ArgSchema{
			{Name: "packages", Type: "string", Default: "./...", Description: "Comma-separated package patterns passed positionally to go test (e.g. ./...,internal/walker)."},
			{Name: "run", Type: "string", Default: "", Description: "Regex passed to go test -run; filters which tests execute."},
			{Name: "count", Type: "int", Default: "1", Description: "Maps to go test -count. Default 1 bypasses the test cache (agents typically want fresh runs after editing). Pass 0 to use the cache."},
			{Name: "race", Type: "bool", Default: "false", Description: "Enable the race detector (-race)."},
			{Name: "short", Type: "bool", Default: "false", Description: "Enable -short mode."},
			{Name: "timeout", Type: "string", Default: "60s", Description: "Go duration for the outer wall (context.WithTimeout). Also passed to go test -timeout (1s grace earlier) so go aborts cleanly first. CI-shaped suites can pass --timeout 10m."},
			{Name: "verbose", Type: "bool", Default: "false", Description: "Render hint: include passing test names per package. Failure output is unconditional."},
		},
	},
}

func ParseArgs(in map[string]any) (*Args, *proto.Error) {
	a := &Args{}
	var perr *proto.Error
	if a.Verb, perr = argutil.OptionalString(in, "verb", ""); perr != nil {
		return nil, perr
	}
	return a, nil
}

// Run is signature-compatible with the rest of the verbs. help has no
// instrumentable sub-phases, so tr is unused.
func Run(a *Args, _ *proto.Tracer) (*Result, *proto.Error) {
	if a.Verb == "" {
		r := &Result{Verbs: registry, Count: len(registry)}
		return r, nil
	}
	for _, vs := range registry {
		if vs.Verb == a.Verb {
			return &Result{Verbs: []VerbSchema{vs}, Count: 1}, nil
		}
	}
	return nil, &proto.Error{Code: "not_found", Msg: "unknown verb: " + a.Verb}
}

func PrettyResponse(req *proto.Request, rsp *proto.Response) string {
	if !rsp.OK {
		return proto.PrettyResponseHeader(rsp)
	}
	r, ok := decodeResult(rsp.Data)
	if !ok {
		return "ok\n<unrecognized help result>"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "=== ash help: %d verb(s) ===\n", r.Count)
	for i, vs := range r.Verbs {
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "verb: %s\n", vs.Verb)
		fmt.Fprintf(&b, "  %s\n", vs.Description)
		for _, arg := range vs.Args {
			writeArg(&b, arg)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func writeArg(b *strings.Builder, a ArgSchema) {
	req := "optional"
	if a.Required {
		req = "required"
	}
	fmt.Fprintf(b, "  --%-20s %-8s %-8s", a.Name, a.Type, req)
	if a.Default != "" {
		fmt.Fprintf(b, " default=%-10s", a.Default)
	} else {
		fmt.Fprintf(b, " %-17s", "")
	}
	b.WriteString(a.Description)
	if len(a.Values) > 0 {
		fmt.Fprintf(b, " [%s]", strings.Join(a.Values, "|"))
	}
	b.WriteByte('\n')
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
	if raw, ok := m["verbs"].([]any); ok {
		for _, x := range raw {
			vm, ok := x.(map[string]any)
			if !ok {
				continue
			}
			vs := VerbSchema{}
			if v, ok := vm["verb"].(string); ok {
				vs.Verb = v
			}
			if v, ok := vm["description"].(string); ok {
				vs.Description = v
			}
			if args, ok := vm["args"].([]any); ok {
				for _, ax := range args {
					am, ok := ax.(map[string]any)
					if !ok {
						continue
					}
					arg := ArgSchema{}
					if v, ok := am["name"].(string); ok {
						arg.Name = v
					}
					if v, ok := am["type"].(string); ok {
						arg.Type = v
					}
					if v, ok := am["required"].(bool); ok {
						arg.Required = v
					}
					if v, ok := am["default"].(string); ok {
						arg.Default = v
					}
					if v, ok := am["description"].(string); ok {
						arg.Description = v
					}
					if vals, ok := am["values"].([]any); ok {
						for _, val := range vals {
							if s, ok := val.(string); ok {
								arg.Values = append(arg.Values, s)
							}
						}
					}
					vs.Args = append(vs.Args, arg)
				}
			}
			r.Verbs = append(r.Verbs, vs)
		}
	}
	if v, ok := m["count"].(int); ok {
		r.Count = v
	} else if v, ok := m["count"].(int64); ok {
		r.Count = int(v)
	} else if v, ok := m["count"].(uint64); ok {
		r.Count = int(v)
	}
	return r, true
}
