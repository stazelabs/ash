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
		{name: "git diff", command: "git diff", want: "deny", wantRule: "Bash:git-diff", wantSugg: "ash git --op diff"},
		{name: "git diff staged", command: "git diff --staged", want: "deny", wantRule: "Bash:git-diff"},
		{name: "git show", command: "git show HEAD", want: "deny", wantRule: "Bash:git-show", wantSugg: "ash git --op show"},
		{name: "git blame allows", command: "git blame foo.go", want: "allow"},
		{name: "git commit allows", command: "git commit -m 'msg'", want: "allow"},

		// Allow paths.
		{name: "go build allows", command: "go build ./...", want: "allow"},
		{name: "go test", command: "go test ./internal/...", want: "deny", wantRule: "Bash:go-test", wantSugg: "ash test --packages ./internal/..."},
		{name: "go test no args", command: "go test", want: "deny", wantRule: "Bash:go-test", wantSugg: "ash test"},
		{name: "go vet allows", command: "go vet ./...", want: "allow"},
		{name: "gh pr list allows", command: "gh pr list", want: "allow"},
		{name: "empty command allows", command: "", want: "allow"},
		{name: "whitespace command allows", command: "   ", want: "allow"},

		// Chained commands — first denied segment wins.
		{name: "chained ; with denied second", command: "echo hi; cat foo", want: "deny", wantRule: "Bash:cat"},
		{name: "chained && with denied", command: "go build && cat result", want: "deny", wantRule: "Bash:cat"},
		{name: "chained || with denied", command: "test -f foo || cat foo", want: "deny", wantRule: "Bash:cat"},
		{name: "pipe with denied LHS", command: "cat foo.go | wc -l", want: "deny", wantRule: "Bash:cat"},
		{name: "all-allowed chain stays allow", command: "go build && gh pr list", want: "allow"},
		// ASH-19: quote-aware segmentation — prose inside quotes must not trigger redirects.
		{name: "commit msg grep in double-quoted arg allows", command: `git commit -m "case; grep verb"`, want: "allow"},
		{name: "commit msg grep in single-quoted arg allows", command: `git commit -m 'case; grep verb'`, want: "allow"},
		{name: "commit msg grep in cmd subst allows", command: "git commit -m \"$(cat <<'EOF'\nfoo; grep bar\nEOF\n)\"", want: "allow"},
		// True-positive: operator outside quotes still denies.
		{name: "semicolon outside quotes still denies", command: "echo hello; grep foo .", want: "deny", wantRule: "Bash:grep"},

		// Prefix stripping: VAR=val and env/command/exec/time/nice.
		{name: "VAR= prefix", command: "FOO=bar grep pattern .", want: "deny", wantRule: "Bash:grep"},
		{name: "env prefix", command: "env FOO=bar grep pattern .", want: "deny", wantRule: "Bash:grep"},
		{name: "command prefix", command: "command grep pattern .", want: "deny", wantRule: "Bash:grep"},
		{name: "time prefix", command: "time cat foo.go", want: "deny", wantRule: "Bash:cat"},
		{name: "absolute path program", command: "/usr/bin/grep -r foo .", want: "deny", wantRule: "Bash:grep"},

		// ASH-48: quoted program names must not bypass deny-list lookup.
		{name: "double-quoted grep", command: `"grep" foo bar`, want: "deny", wantRule: "Bash:grep"},
		{name: "single-quoted grep", command: "grep foo bar", want: "deny", wantRule: "Bash:grep"},
		{name: "backslash-escaped grep", command: `\grep foo bar`, want: "deny", wantRule: "Bash:grep"},
		{name: "double-quoted find", command: `"find" . -name "*.go"`, want: "deny", wantRule: "Bash:find"},
		{name: "double-quoted cat", command: `"cat" main.go`, want: "deny", wantRule: "Bash:cat"},
		{name: "quoted env prefix then grep", command: `"env" FOO=bar grep .`, want: "deny", wantRule: "Bash:grep"},

		// Nudge tail is appended.
		{name: "deny includes nudge tail", command: "grep foo .", want: "deny", wantRule: "Bash:grep"},

		// Heredoc bodies are dropped before segmenting — content inside the
		// body (markdown tables, prose mentions of redirected programs, real
		// bash operators) must not produce false segments or false denies.
		// Tests use ash write as the host command since it is not on the
		// deny list and matches the actual real-world failure pattern.
		{name: "heredoc body with markdown table allows",
			command: "ash write --path docs/foo.md --content - <<'DOC_EOF'\n| find | path |\n| grep | path |\nDOC_EOF",
			want: "allow"},
		{name: "unquoted heredoc body with operators allows",
			command: "ash write --path /tmp/x --content - <<EOF\necho a; grep b .\nEOF",
			want: "allow"},
		{name: "double-quoted heredoc delim allows",
			command: "ash write --path /tmp/x --content - <<\"EOF\"\n| find | x |\nEOF",
			want: "allow"},
		{name: "strip-tabs heredoc allows",
			command: "ash write --path /tmp/x --content - <<-EOF\n\t| find |\n\tEOF",
			want: "allow"},
		{name: "command after heredoc terminator still parsed",
			command: "ash write --path /tmp/x --content - <<EOF\nfoo\nEOF\ngrep . .",
			want: "deny", wantRule: "Bash:grep"},
		{name: "unterminated heredoc allows without crash",
			command: "ash write --path /tmp/x --content - <<EOF\nbody body body",
			want: "allow"},
		{name: "heredoc inside cmd subst still allows (existing behavior preserved)",
			command: "git commit -m \"$(cat <<'EOF'\nfoo; grep bar\nEOF\n)\"",
			want: "allow"},
		{name: "space before single-quoted delimiter allows (ASH-68)",
			command: "tee /tmp/out << 'EOF'\ngrep pattern .\nEOF",
			want: "allow"},
		{name: "space before double-quoted delimiter allows (ASH-68)",
			command: "ash write --path /tmp/x --content - << \"EOF\"\n| find | x |\nEOF",
			want: "allow"},
		{name: "space before unquoted delimiter allows (ASH-68)",
			command: "ash write --path /tmp/x --content - << EOF\ngrep foo .\nEOF",
			want: "allow"},
		{name: "space before strip-tabs delimiter allows (ASH-68)",
			command: "ash write --path /tmp/x --content - <<- 'EOF'\n\tgrep foo .\n\tEOF",
			want: "allow"},

		// ASH-69: redirection operators must not pollute suggestion paths,
		// and cat/echo/printf/tee + > should route to ash write rather
		// than ash read.
		{name: "cat > FILE << EOF redirects to ash write",
			command: "cat > foo.txt << 'EOF'\nhi\nEOF",
			want: "deny", wantRule: "Bash:redirect-write",
			wantSugg: "ash write --path foo.txt --content - << 'EOF'"},
		{name: "echo > FILE redirects to ash write",
			command: `echo "hello" > foo.txt`,
			want: "deny", wantRule: "Bash:redirect-write",
			wantSugg: "ash write --path foo.txt"},
		{name: "printf > FILE redirects to ash write",
			command: `printf "line1\n" > out.txt`,
			want: "deny", wantRule: "Bash:redirect-write",
			wantSugg: "ash write --path out.txt"},
		{name: "tee with output redirect routes to ash write",
			command: "tee >> log.txt",
			want: "deny", wantRule: "Bash:redirect-write",
			wantSugg: "ash write --path log.txt"},
		{name: "cat foo > bar disambiguates to ash write (no malformed >)",
			command: "cat foo.txt > bar.txt",
			want: "deny", wantRule: "Bash:redirect-write",
			wantSugg: "ash write --path bar.txt"},
		{name: "cat foo > /dev/null produces non-malformed write suggestion",
			command: "cat foo.txt > /dev/null",
			want: "deny", wantRule: "Bash:redirect-write",
			wantSugg: "ash write --path /dev/null"},
		{name: "glued > suggests write",
			command: "echo hi >out.txt",
			want: "deny", wantRule: "Bash:redirect-write",
			wantSugg: "ash write --path out.txt"},
		{name: "&> redirects also write",
			command: "echo hi &> out.txt",
			want: "deny", wantRule: "Bash:redirect-write",
			wantSugg: "ash write --path out.txt"},
		{name: "cat with no redirect still denies as read",
			command: "cat foo.txt",
			want: "deny", wantRule: "Bash:cat",
			wantSugg: "ash read --path foo.txt"},
		{name: "grep with stderr redirect doesn't pollute path",
			command: "grep foo bar.txt 2>&1",
			want: "deny", wantRule: "Bash:grep",
			wantSugg: "ash grep --pattern foo --path bar.txt"},
		{name: "find with stderr redirect doesn't pollute path",
			command: "find . -name '*.go' 2>&1",
			want: "deny", wantRule: "Bash:find"},
		{name: "ls -R with stdout redirect strips path target",
			command: "ls -R internal/ > /tmp/list",
			want: "deny", wantRule: "Bash:ls-R",
			wantSugg: "ash find --path internal/"},
		{name: "stat with stderr redirect doesn't pollute paths",
			command: "stat foo.go bar.go 2>/dev/null",
			want: "deny", wantRule: "Bash:stat",
			wantSugg: "ash stat --paths foo.go,bar.go"},

		// sed: file-mode forms route to ash edit / ash read; pure pipeline
		// (no file arg) passes through since stream-transform sed has no
		// ash equivalent yet.
		{name: "sed -i substitute routes to ash edit",
			command: "sed -i 's/foo/bar/' a.go",
			want: "deny", wantRule: "Bash:sed",
			wantSugg: "ash edit --path a.go --old foo --new bar"},
		{name: "sed -i substitute with /g sets --replace_all",
			command: "sed -i 's|foo|bar|g' a.go",
			want: "deny", wantRule: "Bash:sed",
			wantSugg: "--replace_all true"},
		{name: "sed -i.bak (BSD glued backup) routes to ash edit",
			command: "sed -i.bak 's/x/y/' a.go",
			want: "deny", wantRule: "Bash:sed",
			wantSugg: "ash edit --path a.go --old x --new y"},
		{name: "sed -i '' (BSD empty backup) routes to ash edit",
			command: "sed -i '' 's/x/y/' a.go",
			want: "deny", wantRule: "Bash:sed",
			wantSugg: "ash edit --path a.go --old x --new y"},
		{name: "sed -n print range routes to ash read --range",
			command: "sed -n '10,20p' a.go",
			want: "deny", wantRule: "Bash:sed",
			wantSugg: "ash read --path a.go --range 10:20"},
		{name: "sed -n single-line print routes to ash read --range N:N",
			command: "sed -n '5p' a.go",
			want: "deny", wantRule: "Bash:sed",
			wantSugg: "ash read --path a.go --range 5:5"},
		{name: "sed -i delete single line routes to ash edit --range",
			command: "sed -i '5d' a.go",
			want: "deny", wantRule: "Bash:sed",
			wantSugg: "ash edit --path a.go --range 5:5 --new ''"},
		{name: "sed -i delete range routes to ash edit --range A:B",
			command: "sed -i '5,10d' a.go",
			want: "deny", wantRule: "Bash:sed",
			wantSugg: "ash edit --path a.go --range 5:10 --new ''"},
		{name: "sed with regex address falls back to generic",
			command: "sed -i '/PATTERN/d' a.go",
			want: "deny", wantRule: "Bash:sed",
			wantSugg: "ash edit --path a.go --old <text>"},
		{name: "sed -e expression form routes to ash edit",
			command: "sed -e 's/x/y/' a.go",
			want: "deny", wantRule: "Bash:sed",
			wantSugg: "ash edit --path a.go --old x --new y"},
		{name: "sed pure pipeline (no file) allows",
			command: "echo hi | sed 's/x/y/'",
			want: "allow"},
		{name: "sed with stderr redirect still routes via file arg",
			command: "sed -i 's/x/y/' a.go 2>/dev/null",
			want: "deny", wantRule: "Bash:sed",
			wantSugg: "ash edit --path a.go"},
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
			// ASH-69 regression: a redirection operator must never end up as
			// a literal --path value in the suggestion.
			if r.Decision == "deny" {
				for _, bad := range []string{"--path '>'", "--path >", "--path '<'", "--path '2>&1'"} {
					if strings.Contains(r.Suggested, bad) {
						t.Errorf("suggested path is a redirection operator (%q): %q", bad, r.Suggested)
					}
				}
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

func TestDecide_excludeVerbs(t *testing.T) {
	cases := []struct {
		name         string
		args         *Args
		wantDecision string
		wantRule     string
	}{
		// grep excluded: harness Grep allowed with :excluded suffix
		{
			name:         "Grep excluded allows",
			args:         &Args{ToolName: "Grep", Pattern: "foo", Path: ".", ExcludeVerbs: []string{"grep"}},
			wantDecision: "allow",
			wantRule:     "Grep:excluded",
		},
		// grep excluded: bash grep allowed
		{
			name:         "Bash:grep excluded allows",
			args:         &Args{ToolName: "Bash", Command: "grep foo .", ExcludeVerbs: []string{"grep"}},
			wantDecision: "allow",
			wantRule:     "Bash:grep:excluded",
		},
		// grep excluded: rg also allowed (same verb group)
		{
			name:         "Bash:rg excluded allows",
			args:         &Args{ToolName: "Bash", Command: "rg pattern .", ExcludeVerbs: []string{"grep"}},
			wantDecision: "allow",
			wantRule:     "Bash:rg:excluded",
		},
		// grep excluded: unrelated verb (Edit) still denies
		{
			name:         "Edit not excluded when grep excluded",
			args:         &Args{ToolName: "Edit", FilePath: "main.go", ExcludeVerbs: []string{"grep"}},
			wantDecision: "deny",
			wantRule:     "Edit",
		},
		// find excluded: Glob allowed
		{
			name:         "Glob excluded allows",
			args:         &Args{ToolName: "Glob", Pattern: "**/*.go", Path: ".", ExcludeVerbs: []string{"find"}},
			wantDecision: "allow",
			wantRule:     "Glob:excluded",
		},
		// find excluded: bash find allowed
		{
			name:         "Bash:find excluded allows",
			args:         &Args{ToolName: "Bash", Command: "find . -name '*.go'", ExcludeVerbs: []string{"find"}},
			wantDecision: "allow",
			wantRule:     "Bash:find:excluded",
		},
		// read excluded: harness Read text file allowed
		{
			name:         "Read excluded allows text file",
			args:         &Args{ToolName: "Read", FilePath: "main.go", ExcludeVerbs: []string{"read"}},
			wantDecision: "allow",
			wantRule:     "Read:excluded",
		},
		// read excluded: media files are already allowed, rule unchanged
		{
			name:         "Read .png still allows (media)",
			args:         &Args{ToolName: "Read", FilePath: "img.png", ExcludeVerbs: []string{"read"}},
			wantDecision: "allow",
			wantRule:     "Read:.png-allow",
		},
		// git excluded: bash git status allowed
		{
			name:         "Bash:git-status excluded allows",
			args:         &Args{ToolName: "Bash", Command: "git status", ExcludeVerbs: []string{"git"}},
			wantDecision: "allow",
			wantRule:     "Bash:git-status:excluded",
		},
		// git excluded: bash git commit still allows (commit was never denied)
		{
			name:         "git commit still passes through when git excluded",
			args:         &Args{ToolName: "Bash", Command: "git commit -m msg", ExcludeVerbs: []string{"git"}},
			wantDecision: "allow",
			wantRule:     "",
		},
		// stat excluded: bash stat allowed
		{
			name:         "Bash:stat excluded allows",
			args:         &Args{ToolName: "Bash", Command: "stat main.go", ExcludeVerbs: []string{"stat"}},
			wantDecision: "allow",
			wantRule:     "Bash:stat:excluded",
		},
		// test excluded: bash go test allowed
		{
			name:         "Bash:go-test excluded allows",
			args:         &Args{ToolName: "Bash", Command: "go test ./...", ExcludeVerbs: []string{"test"}},
			wantDecision: "allow",
			wantRule:     "Bash:go-test:excluded",
		},
		// edit excluded: bash sed allowed (sed maps to ash edit)
		{
			name:         "Bash:sed excluded via edit verb",
			args:         &Args{ToolName: "Bash", Command: "sed -i 's/x/y/' a.go", ExcludeVerbs: []string{"edit"}},
			wantDecision: "allow",
			wantRule:     "Bash:sed:excluded",
		},
		// read excluded: bash sed also allowed (sed -n maps to ash read)
		{
			name:         "Bash:sed excluded via read verb",
			args:         &Args{ToolName: "Bash", Command: "sed -n '5,10p' a.go", ExcludeVerbs: []string{"read"}},
			wantDecision: "allow",
			wantRule:     "Bash:sed:excluded",
		},
		// empty ExcludeVerbs: normal deny unchanged
		{
			name:         "Grep still denies with empty exclude list",
			args:         &Args{ToolName: "Grep", Pattern: "foo", ExcludeVerbs: []string{}},
			wantDecision: "deny",
			wantRule:     "Grep",
		},
		// nil ExcludeVerbs: same as empty
		{
			name:         "Grep still denies with nil ExcludeVerbs",
			args:         &Args{ToolName: "Grep", Pattern: "foo"},
			wantDecision: "deny",
			wantRule:     "Grep",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := Decide(tc.args)
			if r.Decision != tc.wantDecision {
				t.Errorf("Decision: want %q, got %q", tc.wantDecision, r.Decision)
			}
			if tc.wantRule != "" && r.MatchedRule != tc.wantRule {
				t.Errorf("MatchedRule: want %q, got %q", tc.wantRule, r.MatchedRule)
			}
		})
	}
}

