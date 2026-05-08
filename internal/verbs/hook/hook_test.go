package hook

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDecide_tools(t *testing.T) {
	cases := []struct {
		name     string
		args     *Args
		want     string // "allow" | "deny"
		wantRule string // optional substring match on MatchedRule
		wantSugg string // optional substring match on Suggested
	}{
		// Harness tools: always denied (modulo Read exemptions).
		{
			name:     "Grep tool always denies",
			args:     &Args{ToolName: "Grep", Pattern: "foo", Path: "."},
			want:     "deny",
			wantRule: "Grep",
			wantSugg: "ash grep --pattern foo --path .",
		},
		{
			name:     "Glob tool denies with ash find suggestion",
			args:     &Args{ToolName: "Glob", Pattern: "**/*.go", Path: "internal/"},
			want:     "deny",
			wantRule: "Glob",
			wantSugg: "ash find --path internal/",
		},
		{
			name:     "Edit tool denies with ash edit suggestion",
			args:     &Args{ToolName: "Edit", FilePath: "main.go", OldString: "old", NewString: "new"},
			want:     "deny",
			wantRule: "Edit",
			wantSugg: "ash edit --path main.go",
		},
		{
			name:     "Write tool denies with ash write suggestion",
			args:     &Args{ToolName: "Write", FilePath: "new.go", Content: "package main"},
			want:     "deny",
			wantRule: "Write",
			wantSugg: "ash write --path new.go",
		},

		// Read: source-text denied, media allowed.
		{
			name:     "Read .go denies",
			args:     &Args{ToolName: "Read", FilePath: "internal/foo.go"},
			want:     "deny",
			wantRule: "Read",
			wantSugg: "ash read --path internal/foo.go",
		},
		{
			name:     "Read .png allows",
			args:     &Args{ToolName: "Read", FilePath: "screenshot.png"},
			want:     "allow",
			wantRule: "Read:.png-allow",
		},
		{
			name:     "Read .pdf allows",
			args:     &Args{ToolName: "Read", FilePath: "report.pdf"},
			want:     "allow",
			wantRule: "Read:.pdf-allow",
		},
		{
			name:     "Read .ipynb allows",
			args:     &Args{ToolName: "Read", FilePath: "notebook.ipynb"},
			want:     "allow",
			wantRule: "Read:.ipynb-allow",
		},
		{
			name:     "Read .JPEG (case-insensitive) allows",
			args:     &Args{ToolName: "Read", FilePath: "PHOTO.JPEG"},
			want:     "allow",
			wantRule: "Read:.jpeg-allow",
		},

		// Unrecognized tool → allow.
		{
			name: "Unknown tool allows",
			args: &Args{ToolName: "TodoWrite"},
			want: "allow",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := Decide(tc.args)
			if r.Decision != tc.want {
				t.Fatalf("decision: want %q, got %q (rule=%q, suggested=%q)",
					tc.want, r.Decision, r.MatchedRule, r.Suggested)
			}
			if tc.wantRule != "" && !strings.Contains(r.MatchedRule, tc.wantRule) {
				t.Errorf("matched_rule: want substring %q, got %q", tc.wantRule, r.MatchedRule)
			}
			if tc.wantSugg != "" && !strings.Contains(r.Suggested, tc.wantSugg) {
				t.Errorf("suggested: want substring %q, got %q", tc.wantSugg, r.Suggested)
			}
		})
	}
}

