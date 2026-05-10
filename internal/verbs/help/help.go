// Package help implements the `help` verb.
//
// Args:
//
//	verb    string  (optional) - return schema for a specific verb; omit for all verbs
//	verbose bool    (optional) - show full arg descriptions instead of concise ones
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
	Long        string   `msgpack:"-"`                // verbose-only prose; not on wire
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
	Verb    string
	Verbose bool
}

var registry = []VerbSchema{
	{
		Verb:        "read",
		Description: "Read a file or byte/line range; UTF-8 as-is, binary base64-encoded.",
		Args: []ArgSchema{
			{Name: "path", Type: "string", Required: true,
				Description: "File to read.",
				Long:        "Absolute or relative path to the file."},
			{Name: "range", Type: "string", Default: "",
				Description: "start:end range, 1-based inclusive; end clamped to file length.",
				Long:        "Range to read, formatted as start:end (e.g. 1:100). 1-based, inclusive on both ends. End is clamped to file length."},
			{Name: "unit", Type: "string", Default: "lines", Values: []string{"lines", "bytes"},
				Description: "Unit for the --range argument.",
				Long:        "Unit for the range argument."},
			{Name: "bytes", Type: "int", Default: "262144",
				Description: "Max bytes to return; hard cap 8 MiB.",
				Long:        "Maximum bytes to return. Default 256 KiB; hard cap 8 MiB. The truncation hint in the response shows how to narrow with --range or raise the cap."},
			{Name: "meta", Type: "bool", Default: "false",
				Description: "Include encoding and mtime in the pretty header.",
				Long:        "When true the pretty header includes encoding + mtime. Default lean header omits both (encoding only surfaces when non-utf-8). Wire data always carries them."},
		},
	},
	{
		Verb:        "find",
		Description: "Walk a directory tree and return matching paths; respects .gitignore.",
		Args: []ArgSchema{
			{Name: "path", Type: "string", Required: true,
				Description: "Starting directory for the walk."},
			{Name: "glob", Type: "string", Default: "**",
				Description: "Doublestar glob; matched relative to --path.",
				Long:        "Doublestar glob pattern; matched against the path relative to --path."},
			{Name: "type", Type: "string", Default: "any", Values: []string{"any", "file", "dir", "symlink"},
				Description: "Filter by entry type."},
			{Name: "depth", Type: "int", Default: "0",
				Description: "Max depth; 0 is unlimited, 1 is direct children only.",
				Long:        "Maximum directory depth to descend. 0 means unlimited; 1 = direct children of --path only."},
			{Name: "limit", Type: "int", Default: "256",
				Description: "Max results; hard cap 4096.",
				Long:        "Maximum number of results. Hard cap is 4096."},
			{Name: "exclude", Type: "string", Default: "",
				Description: "Doublestar pattern; matching entries are skipped.",
				Long:        "Doublestar pattern; matching entries are skipped entirely. Matched against path relative to --path (same as --glob)."},
			{Name: "hidden", Type: "bool", Default: "false",
				Description: "Include directories starting with '.' when true.",
				Long:        "When false, directories starting with '.' are skipped. Leaf dotfiles remain findable."},
			{Name: "gi", Type: "bool", Default: "true",
				Description: "Apply .gitignore from the walk root.",
				Long:        "When true, .gitignore at the walk root (--path) is loaded and applied."},
			{Name: "meta", Type: "bool", Default: "false",
				Description: "Show type, size, and date per row; default is path only.",
				Long:        "When true, each pretty-form row shows '<F|D|L> <size> <yyyy-mm-dd> <path>'. Default is path-only (with trailing '/' for dirs); use 'ash stat' for size/mtime."},
			{Name: "absolute", Type: "bool", Default: "false",
				Description: "Emit absolute paths instead of repo-root-relative.",
				Long:        "When true, paths in results are absolute. Default (false) strips the project-root prefix so output is repo-root-relative regardless of --path form (ASH-71)."},
		},
	},
	{
		Verb:        "grep",
		Description: "Search files for an RE2 pattern; skips binary and files >16 MiB.",
		Args: []ArgSchema{
			{Name: "pattern", Type: "string", Required: true,
				Description: "RE2 regex; literal text when lit=true.",
				Long:        "RE2 regex (or literal text when lit=true)."},
			{Name: "path", Type: "string", Required: true,
				Description: "File or directory to search.",
				Long:        "File or directory to search. Match paths in results are repo-root-relative by default (ASH-71); pass --absolute true for the legacy input-mirroring form."},
			{Name: "glob", Type: "string", Default: "**",
				Description: "Doublestar pattern; scan only matching files.",
				Long:        "Doublestar pattern; only files matching this are scanned. Matched against the path relative to --path (the walk root)."},
			{Name: "case", Type: "string", Default: "smart", Values: []string{"smart", "sensitive", "insensitive"},
				Description: "Case sensitivity; smart is insensitive unless pattern has uppercase.",
				Long:        "Case sensitivity. smart = insensitive unless pattern has an uppercase letter."},
			{Name: "lit", Type: "bool", Default: "false",
				Description: "Treat pattern as literal text, not a regex."},
			{Name: "max", Type: "int", Default: "256",
				Description: "Max total match records; hard cap 4096.",
				Long:        "Cap on total match records. Hard cap is 4096."},
			{Name: "mpf", Type: "int", Default: "0",
				Description: "Max match records per file; 0 is unlimited.",
				Long:        "Cap on records per file. 0 means unlimited."},
			{Name: "cb", Type: "int", Default: "0",
				Description: "Context lines before each match; max 50.",
				Long:        "Lines of context before each match. Max 50. Context lines are deduplicated across overlapping matches."},
			{Name: "ca", Type: "int", Default: "0",
				Description: "Context lines after each match; max 50.",
				Long:        "Lines of context after each match. Max 50. Context lines are deduplicated across overlapping matches."},
			{Name: "context", Type: "int", Default: "0",
				Description: "Symmetric context; sets both --cb and --ca.",
				Long:        "Symmetric context: sets both --cb and --ca. Per-direction flags take precedence when both are given."},
			{Name: "fo", Type: "bool", Default: "false",
				Description: "Return only paths of files with at least one match.",
				Long:        "Return only the paths of files containing at least one match."},
			{Name: "no-text", Type: "bool", Default: "false",
				Description: "Omit matched line text; render path:line:col only.",
				Long:        "Omit the matched line text from records; pretty form renders path:line:col only."},
			{Name: "depth", Type: "int", Default: "0",
				Description: "Max depth; 0 is unlimited."},
			{Name: "hidden", Type: "bool", Default: "false",
				Description: "Include directories starting with '.' when true.",
				Long:        "When false, directories starting with '.' are skipped."},
			{Name: "gi", Type: "bool", Default: "true",
				Description: "Apply .gitignore from the walk root.",
				Long:        "When true, .gitignore at the walk root (--path) is loaded and applied."},
			{Name: "absolute", Type: "bool", Default: "false",
				Description: "Emit absolute paths instead of repo-root-relative.",
				Long:        "When true, match paths are absolute. Default (false) strips the project-root prefix so output is repo-root-relative regardless of --path form (ASH-71)."},
		},
	},
	{
		Verb:        "git",
		Description: "Structured git calls via --op discriminator: status, log, diff, show.",
		Args: []ArgSchema{
			{Name: "op", Type: "string", Required: true, Values: []string{"status", "log", "diff", "show"},
				Description: "Subcommand to run."},
			{Name: "path", Type: "string", Default: ".",
				Description: "Any path inside the git worktree; output is repo-root-relative.",
				Long:        "Repository path (any path inside a git work tree). Note: returned file paths are always repo-root-relative regardless of how --path was passed. This departs from find/grep where paths mirror the --path form."},
			{Name: "untracked", Type: "bool", Default: "true", Ops: []string{"status"},
				Description: "Include untracked files."},
			{Name: "ignored", Type: "bool", Default: "false", Ops: []string{"status"},
				Description: "Include gitignored files."},
			{Name: "limit", Type: "int", Default: "20", Ops: []string{"log"},
				Description: "Max commits to return; hard cap 200.",
				Long:        "[log] maximum commits to return. Hard cap is 200."},
			{Name: "staged", Type: "bool", Default: "false", Ops: []string{"diff"},
				Description: "Diff index vs HEAD; default diffs worktree vs index.",
				Long:        "[diff] diff index vs HEAD (--cached). Default diffs worktree vs index."},
			{Name: "ref", Type: "string", Default: "", PH: "<rev>", Ops: []string{"show"},
				Description: "Commit ref for show: SHA, branch, tag, HEAD~1. Required.",
				Long:        "[show] commit ref to inspect (SHA, HEAD, HEAD~1, branch, tag, etc.). Required for show. Root commits diff against the empty tree."},
			{Name: "range", Type: "string", Default: "", PH: "<rev>", Ops: []string{"log", "diff"},
				Description: "Revision range (e.g. main..feature, HEAD~10..HEAD).",
				Long:        "[log/diff] git revision range (e.g. 'main..feature', 'HEAD~10..HEAD', or single commit 'HEAD~1')."},
			{Name: "author", Type: "string", Default: "", Ops: []string{"log"},
				Description: "Filter commits by author name or email substring."},
			{Name: "since", Type: "string", Default: "", Ops: []string{"log"},
				Description: "Only commits after this date; accepts git --since formats.",
				Long:        "[log] only commits after this date (any format git --since accepts, e.g. '1 week ago')."},
			{Name: "until", Type: "string", Default: "", Ops: []string{"log"},
				Description: "Only commits before this date."},
			{Name: "pathspec", Type: "string", Default: "", Ops: []string{"log", "diff", "show"},
				Description: "Restrict to a path, repo-root-relative, passed after --.",
				Long:        "[log/diff/show] restrict to a single path (passed after -- to git). Interpreted relative to the repo root, not relative to --path."},
			{Name: "stat", Type: "bool", Default: "false", Ops: []string{"diff", "show"},
				Description: "Return per-file add/delete counts only; no patch text.",
				Long:        "[diff/show] return per-file addition/deletion counts only (no patch text). Much cheaper in tokens."},
			{Name: "context", Type: "int", Default: "3", Ops: []string{"diff", "show"},
				Description: "Unified diff context lines; max 50."},
			{Name: "bytes", Type: "int", Default: "262144", Ops: []string{"diff", "show"},
				Description: "Max patch bytes; omits patch past cap, preserves stats. Max 4 MiB.",
				Long:        "[diff/show] cap on total patch bytes returned. Files beyond cap have patch omitted but stats preserved. Max 4 MiB."},
		},
	},
	{
		Verb:        "write",
		Description: "Write a file atomically; creates parent directories by default.",
		Args: []ArgSchema{
			{Name: "path", Type: "string", Required: true,
				Description: "File to write.",
				Long:        "File path to write. Absolute or relative to the daemon's project root."},
			{Name: "content", Type: "string", Required: true,
				Description: "File content; pass '-' to read from stdin.",
				Long:        "File content. UTF-8 text by default; base64-encoded bytes when encoding=base64. Pass '-' to read from stdin."},
			{Name: "encoding", Type: "string", Default: "utf-8", Values: []string{"utf-8", "base64"},
				Description: "Content encoding; use base64 for binary files."},
			{Name: "mkdir", Type: "bool", Default: "true",
				Description: "Create missing parent directories."},
			{Name: "no-clobber", Type: "bool", Default: "false",
				Description: "Fail with 'exists' if the file already exists.",
				Long:        "Fail with 'exists' error if the file already exists. Useful as a safety guard against accidental overwrites."},
		},
	},
	{
		Verb:        "metrics",
		Description: "Query recent call history from the ledger.",
		Args: []ArgSchema{
			{Name: "last", Type: "int", Default: "20",
				Description: "Most-recent calls to return; max 200.",
				Long:        "Number of most-recent calls to return. Maximum is 200."},
			{Name: "verb", Type: "string", Default: "",
				Description: "Filter to calls for a specific verb."},
		},
	},
	{
		Verb:        "report",
		Description: "Per-verb ledger summary: n, ok%, p50/p95 latency, tokens, trunc%.",
		Args: []ArgSchema{
			{Name: "session", Type: "string", Default: "current", Values: []string{"current", "all", "<id>"},
				Description: "Session scope: current, all, or explicit session ID.",
				Long:        "Session scope: 'current' (this daemon session), 'all', or an explicit session ID. With --root or --all, defaults to no session filter."},
			{Name: "since", Type: "string", Default: "",
				Description: "Time window (e.g. 15m, 1h, 7d); supports Go duration + d.",
				Long:        "Time window, e.g. '15m', '1h', '24h', '7d'. Supports Go duration syntax plus 'd' for days."},
			{Name: "last", Type: "int", Default: "",
				Description: "Row cap after session/since filters; max 5000.",
				Long:        "Row cap applied after session/since filters. Maximum is 5000."},
			{Name: "verb", Type: "string", Default: "",
				Description: "Restrict aggregation to calls for a specific verb."},
			{Name: "top", Type: "int", Default: "5",
				Description: "Max entries in truncation and error histogram sections.",
				Long:        "Max entries shown in truncation hotspots and error histogram sections. Maximum is 100."},
			{Name: "root", Type: "string", Default: "",
				Description: "Project root to query read-only; mutually exclusive with --all.",
				Long:        "Project root whose ledger to query (read-only). Mutually exclusive with --all. Use to analyze a target repo from outside it."},
			{Name: "all", Type: "bool", Default: "false",
				Description: "Aggregate across every installed repo; includes per-root breakdown.",
				Long:        "Aggregate across every root in the installed-repos registry. Pretty form includes a per-root breakdown."},
		},
	},
	{
		Verb:        "help",
		Description: "Return argument schema for one verb or all verbs.",
		Args: []ArgSchema{
			{Name: "verb", Type: "string", Default: "",
				Description: "Verb name to describe; omit for all verbs."},
			{Name: "verbose", Type: "bool", Default: "false",
				Description: "Show full arg descriptions (default: concise)."},
		},
	},
	{
		Verb:        "hook",
		Description: "PreToolUse decision engine; steers harness tools to ash equivalents.",
		Args: []ArgSchema{
			{Name: "tool", Type: "string",
				Description: "Harness tool the agent attempted.",
				Long:        "Harness tool the agent attempted (e.g. Grep, Bash, Edit, Write, Read)."},
			{Name: "command", Type: "string",
				Description: "Bash command line."},
			{Name: "pattern", Type: "string",
				Description: "Grep regex or Glob pattern."},
			{Name: "path", Type: "string",
				Description: "Grep/Glob search root or Read path."},
			{Name: "glob", Type: "string",
				Description: "Grep file glob filter."},
			{Name: "file", Type: "string",
				Description: "Read/Edit/Write target file path."},
			{Name: "old", Type: "string",
				Description: "Edit text to replace."},
			{Name: "new", Type: "string",
				Description: "Edit replacement text."},
			{Name: "content", Type: "string",
				Description: "Write new file content."},
		},
	},
	{
		Verb:        "stat",
		Description: "Return lstat metadata for one or more paths.",
		Args: []ArgSchema{
			{Name: "paths", Type: "string", PH: "<p1>[,<p2>...]",
				Description: "Comma-separated paths to inspect; one of --paths or --path required.",
				Long:        "Comma-separated list of paths to inspect (e.g. 'cmd/ash/main.go,internal/'). One of --paths or --path is required."},
			{Name: "path", Type: "string",
				Description: "Single-path alias for --paths; one of --paths or --path required.",
				Long:        "Single-path alias for --paths (e.g. --path cmd/ash/main.go). One of --paths or --path is required."},
			{Name: "follow", Type: "bool", Default: "false",
				Description: "Resolve symlinks; broken symlinks produce error=broken_symlink per entry.",
				Long:        "When true, resolve each symlink with os.Stat and report the target's type/size/mtime/mode. link_target is preserved for traceability. Broken symlinks produce error=broken_symlink rather than failing the call."},
		},
	},
	{
		Verb:        "edit",
		Description: "Atomically edit a file: string-replace, line-range, or patch mode.",
		Args: []ArgSchema{
			{Name: "path", Type: "string", Required: true,
				Description: "File to edit."},
			{Name: "old", Type: "string", Mode: "string",
				Description: "Exact text to find; must appear once unless all=true.",
				Long:        "[string mode] Exact text to find (required if range/patch not provided). Must appear exactly once unless all=true."},
			{Name: "new", Type: "string", Mode: "string", Default: "",
				Description: "Replacement text; empty string deletes the match.",
				Long:        "[string mode] Replacement text. Empty string deletes the matched text."},
			{Name: "all", Type: "bool", Mode: "string", Default: "false",
				Description: "Replace every occurrence; false errors if old appears multiple times.",
				Long:        "[string mode] Replace every occurrence of old. If false, errors when old appears more than once."},
			{Name: "range", Type: "string", Mode: "range",
				Description: "start:end line range to replace, 1-based inclusive.",
				Long:        "[range mode] Line range to replace, formatted as start:end, 1-based inclusive (required if old/patch not provided)."},
			{Name: "new", Type: "string", Mode: "range", Default: "",
				Description: "Replacement lines; empty string deletes the range.",
				Long:        "[range mode] Replacement text for the specified lines. Empty string deletes the lines."},
			{Name: "patch", Type: "string", Mode: "patch", PH: "<diff|->",
				Description: "Unified diff to apply; pass '-' to read from stdin.",
				Long:        "[patch mode] Unified diff to apply (required if old/range not provided). Pass '-' to read from stdin. Error codes: patch_parse_error, patch_failed."},
			{Name: "dry", Type: "bool", Default: "false",
				Description: "Compute the replacement without writing; result includes unified diff.",
				Long:        "Compute the replacement but do not write. Result includes a unified diff in the patch field."},
		},
	},
	{
		Verb:        "diff",
		Description: "Unified diff: file vs file or inline content, capped at 2000 lines.",
		Args: []ArgSchema{
			{Name: "path", Type: "string", Required: true,
				Description: "Before-side file (a)."},
			{Name: "other", Type: "string",
				Description: "After-side file (b); mutually exclusive with --content.",
				Long:        "Second file for the after (b) side. Mutually exclusive with content."},
			{Name: "content", Type: "string",
				Description: "Inline after-side text; pass '-' to read from stdin.",
				Long:        "Inline after-side text. Mutually exclusive with other. Pass '-' to read from stdin."},
			{Name: "context", Type: "int", Default: "3",
				Description: "Context lines per hunk; max 50."},
			{Name: "stat", Type: "bool", Default: "false",
				Description: "Return add/delete/unchanged counts only; no patch text.",
				Long:        "Return counts only (additions, deletions, unchanged). Omits patch text. Much cheaper in tokens when you only need to know if or how much changed."},
		},
	},
	{
		Verb:        "bench",
		Description: "Benchmark ash vs bash; report token/latency deltas per case.",
		Args: []ArgSchema{
			{Name: "verb", Type: "string", Default: "",
				Description: "Restrict to cases for one verb."},
			{Name: "case", Type: "string", Default: "",
				Description: "Run a single named case; overrides --verb.",
				Long:        "Run a single named case (e.g. 'grep_todo_repo'). Overrides --verb."},
			{Name: "limit", Type: "int", Default: "0",
				Description: "Cap cases after filters; 0 is no cap."},
			{Name: "repeat", Type: "int", Default: "1",
				Description: "Measured iterations per case; >1 enables p50 and min latency.",
				Long:        "Measured iterations per case per side. >1 enables p50 + min latency reporting; tokens are deterministic and pinned to the first measured iteration."},
			{Name: "warmup", Type: "int", Default: "0",
				Description: "Unmeasured warm-up iterations; defaults to 1 when repeat>1.",
				Long:        "Unmeasured iterations per case per side, run before measurement to prime filesystem cache and runtime state. Defaults to 1 when --repeat>1, 0 otherwise."},
			{Name: "list", Type: "bool", Default: "false",
				Description: "List recent persisted bench runs and exit."},
			{Name: "list_limit", Type: "int", Default: "20",
				Description: "Cap the --list output."},
			{Name: "compare", Type: "string", Default: "", PH: "<A,B>",
				Description: "Compare two persisted runs; 'latest' resolves to most recent.",
				Long:        "Compare two persisted runs by uuid (prefix accepted). Token 'latest' resolves to the most recent run. Output is a side-by-side per-case delta with REGRESS / IMPROVE flags."},
			{Name: "baseline", Type: "string", Default: "", PH: "<dur>",
				Description: "Run fresh bench, compare against rolling median over the window.",
				Long:        "Run a fresh bench, then compare against the per-case rolling median over the given window (e.g. 7d, 24h). Persists the fresh run."},
			{Name: "regress_tokens", Type: "int", Default: "10",
				Description: "Token delta % threshold for regression in --compare.",
				Long:        "Δtok% threshold above which a case counts as a regression in --compare output."},
			{Name: "regress_latency", Type: "int", Default: "20",
				Description: "Latency delta % threshold for regression in --compare.",
				Long:        "Δlat% threshold above which a case counts as a regression."},
			{Name: "record_baseline", Type: "bool", Default: "false",
				Description: "Run fresh bench; write baseline.json, baseline.md, latency-snapshot.json.",
				Long:        "Run a fresh bench, then write bench/baseline.json + bench/baseline.md + bench/latency-snapshot.json. The baseline.json file is the regression contract checked into the repo."},
			{Name: "export_md", Type: "bool", Default: "false",
				Description: "Render latest bench run as Markdown.",
				Long:        "Render the latest persisted bench run as Markdown. Pipe stdout into bench/baseline.md."},
		},
	},
	{
		Verb:        "test",
		Description: "Run Go tests; return structured per-test results.",
		Args: []ArgSchema{
			{Name: "packages", Type: "string", Default: "./...", PH: "<pkgs>",
				Description: "Comma-separated package patterns for go test.",
				Long:        "Comma-separated package patterns passed positionally to go test (e.g. ./...,internal/walker)."},
			{Name: "run", Type: "string", Default: "",
				Description: "Regex for go test -run; filters tests.",
				Long:        "Regex passed to go test -run; filters which tests execute."},
			{Name: "count", Type: "int", Default: "1",
				Description: "go test -count; 1 bypasses cache, 0 uses cache.",
				Long:        "Maps to go test -count. Default 1 bypasses the test cache (agents typically want fresh runs after editing). Pass 0 to use the cache."},
			{Name: "race", Type: "bool", Default: "false",
				Description: "Enable the race detector."},
			{Name: "short", Type: "bool", Default: "false",
				Description: "Enable -short mode."},
			{Name: "timeout", Type: "string", Default: "60s", PH: "<dur>",
				Description: "Timeout; also passed to go test -timeout.",
				Long:        "Go duration for the outer wall (context.WithTimeout). Also passed to go test -timeout (1s grace earlier) so go aborts cleanly first. CI-shaped suites can pass --timeout 10m."},
			{Name: "verbose", Type: "bool", Default: "false",
				Description: "Include passing test names per package.",
				Long:        "Render hint: include passing test names per package. Failure output is unconditional."},
			{Name: "bench", Type: "string", Default: "",
				Description: "go test -bench pattern; implies -run=^$ when --run is unset.",
				Long:        "Passed to go test -bench. When set, non-bench tests are suppressed (implicit -run=^$) unless --run is also given. E.g. --bench=BenchmarkFoo or --bench=."},
			{Name: "benchmem", Type: "bool", Default: "false",
				Description: "go test -benchmem: report memory allocations."},
			{Name: "benchtime", Type: "string", Default: "",
				Description: "go test -benchtime duration (e.g. '1s', '100x').",
				Long:        "Passed as -benchtime to go test. Use the 'Nx' form for a fixed iteration count (e.g. '1x' for a single pass) or a duration like '2s'."},
		},
	},
	{
		Verb:        "init",
		Description: "Bootstrap a repo for ash: hook, gitignore, CLAUDE.md, registry. Idempotent.",
		Args: []ArgSchema{
			{Name: "path", Type: "string", Default: ".",
				Description: "Target repo root; default '.' is the daemon's project root.",
				Long:        "Target repo root (absolute or relative). Default . uses the daemon's project root."},
			{Name: "force", Type: "bool", Default: "false",
				Description: "Overwrite conflicting PreToolUse or CLAUDE.md sections.",
				Long:        "Replace an existing PreToolUse entry that invokes ash with a different command, or replace an existing CLAUDE.md/AGENTS.md ash-managed section whose content differs from the current template. Without --force a conflict produces a warning and no change."},
			{Name: "no-registry", Type: "bool", Default: "false",
				Description: "Skip writing the installed-repos registry.",
				Long:        "Skip writing the installed-repos registry. Useful for ephemeral test repos."},
		},
	},
	{
		Verb:        "uninit",
		Description: "Reverse ash init: remove hook, gitignore entry, CLAUDE.md section, registry.",
		Args: []ArgSchema{
			{Name: "path", Type: "string", Default: ".",
				Description: "Target repo root."},
			{Name: "no-registry", Type: "bool", Default: "false",
				Description: "Skip the registry removal."},
		},
	},
	{
		Verb:        "stop",
		Description: "Stop the daemon cleanly (SIGTERM, 7s). Idempotent; next call auto-restarts.",
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
	if a.Verbose, perr = argutil.OptionalBool(in, "verbose", false); perr != nil {
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
	verbose := false
	if req != nil {
		if v, ok := req.Args["verbose"]; ok {
			switch vt := v.(type) {
			case bool:
				verbose = vt
			case string:
				verbose = vt == "true" || vt == "1"
			}
		}
	}
	var b strings.Builder
	if r.Count != 1 {
		fmt.Fprintf(&b, "=== ash help: %d verb(s) — use `ash help <verb>` for full schema ===\n", r.Count)
		for _, vs := range r.Verbs {
			fmt.Fprintf(&b, "  %-*s  %s\n", verbNameW, vs.Verb, vs.Description)
		}
		return strings.TrimRight(b.String(), "\n")
	}
	fmt.Fprintf(&b, "=== ash help: %d verb(s) ===\n", r.Count)
	for i, vs := range r.Verbs {
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "verb: %s\n", vs.Verb)
		fmt.Fprintf(&b, "  %s\n", vs.Description)
		for _, arg := range vs.Args {
			writeArg(&b, arg, verbose)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func writeArg(b *strings.Builder, a ArgSchema, verbose bool) {
	sig := "--" + a.Name + ":" + a.Type
	if a.Required {
		sig += "!"
	} else if a.Default != "" {
		sig += "=" + a.Default
	}
	desc := a.Description
	if verbose && a.Long != "" {
		desc = a.Long
	}
	if len(a.Values) > 0 {
		desc += " [" + strings.Join(a.Values, "|") + "]"
	}
	fmt.Fprintf(b, "  %s — %s\n", sig, desc)
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
		case "new", "old":
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
			if a.Name == "dry" {
				continue // dry appended last
			}
			tokens = append(tokens, argToken(a))
		}
		for _, a := range args {
			tokens = append(tokens, argToken(a))
		}
		// dry_run is always last
		for _, a := range sharedArgs {
			if a.Name == "dry" {
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
