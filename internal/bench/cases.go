// Package bench holds the canonical case list and the bash/ash plumbing
// used by the `ash bench` verb to compare ash output against the bash
// equivalent the agent would otherwise have run.
//
// Cases are static Go data — no TOML/JSON dep — so they ship with the
// binary and stay in lockstep with the verbs they exercise. The set is
// deliberately small and representative: a handful of cases per verb
// covering both the cases where ash should win big (truncation-prone
// grep/find) and the cases where ash may be breakeven or worse (small
// reads where the JSON envelope can exceed the raw bash output).
package bench

// Case is one bench scenario. Verb + AshArgs are dispatched in-process
// against the live runner registry; BashArgv is constructed by translate
// and run in a sandboxed subprocess.
type Case struct {
	Name    string         // short, unique identifier (e.g. "find_go_files")
	Verb    string         // ash verb to dispatch
	AshArgs map[string]any // wire-shape args for the verb
	Why     string         // hypothesis this case tests; surfaces in pretty output
}

// Cases is the canonical case list. Add cases sparingly — each one is
// part of the project's ongoing self-measurement, so churn here implies
// trend lines that don't compare across runs.
var Cases = []Case{
	// --- find ---
	{
		Name:    "find_shallow",
		Verb:    "find",
		AshArgs: map[string]any{"path": ".", "max_depth": "1"},
		Why:     "shallow walk, small result — tests JSON envelope overhead vs raw bash",
	},
	{
		Name:    "find_go_files",
		Verb:    "find",
		AshArgs: map[string]any{"path": ".", "glob": "**/*.go"},
		Why:     "moderate result; ash skips .git/.ash/bin via hidden-dir + gitignore defaults",
	},
	{
		Name:    "find_md_in_docs",
		Verb:    "find",
		AshArgs: map[string]any{"path": "docs", "glob": "**/*.md"},
		Why:     "narrow scope, small known result — control case",
	},

	// --- grep ---
	{
		Name:    "grep_todo_repo",
		Verb:    "grep",
		AshArgs: map[string]any{"pattern": "TODO", "path": "."},
		Why:     "common pattern across repo; ash skips binary + gitignore'd",
	},
	{
		Name:    "grep_parseargs_internal",
		Verb:    "grep",
		AshArgs: map[string]any{"pattern": "ParseArgs", "path": "internal"},
		Why:     "moderate match count, narrow scope",
	},
	{
		Name:    "grep_files_only",
		Verb:    "grep",
		AshArgs: map[string]any{"pattern": "Run", "path": ".", "files_only": "true"},
		Why:     "files-only output should be tight on both sides",
	},
	{
		Name:    "grep_rare_pattern",
		Verb:    "grep",
		AshArgs: map[string]any{"pattern": "ash bench:", "path": "."},
		Why:     "rare pattern; both sides return ~nothing, JSON envelope penalty visible",
	},
	{
		Name:    "grep_heavy_func_internal",
		Verb:    "grep",
		AshArgs: map[string]any{"pattern": "func", "path": "internal"},
		Why:     "many-match query; ash truncates at default max_matches=256 with a narrow-this hint while bash dumps every line — the load-bearing token-savings case",
	},

	// --- read ---
	{
		Name:    "read_small",
		Verb:    "read",
		AshArgs: map[string]any{"path": "README.md"},
		Why:     "small file; bash `cat` is essentially raw bytes — expect ash ~= bash",
	},
	{
		Name:    "read_range",
		Verb:    "read",
		AshArgs: map[string]any{"path": "README.md", "range": "1:50", "range_kind": "lines"},
		Why:     "line range; bash equivalent is sed -n '1,50p'",
	},
	{
		Name:    "read_tiny_range",
		Verb:    "read",
		AshArgs: map[string]any{"path": "go.mod", "range": "1:5", "range_kind": "lines"},
		Why:     "small range read where the lean header dominates relative cost — exercises the --with_meta opt-out path",
	},

	// --- git ---
	{
		Name:    "git_status",
		Verb:    "git",
		AshArgs: map[string]any{"op": "status"},
		Why:     "structured status vs porcelain string — ash JSON adds shape, may add tokens",
	},
	{
		Name:    "git_log_20",
		Verb:    "git",
		AshArgs: map[string]any{"op": "log", "limit": "20"},
		Why:     "structured commits with full SHAs and bodies — bash log is more compact prose",
	},

	// --- stat ---
	{
		Name:    "stat_single",
		Verb:    "stat",
		AshArgs: map[string]any{"paths": "README.md"},
		Why:     "single path; bash `stat` is verbose by default, ash is structured",
	},
	{
		Name:    "stat_bulk",
		Verb:    "stat",
		AshArgs: map[string]any{"paths": "README.md,CLAUDE.md,go.mod"},
		Why:     "bulk paths in one call; the kind of pre-read sizing ash stat is for",
	},
}

// FindCase returns the case with the given name, or nil if not found.
func FindCase(name string) *Case {
	for i := range Cases {
		if Cases[i].Name == name {
			return &Cases[i]
		}
	}
	return nil
}