func TestDecide_bash(t *testing.T) {
	cases := []struct {
		name     string
		command  string
		want     string
		wantRule string
		wantSugg string
	}{
		// Each redirected program.
		{name: "grep", command: "grep -r foo .", want: "deny", wantRule: "Bash:grep", wantSugg: "ash grep"},
		{name: "rg", command: "rg --hidden TODO src/", want: "deny", wantRule: "Bash:rg", wantSugg: "ash grep"},
		{name: "egrep", command: "egrep TODO .", want: "deny", wantRule: "Bash:egrep"},
		{name: "fgrep", command: "fgrep foo bar.txt", want: "deny", wantRule: "Bash:fgrep"},
		{name: "find", command: "find . -name '*.go'", want: "deny", wantRule: "Bash:find", wantSugg: "ash find"},
		{name: "find -iname", command: "find internal -iname '*ledger*'", want: "deny", wantRule: "Bash:find", wantSugg: "*ledger*"},
		{name: "cat", command: "cat main.go", want: "deny", wantRule: "Bash:cat", wantSugg: "ash read --path main.go"},
		{name: "head", command: "head -n 20 main.go", want: "deny", wantRule: "Bash:head"},
		{name: "tail", command: "tail -f log.txt", want: "deny", wantRule: "Bash:tail"},
		{name: "ls -R", command: "ls -R internal/", want: "deny", wantRule: "Bash:ls-R"},
		{name: "ls --recursive", command: "ls --recursive .", want: "deny", wantRule: "Bash:ls-R"},
		{name: "ls non-recursive allows", command: "ls -la", want: "allow"},
		{name: "stat", command: "stat foo.go bar.go", want: "deny", wantRule: "Bash:stat", wantSugg: "ash stat"},
		{name: "git status", command: "git status", want: "deny", wantRule: "Bash:git-status", wantSugg: "ash git --op status"},
		{name: "git log", command: "git log -n 5", want: "deny", wantRule: "Bash:git-log"},
		{name: "git diff allows", command: "git diff", want: "allow"},
		{name: "git blame allows", command: "git blame foo.go", want: "allow"},
		{name: "git commit allows", command: "git commit -m 'msg'", want: "allow"},

		// Allow paths.
		{name: "go build allows", command: "go build ./...", want: "allow"},
		{name: "go test allows", command: "go test ./internal/...", want: "allow"},
		{name: "gh pr list allows", command: "gh pr list", want: "allow"},
		{name: "empty command allows", command: "", want: "allow"},
		{name: "whitespace command allows", command: "   ", want: "allow"},

		// Chained commands — first denied segment wins.
		{name: "chained ; with denied second", command: "echo hi; cat foo", want: "deny", wantRule: "Bash:cat"},
		{name: "chained && with denied", command: "go build && cat result", want: "deny", wantRule: "Bash:cat"},
		{name: "chained || with denied", command: "test -f foo || cat foo", want: "deny", wantRule: "Bash:cat"},
		{name: "pipe with denied LHS", command: "cat foo.go | wc -l", want: "deny", wantRule: "Bash:cat"},
		{name: "all-allowed chain stays allow", command: "go build && go test ./... ; gh pr list", want: "allow"},

		// Prefix stripping: VAR=val and env/command/exec/time/nice.
		{name: "VAR= prefix", command: "FOO=bar grep pattern .", want: "deny", wantRule: "Bash:grep"},
		{name: "env prefix", command: "env FOO=bar grep pattern .", want: "deny", wantRule: "Bash:grep"},
		{name: "command prefix", command: "command grep pattern .", want: "deny", wantRule: "Bash:grep"},
		{name: "time prefix", command: "time cat foo.go", want: "deny", wantRule: "Bash:cat"},
		{name: "absolute path program", command: "/usr/bin/grep -r foo .", want: "deny", wantRule: "Bash:grep"},

		// Nudge tail is appended.
		{name: "deny includes nudge tail", command: "grep foo .", want: "deny", wantRule: "Bash:grep"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := Decide(&Args{ToolName: "Bash", Command: tc.command})
			if r.Decision != tc.want {
				t.Fatalf("command %q: want %q, got %q (rule=%q)", tc.command, tc.want, r.Decision, r.MatchedRule)
			}
			if tc.wantRule != "" && r.MatchedRule != tc.wantRule {
				t.Errorf("matched_rule: want %q, got %q", tc.wantRule, r.MatchedRule)
			}
			if tc.wantSugg != "" && !strings.Contains(r.Suggested, tc.wantSugg) {
				t.Errorf("suggested: want substring %q, got %q", tc.wantSugg, r.Suggested)
			}
			if r.Decision == "deny" && !strings.Contains(r.Reason, "session-notes") {
				t.Errorf("deny reason should include nudge tail: %q", r.Reason)
			}
		})
	}
}

