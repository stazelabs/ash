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
	Ops         []string `msgpack:"ops,omitempty"`    // [git] which ops this arg applies to; empty = global
	Mode        string   `msgpack:"mode,omitempty"`   // [edit] which mode (string/range/patch); empty = all modes
	PH          string   `msgpack:"ph,omitempty"`     // placeholder override for usage rendering
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
			{Name: "untracked", Type: "bool", Default: "true", Ops: []string{"status"}, Description: "[status] include untracked files. Pass false to suppress."},
			{Name: "ignored", Type: "bool", Default: "false", Ops: []string{"status"}, Description: "[status] include gitignored files."},
			{Name: "limit", Type: "int", Default: "20", Ops: []string{"log"}, Description: "[log] maximum commits to return. Hard cap is 200."},
			{Name: "staged", Type: "bool", Default: "false", Ops: []string{"diff"}, Description: "[diff] diff index vs HEAD (--cached). Default diffs worktree vs index."},
			{Name: "ref", Type: "string", Default: "", PH: "<rev>", Ops: []string{"show"}, Description: "[show] commit ref to inspect (SHA, HEAD, HEAD~1, branch, tag, etc.). Required for show. Root commits diff against the empty tree."},
			{Name: "range", Type: "string", Default: "", PH: "<rev>", Ops: []string{"log", "diff"}, Description: "[log/diff] git revision range (e.g. 'main..feature', 'HEAD~10..HEAD', or single commit 'HEAD~1')."},
			{Name: "author", Type: "string", Default: "", Ops: []string{"log"}, Description: "[log] filter commits by author name/email substring."},
			{Name: "since", Type: "string", Default: "", Ops: []string{"log"}, Description: "[log] only commits after this date (any format git --since accepts, e.g. '1 week ago')."},
			{Name: "until", Type: "string", Default: "", Ops: []string{"log"}, Description: "[log] only commits before this date."},
			{Name: "pathspec", Type: "string", Default: "", Ops: []string{"log", "diff", "show"}, Description: "[log/diff/show] restrict to a single path (passed after -- to git). Interpreted relative to the repo root, not relative to --path."},
			{Name: "stat", Type: "bool", Default: "false", Ops: []string{"diff", "show"}, Description: "[diff/show] return per-file addition/deletion counts only (no patch text). Much cheaper in tokens."},
			{Name: "context", Type: "int", Default: "3", Ops: []string{"diff", "show"}, Description: "[diff/show] unified diff context lines. Max 50."},
			{Name: "limit_bytes", Type: "int", Default: "262144", Ops: []string{"diff", "show"}, Description: "[diff/show] cap on total patch bytes returned. Files beyond cap have patch omitted but stats preserved. Max 4 MiB."},
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
			{Name: "session", Type: "string", Default: "current", Values: []string{"current", "all", "<id>"}, Description: "Session scope: 'current' (this daemon session), 'all', or an explicit session ID. With --root or --all_roots, defaults to no session filter."},
			{Name: "since", Type: "string", Default: "", Description: "Time window, e.g. '15m', '1h', '24h', '7d'. Supports Go duration syntax plus 'd' for days."},
			{Name: "last", Type: "int", Default: "", Description: "Row cap applied after session/since filters. Maximum is 5000."},
			{Name: "verb", Type: "string", Default: "", Description: "Restrict aggregation to calls for a specific verb."},
			{Name: "top", Type: "int", Default: "5", Description: "Max entries shown in truncation hotspots and error histogram sections. Maximum is 100."},
			{Name: "root", Type: "string", Default: "", Description: "Project root whose ledger to query (read-only). Mutually exclusive with --all_roots. Use to analyze a target repo from outside it."},
			{Name: "all_roots", Type: "bool", Default: "false", Description: "Aggregate across every root in the installed-repos registry. Pretty form includes a per-root breakdown."},
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
			{Name: "paths", Type: "string", PH: "<p1>[,<p2>...]", Description: "Comma-separated list of paths to inspect (e.g. 'cmd/ash/main.go,internal/'). One of --paths or --path is required."},
			{Name: "path", Type: "string", Description: "Single-path alias for --paths (e.g. --path cmd/ash/main.go). One of --paths or --path is required."},
			{Name: "follow_symlinks", Type: "bool", Default: "false", Description: "When true, resolve each symlink with os.Stat and report the target's type/size/mtime/mode. link_target is preserved for traceability. Broken symlinks produce error=broken_symlink rather than failing the call."},
			{Name: "with_meta", Type: "bool", Default: "false", Description: "When true the pretty rows include mode + mtime: `<F|D|L> <size> <mode> <mtime> <path>`. Default lean rows are `<F|D|L> <size> <path>`. Wire data always carries mode + mtime."},
		},
	},
	{
		Verb:        "edit",
		Description: "In-place file mutation. String-replacement mode (old_string/new_string), line-range mode (range/new_content), or patch mode (patch). Atomic write via temp-file+rename.",
		Args: []ArgSchema{
			{Name: "path", Type: "string", Required: true, Description: "File to edit."},
			{Name: "old_string", Type: "string", Mode: "string", Description: "[string mode] Exact text to find (required if range/patch not provided). Must appear exactly once unless replace_all=true."},
			{Name: "new_string", Type: "string", Mode: "string", Default: "", Description: "[string mode] Replacement text. Empty string deletes the matched text."},
			{Name: "replace_all", Type: "bool", Mode: "string", Default: "false", Description: "[string mode] Replace every occurrence of old_string. If false, errors when old_string appears more than once."},
			{Name: "range", Type: "string", Mode: "range", Description: "[range mode] Line range to replace, formatted as start:end, 1-based inclusive (required if old_string/patch not provided)."},
			{Name: "new_content", Type: "string", Mode: "range", Default: "", Description: "[range mode] Replacement text for the specified lines. Empty string deletes the lines."},
			{Name: "patch", Type: "string", Mode: "patch", PH: "<diff|->", Description: "[patch mode] Unified diff to apply (required if old_string/range not provided). Pass '-' to read from stdin. Error codes: patch_parse_error, patch_failed."},
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
			{Name: "packages", Type: "string", Default: "./...", PH: "<pkgs>", Description: "Comma-separated package patterns passed positionally to go test (e.g. ./...,internal/walker)."},
			{Name: "run", Type: "string", Default: "", Description: "Regex passed to go test -run; filters which tests execute."},
			{Name: "count", Type: "int", Default: "1", Description: "Maps to go test -count. Default 1 bypasses the test cache (agents typically want fresh runs after editing). Pass 0 to use the cache."},
			{Name: "race", Type: "bool", Default: "false", Description: "Enable the race detector (-race)."},
			{Name: "short", Type: "bool", Default: "false", Description: "Enable -short mode."},
			{Name: "timeout", Type: "string", Default: "60s", PH: "<dur>", Description: "Go duration for the outer wall (context.WithTimeout). Also passed to go test -timeout (1s grace earlier) so go aborts cleanly first. CI-shaped suites can pass --timeout 10m."},
			{Name: "verbose", Type: "bool", Default: "false", Description: "Render hint: include passing test names per package. Failure output is unconditional."},
		},
	},
	{
		Verb:        "init",
		Description: "Bootstrap a target repo for ash: write the PreToolUse hook into .claude/settings.json, append .ash/ to .gitignore, write or merge the agent-guidance section into CLAUDE.md (or AGENTS.md if the target repo already uses it) between <!-- ash:begin --> / <!-- ash:end --> markers, and record the absolute root in the global installed-repos registry. Idempotent: re-running on an installed repo is a no-op.",
		Args: []ArgSchema{
			{Name: "path", Type: "string", Default: ".", Description: "Target repo root (absolute or relative). Default . uses the daemons project root."},
			{Name: "force", Type: "bool", Default: "false", Description: "Replace an existing PreToolUse entry that invokes ash with a different command, or replace an existing CLAUDE.md/AGENTS.md ash-managed section whose content differs from the current template. Without --force a conflict produces a warning and no change."},
			{Name: "no_registry", Type: "bool", Default: "false", Description: "Skip writing the installed-repos registry. Useful for ephemeral test repos."},
		},
	},
	{
		Verb:        "uninit",
		Description: "Reverse `ash init`: remove the ash PreToolUse entry from .claude/settings.json, drop the .ash/ line from .gitignore, strip the ash-managed section from CLAUDE.md (or AGENTS.md), and remove the registry entry. The .ash/ledger.db and any user content outside the markers are left in place.",
		Args: []ArgSchema{
			{Name: "path", Type: "string", Default: ".", Description: "Target repo root."},
			{Name: "no_registry", Type: "bool", Default: "false", Description: "Skip the registry removal."},
		},
	},
	{
		Verb:        "stop",
		Description: "Stop the per-project ash daemon cleanly. Sends SIGTERM and waits up to 7s for exit. Idempotent: returns success when no daemon is running. The next ash invocation auto-starts a fresh daemon.",
		Args:        []ArgSchema{},
	},
}

