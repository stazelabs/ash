package bench

import (
	"reflect"
	"strings"
	"testing"
)

// TestBashFor_AllVerbs is the load-bearing translation pin. Bench
// validity depends on bash forms that match what an agent would
// actually have written. A regression here silently corrupts every
// future bench claim.
func TestBashFor_AllVerbs(t *testing.T) {
	cases := []struct {
		name string
		in   Case
		want []string
	}{
		{
			name: "find_default",
			in:   Case{Verb: "find", AshArgs: map[string]any{"path": ".", "depth": "1"}},
			want: []string{"find", ".", "-maxdepth", "1"},
		},
		{
			name: "find_glob_strips_doublestar_prefix",
			in:   Case{Verb: "find", AshArgs: map[string]any{"path": ".", "glob": "**/*.go"}},
			want: []string{"find", ".", "-name", "*.go"},
		},
		{
			name: "find_type_file",
			in:   Case{Verb: "find", AshArgs: map[string]any{"path": ".", "type": "file"}},
			want: []string{"find", ".", "-type", "f"},
		},
		{
			name: "find_type_dir",
			in:   Case{Verb: "find", AshArgs: map[string]any{"path": ".", "type": "dir"}},
			want: []string{"find", ".", "-type", "d"},
		},
		{
			name: "find_type_symlink",
			in:   Case{Verb: "find", AshArgs: map[string]any{"path": ".", "type": "symlink"}},
			want: []string{"find", ".", "-type", "l"},
		},
		{
			name: "find_default_path_when_omitted",
			in:   Case{Verb: "find", AshArgs: map[string]any{}},
			want: []string{"find", "."},
		},
		{
			name: "find_zero_depth_is_dropped",
			in:   Case{Verb: "find", AshArgs: map[string]any{"path": ".", "depth": "0"}},
			want: []string{"find", "."},
		},
		{
			name: "grep_default_flags",
			in:   Case{Verb: "grep", AshArgs: map[string]any{"pattern": "TODO", "path": "."}},
			want: []string{"grep", "-rn", "TODO", "."},
		},
		{
			name: "grep_files_only",
			in:   Case{Verb: "grep", AshArgs: map[string]any{"pattern": "Run", "path": ".", "fo": "true"}},
			want: []string{"grep", "-rln", "Run", "."},
		},
		{
			name: "grep_fixed_pattern",
			in:   Case{Verb: "grep", AshArgs: map[string]any{"pattern": "TODO", "path": ".", "lit": "true"}},
			want: []string{"grep", "-rnF", "TODO", "."},
		},
		{
			name: "grep_files_only_and_fixed",
			in:   Case{Verb: "grep", AshArgs: map[string]any{"pattern": "x", "path": ".", "fo": "true", "lit": "true"}},
			want: []string{"grep", "-rlnF", "x", "."},
		},
		{
			name: "read_no_range_uses_cat",
			in:   Case{Verb: "read", AshArgs: map[string]any{"path": "README.md"}},
			want: []string{"cat", "README.md"},
		},
		{
			name: "read_line_range_uses_sed",
			in:   Case{Verb: "read", AshArgs: map[string]any{"path": "README.md", "range": "1:50"}},
			want: []string{"sed", "-n", "1,50p", "README.md"},
		},
		{
			name: "read_byte_range_from_start_uses_head",
			in:   Case{Verb: "read", AshArgs: map[string]any{"path": "x", "range": "1:100", "unit": "bytes"}},
			want: []string{"head", "-c", "100", "x"},
		},
		{
			name: "read_byte_range_mid_falls_back_to_head_E",
			in:   Case{Verb: "read", AshArgs: map[string]any{"path": "x", "range": "50:100", "unit": "bytes"}},
			want: []string{"head", "-c", "100", "x"}, // documented limitation
		},
		{
			name: "read_malformed_range_falls_back_to_cat",
			in:   Case{Verb: "read", AshArgs: map[string]any{"path": "x", "range": "no-colon"}},
			want: []string{"cat", "x"},
		},
		{
			name: "git_status",
			in:   Case{Verb: "git", AshArgs: map[string]any{"op": "status"}},
			want: []string{"git", "status"},
		},
		{
			name: "git_log_with_limit",
			in:   Case{Verb: "git", AshArgs: map[string]any{"op": "log", "limit": "20"}},
			want: []string{"git", "log", "-n", "20"},
		},
		{
			name: "git_log_with_all_filters",
			in: Case{Verb: "git", AshArgs: map[string]any{
				"op": "log", "limit": "5", "range": "main..HEAD",
				"author": "cstaszak", "since": "1w", "until": "now", "pathspec": "internal/",
			}},
			want: []string{"git", "log", "-n", "5", "main..HEAD",
				"--author", "cstaszak", "--since", "1w", "--until", "now", "--", "internal/"},
		},
		{
			name: "stat_single",
			in:   Case{Verb: "stat", AshArgs: map[string]any{"paths": "README.md"}},
			want: []string{"stat", "README.md"},
		},
		{
			name: "stat_bulk_trims_whitespace",
			in:   Case{Verb: "stat", AshArgs: map[string]any{"paths": "a, b ,c"}},
			want: []string{"stat", "a", "b", "c"},
		},
		{
			name: "stat_legacy_singular_path",
			in:   Case{Verb: "stat", AshArgs: map[string]any{"path": "X"}},
			want: []string{"stat", "X"},
		},
		{
			name: "write_with_trailing_newline",
			in:   Case{Verb: "write", AshArgs: map[string]any{"path": "/t/x", "content": "hello\n"}},
			want: []string{"sh", "-c", "cat > /t/x.bash <<'BENCH_EOF'\nhello\nBENCH_EOF\n"},
		},
		{
			name: "write_appends_newline_when_missing",
			in:   Case{Verb: "write", AshArgs: map[string]any{"path": "/t/x", "content": "hello"}},
			want: []string{"sh", "-c", "cat > /t/x.bash <<'BENCH_EOF'\nhello\nBENCH_EOF\n"},
		},
		{
			name: "edit_string_replace",
			in:   Case{Verb: "edit", AshArgs: map[string]any{"path": "/t/x", "old": "FOO", "new": "BAR"}},
			want: []string{"sed", "-i.bak", "s|FOO|BAR|g", "/t/x"},
		},
		{
			name: "diff_two_files",
			in:   Case{Verb: "diff", AshArgs: map[string]any{"path": "a.go", "other": "b.go"}},
			want: []string{"diff", "a.go", "b.go"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := BashFor(tc.in)
			if err != nil {
				t.Fatalf("BashFor: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("argv:\n  got:  %#v\n  want: %#v", got, tc.want)
			}
		})
	}
}

func TestBashFor_Errors(t *testing.T) {
	cases := []struct {
		name    string
		in      Case
		wantMsg string
	}{
		{name: "unknown_verb", in: Case{Verb: "blame"}, wantMsg: "no bash translation for verb"},
		{name: "grep_missing_pattern", in: Case{Verb: "grep", AshArgs: map[string]any{}}, wantMsg: "missing pattern"},
		{name: "read_missing_path", in: Case{Verb: "read", AshArgs: map[string]any{}}, wantMsg: "missing path"},
		{name: "git_missing_op", in: Case{Verb: "git", AshArgs: map[string]any{}}, wantMsg: "missing op"},
		{name: "git_unsupported_op", in: Case{Verb: "git", AshArgs: map[string]any{"op": "blame"}}, wantMsg: "no bash translation for git op"},
		{name: "stat_missing_paths", in: Case{Verb: "stat", AshArgs: map[string]any{}}, wantMsg: "missing paths"},
		{name: "write_missing_path", in: Case{Verb: "write", AshArgs: map[string]any{"content": "x"}}, wantMsg: "missing path"},
		{name: "edit_missing_path", in: Case{Verb: "edit", AshArgs: map[string]any{"old": "x"}}, wantMsg: "missing path"},
		{name: "edit_missing_old", in: Case{Verb: "edit", AshArgs: map[string]any{"path": "/t/x"}}, wantMsg: "missing old"},
		{name: "diff_missing_path", in: Case{Verb: "diff", AshArgs: map[string]any{"other": "b.go"}}, wantMsg: "missing path"},
		{name: "diff_missing_other", in: Case{Verb: "diff", AshArgs: map[string]any{"path": "a.go"}}, wantMsg: "missing other"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := BashFor(tc.in)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantMsg)
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("error: got %q, want substring %q", err.Error(), tc.wantMsg)
			}
		})
	}
}