func TestExtractArgs_roundtrip(t *testing.T) {
	payload := []byte(`{
		"tool_name": "Grep",
		"tool_input": {"pattern": "foo bar", "path": "internal", "include": "*.go"}
	}`)
	wire, args, err := ExtractArgs(payload)
	if err != nil {
		t.Fatal(err)
	}
	if args.ToolName != "Grep" || args.Pattern != "foo bar" || args.Path != "internal" {
		t.Errorf("args mismatch: %+v", args)
	}
	if args.Glob != "*.go" {
		t.Errorf("glob fallback to 'include' failed: %q", args.Glob)
	}
	// Wire map should round-trip through ParseArgs.
	a2, perr := ParseArgs(wire)
	if perr != nil {
		t.Fatalf("ParseArgs: %v", perr)
	}
	if a2.Pattern != args.Pattern || a2.Glob != args.Glob {
		t.Errorf("wire round-trip mismatch: a1=%+v a2=%+v", args, a2)
	}
}

func TestExtractArgs_bash(t *testing.T) {
	payload := []byte(`{"tool_name": "Bash", "tool_input": {"command": "grep -r foo ."}}`)
	wire, args, err := ExtractArgs(payload)
	if err != nil {
		t.Fatal(err)
	}
	if args.Command != "grep -r foo ." {
		t.Errorf("command: %q", args.Command)
	}
	if wire["command"] != "grep -r foo ." {
		t.Errorf("wire command: %q", wire["command"])
	}
}

func TestExtractArgs_invalidJSON(t *testing.T) {
	_, _, err := ExtractArgs([]byte(`not json`))
	if err == nil {
		t.Error("expected error on malformed JSON")
	}
}

func TestEncodeClaudeDecision(t *testing.T) {
	// Allow → no output.
	out, err := EncodeClaudeDecision(&Result{Decision: "allow"})
	if err != nil {
		t.Fatal(err)
	}
	if out != nil {
		t.Errorf("allow should produce nil output, got %s", out)
	}

	// Deny → Claude-shape JSON with the reason.
	out, err = EncodeClaudeDecision(&Result{
		Decision: "deny",
		Reason:   "Use ash instead: `ash grep --pattern foo --path .`. " + nudgeTail,
	})
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	hso, ok := decoded["hookSpecificOutput"].(map[string]any)
	if !ok {
		t.Fatalf("missing hookSpecificOutput: %v", decoded)
	}
	if hso["hookEventName"] != "PreToolUse" {
		t.Errorf("hookEventName: %v", hso["hookEventName"])
	}
	if hso["permissionDecision"] != "deny" {
		t.Errorf("permissionDecision: %v", hso["permissionDecision"])
	}
	reason, _ := hso["permissionDecisionReason"].(string)
	if !strings.Contains(reason, "ash grep") || !strings.Contains(reason, "session-notes") {
		t.Errorf("reason: %q", reason)
	}
}

func TestParseArgs_emptyMap(t *testing.T) {
	// Empty args is valid (decision falls through to "allow" on unknown tool).
	a, perr := ParseArgs(map[string]any{})
	if perr != nil {
		t.Fatal(perr)
	}
	if a.ToolName != "" {
		t.Errorf("expected empty ToolName, got %q", a.ToolName)
	}
}

func TestShellquote(t *testing.T) {
	cases := map[string]string{
		"":             "''",
		"foo":          "foo",
		"foo.go":       "foo.go",
		"a/b/c":        "a/b/c",
		"foo bar":      `'foo bar'`,
		"can't":        `'can'"'"'t'`,
		"a&b":          `'a&b'`,
		"a*b":          `'a*b'`,
	}
	for in, want := range cases {
		if got := shellquote(in); got != want {
			t.Errorf("shellquote(%q): want %q, got %q", in, want, got)
		}
	}
}

func TestRun_wrapsDecide(t *testing.T) {
	// Run is just Decide with the verb-runner signature; sanity check.
	r, perr := Run(&Args{ToolName: "Grep", Pattern: "x"}, nil)
	if perr != nil {
		t.Fatal(perr)
	}
	if r.Decision != "deny" || r.MatchedRule != "Grep" {
		t.Errorf("run(grep): %+v", r)
	}
}
