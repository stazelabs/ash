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
			{Name: "range", Type: "string", Default: "", Description: "Range to read, formatted as start:end (e.g. 1:100)."},
			{Name: "range_kind", Type: "string", Default: "lines", Values: []string{"lines", "bytes"}, Description: "Unit for the range argument."},
			{Name: "limit_bytes", Type: "int", Default: "262144", Description: "Maximum bytes to return. Hard cap is 256 KiB."},
		},
	},
	{
		Verb:        "find",
		Description: "Walk a directory tree and return matching paths. Respects .gitignore by default.",
		Args: []ArgSchema{
			{Name: "path", Type: "string", Required: true, Description: "Starting directory for the walk."},
			{Name: "glob", Type: "string", Default: "**", Description: "Doublestar glob pattern; matched against the path relative to --path."},
			{Name: "type", Type: "string", Default: "any", Values: []string{"any", "file", "dir", "symlink"}, Description: "Filter by entry type."},
			{Name: "max_depth", Type: "int", Default: "0", Description: "Maximum directory depth to descend. 0 means unlimited."},
			{Name: "limit", Type: "int", Default: "256", Description: "Maximum number of results. Hard cap is 4096."},
			{Name: "exclude", Type: "string", Default: "", Description: "Doublestar pattern; matching entries are skipped entirely."},
			{Name: "include_hidden", Type: "bool", Default: "false", Description: "When false, directories starting with '.' are skipped. Leaf dotfiles remain findable."},
			{Name: "respect_gitignore", Type: "bool", Default: "true", Description: "When true, .gitignore at the walk root is loaded and applied. Pass false for a raw walk."},
			{Name: "with_meta", Type: "bool", Default: "false", Description: "When true, each pretty-form row shows '<F|D|L> <size> <yyyy-mm-dd> <path>'. Default is path-only (with trailing '/' for dirs); use 'ash stat' for size/mtime."},
		},
	},
	{
		Verb:        "grep",
		Description: "Search files for an RE2 pattern. Skips binary files and files >16 MiB. Respects .gitignore by default.",
		Args: []ArgSchema{
			{Name: "pattern", Type: "string", Required: true, Description: "RE2 regex (or literal text when fixed_string=true)."},
			{Name: "path", Type: "string", Required: true, Description: "File or directory to search."},
			{Name: "glob", Type: "string", Default: "**", Description: "Doublestar pattern; only files matching this are scanned."},
			{Name: "case", Type: "string", Default: "smart", Values: []string{"smart", "sensitive", "insensitive"}, Description: "Case sensitivity. smart = insensitive unless pattern has an uppercase letter."},
			{Name: "fixed_string", Type: "bool", Default: "false", Description: "Treat pattern as literal text instead of a regex."},
			{Name: "word", Type: "bool", Default: "false", Description: "Require word boundaries (\\b) around the pattern."},
			{Name: "max_matches", Type: "int", Default: "256", Description: "Cap on total match records. Hard cap is 4096."},
			{Name: "max_per_file", Type: "int", Default: "0", Description: "Cap on records per file. 0 means unlimited."},
			{Name: "context_before", Type: "int", Default: "0", Description: "Lines of context before each match. Max 50."},
			{Name: "context_after", Type: "int", Default: "0", Description: "Lines of context after each match. Max 50."},
			{Name: "files_only", Type: "bool", Default: "false", Description: "Return only the paths of files containing at least one match."},
			{Name: "exclude", Type: "string", Default: "", Description: "Doublestar pattern; matching paths are skipped."},
			{Name: "max_depth", Type: "int", Default: "0", Description: "Maximum directory depth to descend. 0 means unlimited."},
			{Name: "include_hidden", Type: "bool", Default: "false", Description: "When false, directories starting with '.' are skipped."},
			{Name: "respect_gitignore", Type: "bool", Default: "true", Description: "When true, .gitignore at the walk root is loaded and applied."},
		},
	},
	{
		Verb:        "git",
		Description: "Version control as structured calls. Single verb with --op discriminator. Live ops: status, log. Shells out to system git.",
		Args: []ArgSchema{
			{Name: "op", Type: "string", Required: true, Values: []string{"status", "log"}, Description: "Subcommand to run. More ops in subsequent ships (diff, blame, show, ...)."},
			{Name: "path", Type: "string", Default: ".", Description: "Repository path (any path inside a git work tree)."},
			{Name: "untracked", Type: "bool", Default: "true", Description: "[status] include untracked files. Pass false to suppress."},
			{Name: "ignored", Type: "bool", Default: "false", Description: "[status] include gitignored files."},
			{Name: "limit", Type: "int", Default: "20", Description: "[log] maximum commits to return. Hard cap is 200."},
			{Name: "range", Type: "string", Default: "", Description: "[log] git revision range (e.g. 'main..feature' or 'HEAD~10..HEAD')."},
			{Name: "author", Type: "string", Default: "", Description: "[log] filter commits by author name/email substring."},
			{Name: "since", Type: "string", Default: "", Description: "[log] only commits after this date (any format git --since accepts, e.g. '1 week ago')."},
			{Name: "until", Type: "string", Default: "", Description: "[log] only commits before this date."},
			{Name: "pathspec", Type: "string", Default: "", Description: "[log] only commits affecting this path (single path, passed after `--`)."},
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
		Verb:        "stat",
		Description: "Return filesystem metadata for one or more explicit paths. Uses lstat, so symlinks are reported as their own type. Missing paths produce a per-entry error rather than failing the whole call.",
		Args: []ArgSchema{
			{Name: "paths", Type: "string", Description: "Comma-separated list of paths to inspect (e.g. 'cmd/ash/main.go,internal/'). One of --paths or --path is required."},
			{Name: "path", Type: "string", Description: "Single-path alias for --paths (e.g. --path cmd/ash/main.go). One of --paths or --path is required."},
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
