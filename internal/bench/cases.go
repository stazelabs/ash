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

import (
	"os"
	"path/filepath"
	"strings"
)

// Case is one bench scenario. Verb + AshArgs are dispatched in-process
// against the live runner registry; BashArgv is constructed by translate
// and run in a sandboxed subprocess.
type Case struct {
	Name    string         // short, unique identifier (e.g. "find_go_files")
	Verb    string         // ash verb to dispatch
	AshArgs map[string]any // wire-shape args for the verb
	Why     string         // hypothesis this case tests; surfaces in pretty output
	// Setup is optional. When non-nil, it runs before each iteration
	// (warmup or measured, both sides) to reset whatever state the case
	// mutates. Used by write/edit cases to guarantee a known starting
	// content. Errors are logged but not fatal.
	Setup func() error
}

// Cases is the canonical case list. Add cases sparingly — each one is
// part of the project's ongoing self-measurement, so churn here implies
// trend lines that don't compare across runs.
var Cases = []Case{
	// --- find ---
	{
		Name:    "find_shallow",
		Verb:    "find",
		AshArgs: map[string]any{"path": ".", "depth": "1"},
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
	{
		Name:    "find_go_files_absolute",
		Verb:    "find",
		AshArgs: map[string]any{"path": "{root}", "glob": "**/*.go"},
		Why:     "absolute --path exercises ASH-71 repo-root strip on each Record.Path; ash_tokens match the relative sibling but bash balloons, widening Δtok%",
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
		Name:    "grep_parseargs_absolute",
		Verb:    "grep",
		AshArgs: map[string]any{"pattern": "ParseArgs", "path": "{root}/internal"},
		Why:     "absolute --path exercises ASH-71 repo-root strip on Match.Path; ash_tokens match the relative sibling but bash balloons, widening Δtok%",
	},
	{
		Name:    "grep_files_only",
		Verb:    "grep",
		AshArgs: map[string]any{"pattern": "Run", "path": ".", "fo": "true"},
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
		AshArgs: map[string]any{"path": "README.md", "range": "1:50", "unit": "lines"},
		Why:     "line range; bash equivalent is sed -n '1,50p'",
	},
	{
		Name:    "read_tiny_range",
		Verb:    "read",
		AshArgs: map[string]any{"path": "go.mod", "range": "1:5", "unit": "lines"},
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

	// --- write ---
	{
		Name:    "write_small",
		Verb:    "write",
		AshArgs: map[string]any{"path": BenchTmpDir + "/write_small.txt", "content": "hello\nworld\n"},
		Setup:   ensureBenchTmpDir,
		Why:     "small file write; bash equivalent is sh -c 'cat > F << EOF…' — atomic-write overhead vs naive redirect",
	},

	// --- edit ---
	{
		Name:    "edit_string_replace",
		Verb:    "edit",
		AshArgs: map[string]any{"path": BenchTmpDir + "/edit_target.txt", "old": "FOO", "new": "BAR"},
		Setup:   func() error { return writeFixture("edit_target.txt", "FOO bar baz\n") },
		Why:     "string-replacement mode; bash equivalent is sed -i — exercises ambiguity-detection vs sed silent-replace",
	},

	// --- diff ---
	{
		Name:    "diff_two_files",
		Verb:    "diff",
		AshArgs: map[string]any{"path": "README.md", "other": "CLAUDE.md"},
		Why:     "two checked-in files; bash equivalent is `diff README.md CLAUDE.md`",
	},
	{
		Name:    "diff_stat_only",
		Verb:    "diff",
		AshArgs: map[string]any{"path": "README.md", "other": "CLAUDE.md", "stat": "true"},
		Why:     "stat-only diff; ash should be a clear win when the caller only wants counts",
	},
}


// ExpandArgs returns a copy of args with the literal placeholder
// "{root}" replaced by root in every string-valued entry. Used by the
// bench runner to express absolute-path cases without baking a
// host-specific path into the static case list. The case-set hash
// reads the raw (pre-expansion) values, so a placeholder-using case
// is stable across machines.
func ExpandArgs(args map[string]any, root string) map[string]any {
	out := make(map[string]any, len(args))
	for k, v := range args {
		if s, ok := v.(string); ok {
			out[k] = strings.ReplaceAll(s, "{root}", root)
		} else {
			out[k] = v
		}
	}
	return out
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

// MeasuredVerbs are the bench-meaningful verbs: each must have at least
// one entry in Cases. The coverage test in
// internal/verbs/bench/coverage_test.go enforces this. Update both this
// list and Cases when a new bench-meaningful verb ships.
var MeasuredVerbs = []string{
	"read", "write", "edit", "diff", "find", "grep", "git", "stat",
}

// ExemptVerbs are verbs the harness deliberately does not measure.
// Each entry has a one-line justification that survives in code review.
var ExemptVerbs = map[string]string{
	"metrics": "reads ledger; size depends on session, no honest bash equivalent",
	"report":  "reads ledger; size depends on session",
	"help":    "no honest bash equivalent; token budget enforced by TestNoArgTokenBudget in internal/verbs/help",
	"init":    "one-shot setup; mutates files",
	"uninit":  "one-shot teardown; mutates files",
	"stop":    "kills daemon",
	"hook":    "the redirector under test; circular",
	"bench":   "recursive",
	"test":    "no honest bash equivalent at the verb level",
	"replay":  "reads ledger and re-dispatches verbs; no honest bash equivalent",
	"recap":     "reads ledger; session-graph summary, no honest bash equivalent",
	"workspace": "reads ledger + git status; re-orientation snapshot, no honest bash equivalent",
	"usage":     "annotates the ledger row of a prior call; no bash equivalent, and the call is trivial (one UPDATE)",
	"lang":      "LSP-broker semantic queries; no bash equivalent (gopls round-trip vs grep+read sequences is exactly what ASH-141 is set up to measure independently)",
}

// BenchTmpDir is the working directory cases use for write/edit
// fixtures. Lives under .ash/ which is gitignored. Created by
// ensureBenchTmpDir before each iteration; deleted by
// CleanupBenchTmpDir after a bench run.
const BenchTmpDir = ".ash/bench-tmp"

func ensureBenchTmpDir() error {
	return os.MkdirAll(BenchTmpDir, 0o755)
}

func writeFixture(rel, content string) error {
	full := filepath.Join(BenchTmpDir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	return os.WriteFile(full, []byte(content), 0o644)
}

// CleanupBenchTmpDir removes BenchTmpDir and its contents. Best-effort.
func CleanupBenchTmpDir() error {
	return os.RemoveAll(BenchTmpDir)
}