// Registry returns the full verb schema registry (read-only view for tests and tooling).
func Registry() []VerbSchema { return registry }

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
	var r Result
	if err := proto.UnmarshalData(rsp, &r); err != nil {
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

// verbDisplayOrder defines display order in usage output.
// Update this list when new verbs are added to the registry.
var verbDisplayOrder = []string{
	"read", "write", "edit", "diff",
	"find", "grep", "git",
	"metrics", "report", "stat", "bench", "test",
	"help", "hook", "init", "uninit", "stop",
}

// RenderUsage produces the full usage string printed when `ash` is invoked
// with no arguments or --help. termWidth is used for soft line-wrapping;
// pass 0 to use the default (100).
func RenderUsage(termWidth int) string {
	if termWidth <= 0 {
		termWidth = 100
	}
	var b strings.Builder
	b.WriteString("usage: ash <verb> [<positional>...] [--key value | --key=value]... [--format pretty|json|msgpack]\n")
	b.WriteString("\nverbs (phase 2):\n")

	for _, name := range verbDisplayOrder {
		var vs *VerbSchema
		for i := range registry {
			if registry[i].Verb == name {
				vs = &registry[i]
				break
			}
		}
		if vs == nil {
			continue
		}
		switch vs.Verb {
		case "git":
			renderGitVerb(&b, vs, termWidth)
		case "edit":
			renderEditVerb(&b, vs, termWidth)
		default:
			renderFlatVerb(&b, vs, termWidth)
		}
	}

	b.WriteString(`
positional args (most verbs accept their dominant arg positionally):
  ash read foo.go              (== ash read --path foo.go)
  ash grep TODO cmd            (== ash grep --pattern TODO --path cmd)
  ash find cmd                 (== ash find --path cmd)
  ash stat a,b,c               (== ash stat --paths a,b,c)
  ash write foo.go --content X (== ash write --path foo.go --content X)

note: pass - as a value to read that arg from stdin (e.g. --content -)

global flags:
  --format pretty|json|msgpack   output format (default: pretty)

ash auto-starts the daemon (ashd) on first call.`)
	return b.String()
}

// argPlaceholder returns the compact placeholder string for arg value in usage.
func argPlaceholder(a ArgSchema) string {
	if a.PH != "" {
		return a.PH
	}
	if len(a.Values) > 0 {
		return strings.Join(a.Values, "|")
	}
	switch a.Type {
	case "bool":
		return "true|false"
	case "int":
		return "N"
	default: // string
		switch a.Name {
		case "path", "other", "root", "pathspec":
			return "<p>"
		case "pattern":
			return "<re>"
		case "content":
			return "<text|->"
		case "new_content", "old_string", "new_string":
			return "<text>"
		case "range":
			return "start:end"
		case "glob", "exclude":
			return "<pattern>"
		case "since", "until":
			return "<dur>"
		case "author":
			return "<s>"
		default:
			return "<" + a.Name + ">"
		}
	}
}

// argToken returns the usage token for a single arg: "--name ph" or "[--name ph]".
func argToken(a ArgSchema) string {
	ph := argPlaceholder(a)
	inner := "--" + a.Name + " " + ph
	if a.Required {
		return inner
	}
	return "[" + inner + "]"
}

// writeWrapped writes prefix+tokens to b, wrapping onto continuation lines.
func writeWrapped(b *strings.Builder, prefix, cont string, tokens []string, termWidth int) {
	line := prefix
	first := true
	for _, tok := range tokens {
		if first {
			line += tok
			first = false
		} else if termWidth > 0 && len(line)+1+len(tok) > termWidth {
			fmt.Fprintf(b, "%s\n", line)
			line = cont + tok
		} else {
			line += " " + tok
		}
	}
	fmt.Fprintf(b, "%s\n", line)
}

const verbNameW = 8 // verb name padded to this width; 2 leading spaces → col 10

// renderFlatVerb renders a verb with all args on one or more wrapped lines.
func renderFlatVerb(b *strings.Builder, vs *VerbSchema, termWidth int) {
	prefix := fmt.Sprintf("  %-*s", verbNameW, vs.Verb)
	cont := strings.Repeat(" ", len(prefix))
	var tokens []string
	for _, a := range vs.Args {
		tokens = append(tokens, argToken(a))
	}
	writeWrapped(b, prefix, cont, tokens, termWidth)
}

// renderEditVerb renders the edit verb grouped by mode (string/range/patch).
// Mode="" args (path, dry_run) are shared and appear at the start of each mode line.
func renderEditVerb(b *strings.Builder, vs *VerbSchema, termWidth int) {
	prefix := fmt.Sprintf("  %-*s", verbNameW, "edit")
	cont := strings.Repeat(" ", len(prefix))

	var sharedArgs []ArgSchema
	modeArgs := map[string][]ArgSchema{}
	modeOrder := []string{"string", "range", "patch"}
	for _, a := range vs.Args {
		if a.Mode == "" {
			sharedArgs = append(sharedArgs, a)
		} else {
			modeArgs[a.Mode] = append(modeArgs[a.Mode], a)
		}
	}

	for i, mode := range modeOrder {
		args, ok := modeArgs[mode]
		if !ok {
			continue
		}
		var tokens []string
		for _, a := range sharedArgs {
			if a.Name == "dry_run" {
				continue // dry_run appended last
			}
			tokens = append(tokens, argToken(a))
		}
		for _, a := range args {
			tokens = append(tokens, argToken(a))
		}
		// dry_run is always last
		for _, a := range sharedArgs {
			if a.Name == "dry_run" {
				tokens = append(tokens, argToken(a))
			}
		}
		pfx := prefix
		if i > 0 {
			pfx = cont
		}
		writeWrapped(b, pfx, cont, tokens, termWidth)
	}
}

// renderGitVerb renders the git verb with a per-op sub-block.
func renderGitVerb(b *strings.Builder, vs *VerbSchema, termWidth int) {
	prefix := fmt.Sprintf("  %-*s", verbNameW, "git")
	cont := strings.Repeat(" ", len(prefix))

	var globalArgs []ArgSchema
	opArgsMap := map[string][]ArgSchema{}
	opsOrdered := []string{"status", "log", "diff", "show"}

	for _, a := range vs.Args {
		if len(a.Ops) == 0 {
			globalArgs = append(globalArgs, a)
		}
	}
	for _, op := range opsOrdered {
		for _, a := range vs.Args {
			for _, ao := range a.Ops {
				if ao == op {
					opArgsMap[op] = append(opArgsMap[op], a)
					break
				}
			}
		}
	}

	var firstTokens []string
	for _, a := range globalArgs {
		firstTokens = append(firstTokens, argToken(a))
	}
	if len(opArgsMap) > 0 {
		firstTokens = append(firstTokens, "[op-specific flags]")
	}
	writeWrapped(b, prefix, cont, firstTokens, termWidth)

	if len(opArgsMap) == 0 {
		return
	}

	maxOpW := 0
	for _, op := range opsOrdered {
		if _, ok := opArgsMap[op]; ok && len(op) > maxOpW {
			maxOpW = len(op)
		}
	}

	// ops: sub-block indented to cont+"     ops: "
	opSectionPfx := cont + "     ops: "
	opSectionW := len(opSectionPfx)
	first := true
	for _, op := range opsOrdered {
		args, ok := opArgsMap[op]
		if !ok {
			continue
		}
		var opPfx string
		if first {
			opPfx = opSectionPfx + fmt.Sprintf("%-*s ", maxOpW, op)
			first = false
		} else {
			opPfx = strings.Repeat(" ", opSectionW) + fmt.Sprintf("%-*s ", maxOpW, op)
		}
		opCont := strings.Repeat(" ", len(opPfx))
		var tokens []string
		for _, a := range args {
			tokens = append(tokens, argToken(a))
		}
		writeWrapped(b, opPfx, opCont, tokens, termWidth)
	}
}