func TestParseArgs_excludeVerbs(t *testing.T) {
	// Verify that exclude_verbs in the wire map is correctly decoded.
	in := map[string]any{
		"tool_name":     "Grep",
		"exclude_verbs": []interface{}{"grep", "find"},
	}
	a, perr := ParseArgs(in)
	if perr != nil {
		t.Fatal(perr)
	}
	if len(a.ExcludeVerbs) != 2 || a.ExcludeVerbs[0] != "grep" || a.ExcludeVerbs[1] != "find" {
		t.Errorf("ExcludeVerbs: %v", a.ExcludeVerbs)
	}
}

func TestClassifyRedirectToken(t *testing.T) {
	cases := []struct {
		tok          string
		isRedirect   bool
		consumesNext bool
	}{
		{">", true, true},
		{">>", true, true},
		{"<", true, true},
		{"<<", true, true},
		{"<<-", true, true},
		{"&>", true, true},
		{"&>>", true, true},
		{"2>", true, true},
		{"1>>", true, true},
		{">foo", true, false},
		{">>foo", true, false},
		{"<foo", true, false},
		{"<<EOF", true, false},
		{"<<-EOF", true, false},
		{"2>&1", true, false},
		{"2>file", true, false},
		{"&>/dev/null", true, false},
		{"foo", false, false},
		{"-n", false, false},
		{"&", false, false},
		{"", false, false},
	}
	for _, tc := range cases {
		gotRed, gotConsume := classifyRedirectToken(tc.tok)
		if gotRed != tc.isRedirect || gotConsume != tc.consumesNext {
			t.Errorf("classifyRedirectToken(%q) = (%v, %v), want (%v, %v)",
				tc.tok, gotRed, gotConsume, tc.isRedirect, tc.consumesNext)
		}
	}
}