func TestStrArg_AndBoolArg(t *testing.T) {
	// strArg: present-string returns the value, present-non-string and
	// absent both return the default.
	args := map[string]any{"s": "hello", "n": 42}
	if got := strArg(args, "s", "def"); got != "hello" {
		t.Errorf("strArg present-string: got %q", got)
	}
	if got := strArg(args, "n", "def"); got != "def" {
		t.Errorf("strArg present-non-string should fall back to default: got %q", got)
	}
	if got := strArg(args, "missing", "def"); got != "def" {
		t.Errorf("strArg absent: got %q", got)
	}

	// boolArg: native bool, string forms, unknown string, absent.
	boolCases := []struct {
		key  string
		val  any
		def  bool
		want bool
	}{
		{key: "b", val: true, def: false, want: true},
		{key: "b", val: false, def: true, want: false},
		{key: "b", val: "true", def: false, want: true},
		{key: "b", val: "1", def: false, want: true},
		{key: "b", val: "yes", def: false, want: true},
		{key: "b", val: "false", def: true, want: false},
		{key: "b", val: "0", def: true, want: false},
		{key: "b", val: "no", def: true, want: false},
		{key: "b", val: "garbage", def: true, want: true}, // unrecognised → default
		{key: "missing", val: nil, def: true, want: true}, // absent → default
	}
	for _, tc := range boolCases {
		m := map[string]any{}
		if tc.val != nil {
			m[tc.key] = tc.val
		}
		if got := boolArg(m, "b", tc.def); got != tc.want {
			t.Errorf("boolArg(%v, def=%v) = %v, want %v", tc.val, tc.def, got, tc.want)
		}
	}
}