func TestStripRedirections(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"no redirects", []string{"foo", "bar"}, []string{"foo", "bar"}},
		{"bare > consumes target", []string{"foo", ">", "out.txt"}, []string{"foo"}},
		{"glued >FILE drops single token", []string{"foo", ">out.txt"}, []string{"foo"}},
		{"2>&1 drops alone", []string{"foo", "bar.txt", "2>&1"}, []string{"foo", "bar.txt"}},
		{"heredoc bare", []string{"<<", "EOF"}, []string{}},
		{"heredoc glued", []string{"<<EOF"}, []string{}},
		{"input redirect bare", []string{"<", "in.txt"}, []string{}},
		{"mixed redirects", []string{"foo", ">", "out", "2>&1", "<<", "EOF"}, []string{"foo"}},
	}
	for _, tc := range cases {
		got := stripRedirections(tc.in)
		if len(got) != len(tc.want) {
			t.Errorf("%s: stripRedirections(%v) = %v, want %v", tc.name, tc.in, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("%s: stripRedirections(%v) = %v, want %v", tc.name, tc.in, got, tc.want)
				break
			}
		}
	}
}

func TestDetectOutputRedirect(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		wantTarget string
		wantOk     bool
	}{
		{"bare > with target", []string{">", "foo.txt"}, "foo.txt", true},
		{"bare >> with target", []string{">>", "foo.txt"}, "foo.txt", true},
		{"glued >FILE", []string{">foo.txt"}, "foo.txt", true},
		{"glued >>FILE", []string{">>foo.txt"}, "foo.txt", true},
		{"&> with target", []string{"&>", "out"}, "out", true},
		{"glued &>FILE", []string{"&>out"}, "out", true},
		{"after positional args", []string{"-n", "foo", ">", "out"}, "out", true},
		{"with stderr first", []string{"foo", "2>&1", ">", "out"}, "out", true},
		{"input redirect ignored", []string{"<", "in.txt"}, "", false},
		{"stderr-only ignored", []string{"2>", "err.txt"}, "", false},
		{"2>&1 alone ignored", []string{"2>&1"}, "", false},
		{"no redirect", []string{"foo", "bar"}, "", false},
		{"bare > no target", []string{">"}, "", false},
	}
	for _, tc := range cases {
		got, ok := detectOutputRedirect(tc.args)
		if got != tc.wantTarget || ok != tc.wantOk {
			t.Errorf("%s: detectOutputRedirect(%v) = (%q, %v), want (%q, %v)",
				tc.name, tc.args, got, ok, tc.wantTarget, tc.wantOk)
		}
	}
}
