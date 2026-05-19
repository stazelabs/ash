package git

import (
	"strings"
	"testing"

	"github.com/stazelabs/ash/internal/proto"
)

func TestParseBlameLines(t *testing.T) {
	cases := []struct {
		name    string
		spec    string
		wantOk  bool
		wantS   int
		wantE   int
		wantErr string
	}{
		{name: "empty", spec: "", wantOk: false},
		{name: "full range", spec: "5:10", wantOk: true, wantS: 5, wantE: 10},
		{name: "open end", spec: "5:", wantOk: true, wantS: 5, wantE: 0},
		{name: "open start", spec: ":10", wantOk: true, wantS: 0, wantE: 10},
		{name: "single line", spec: "7:7", wantOk: true, wantS: 7, wantE: 7},
		{name: "no colon", spec: "5", wantErr: "expected start:end"},
		{name: "zero start", spec: "0:5", wantErr: "invalid --lines start"},
		{name: "negative end", spec: "5:-1", wantErr: "invalid --lines end"},
		{name: "garbage", spec: "abc:def", wantErr: "invalid --lines start"},
		{name: "end < start", spec: "10:5", wantErr: "end 5 < start 10"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, e, ok, perr := parseBlameLines(c.spec)
			if c.wantErr != "" {
				if perr == nil {
					t.Fatalf("expected error containing %q, got nil (s=%d e=%d ok=%v)", c.wantErr, s, e, ok)
				}
				if !strings.Contains(perr.Msg, c.wantErr) {
					t.Errorf("error msg %q does not contain %q", perr.Msg, c.wantErr)
				}
				if perr.Code != "args" {
					t.Errorf("code=%q want args", perr.Code)
				}
				return
			}
			if perr != nil {
				t.Fatalf("unexpected error: %+v", perr)
			}
			if ok != c.wantOk {
				t.Errorf("ok=%v want %v", ok, c.wantOk)
			}
			if s != c.wantS || e != c.wantE {
				t.Errorf("got (%d,%d) want (%d,%d)", s, e, c.wantS, c.wantE)
			}
		})
	}
}

func TestCompactBlameLines_RunsCollapse(t *testing.T) {
	lines := []blameLine{
		{SHA: "aaaa111aaaa", AuthorName: "Alice", AuthorTime: 100, Text: "line 1"},
		{SHA: "aaaa111aaaa", AuthorName: "Alice", AuthorTime: 100, Text: "line 2"},
		{SHA: "aaaa111aaaa", AuthorName: "Alice", AuthorTime: 100, Text: "line 3"},
		{SHA: "bbbb222bbbb", AuthorName: "Bob", AuthorTime: 200, Text: "line 4"},
		{SHA: "aaaa111aaaa", AuthorName: "Alice", AuthorTime: 100, Text: "line 5"},
		{SHA: "aaaa111aaaa", AuthorName: "Alice", AuthorTime: 100, Text: "line 6"},
	}
	hunks := compactBlameLines(lines, 10)
	if len(hunks) != 3 {
		t.Fatalf("want 3 hunks (Alice/Bob/Alice), got %d: %+v", len(hunks), hunks)
	}
	if hunks[0].SHA != "aaaa111aaaa" || hunks[0].StartLine != 10 || len(hunks[0].Lines) != 3 {
		t.Errorf("hunk 0: %+v", hunks[0])
	}
	if hunks[1].SHA != "bbbb222bbbb" || hunks[1].StartLine != 13 || len(hunks[1].Lines) != 1 {
		t.Errorf("hunk 1: %+v", hunks[1])
	}
	if hunks[2].SHA != "aaaa111aaaa" || hunks[2].StartLine != 14 || len(hunks[2].Lines) != 2 {
		t.Errorf("hunk 2: %+v", hunks[2])
	}
	if hunks[0].ShortSHA != "aaaa111" {
		t.Errorf("ShortSHA=%q want aaaa111", hunks[0].ShortSHA)
	}
}

func TestCompactBlameLines_Empty(t *testing.T) {
	if got := compactBlameLines(nil, 1); got != nil {
		t.Errorf("want nil for empty input, got %+v", got)
	}
	if got := compactBlameLines([]blameLine{}, 1); got != nil {
		t.Errorf("want nil for empty input, got %+v", got)
	}
}

func TestCompactBlameLines_SingleLine(t *testing.T) {
	got := compactBlameLines([]blameLine{{SHA: "abcdef0", AuthorName: "X", Text: "hi"}}, 42)
	if len(got) != 1 || got[0].StartLine != 42 || len(got[0].Lines) != 1 || got[0].Lines[0] != "hi" {
		t.Errorf("single-line compaction wrong: %+v", got)
	}
}

func TestApplyBlameByteCap_NoTrunc(t *testing.T) {
	hunks := []BlameHunk{
		{SHA: "a", Lines: []string{"short"}},
		{SHA: "b", Lines: []string{"line"}},
	}
	got, truncated, ti := applyBlameByteCap(hunks, 1024)
	if truncated || ti != nil {
		t.Errorf("expected no truncation, got truncated=%v ti=%+v", truncated, ti)
	}
	if len(got) != 2 {
		t.Errorf("hunks shrank: %d", len(got))
	}
}

func TestApplyBlameByteCap_Trunc(t *testing.T) {
	big := strings.Repeat("x", 200)
	hunks := []BlameHunk{
		{SHA: "a", Lines: []string{big, big, big}}, // ~600+ bytes + 80 overhead
		{SHA: "b", Lines: []string{big, big, big}},
		{SHA: "c", Lines: []string{big}},
	}
	got, truncated, ti := applyBlameByteCap(hunks, 500)
	if !truncated {
		t.Fatalf("expected truncation, got got=%d truncated=%v", len(got), truncated)
	}
	if len(got) < 1 {
		t.Errorf("expected at least one hunk retained, got %d", len(got))
	}
	if ti == nil || ti.Trunc < 1 || ti.Limit != 500 {
		t.Errorf("TruncInfo wrong: %+v", ti)
	}
}

func TestApplyBlameByteCap_KeepAtLeastOne(t *testing.T) {
	huge := strings.Repeat("x", 10_000)
	hunks := []BlameHunk{
		{SHA: "a", Lines: []string{huge}},
		{SHA: "b", Lines: []string{"short"}},
	}
	got, truncated, _ := applyBlameByteCap(hunks, 100)
	if !truncated {
		t.Fatal("expected truncation")
	}
	if len(got) != 1 {
		t.Errorf("expected exactly 1 hunk retained (first), got %d", len(got))
	}
}

func TestPrettyBlame_Basic(t *testing.T) {
	b := &BlameResult{
		Path: "internal/foo.go",
		Rev:  "abcdef1234567890abcdef1234567890abcdef12",
		Hunks: []BlameHunk{
			{SHA: "abcdef1234567890", ShortSHA: "abcdef1", AuthorName: "Alice",
				AuthorTime: 1_700_000_000_000_000_000, StartLine: 1, Lines: []string{"package foo", "", "func Foo() {}"}},
			{SHA: "fedcba0987654321", ShortSHA: "fedcba0", AuthorName: "Bob",
				AuthorTime: 1_705_000_000_000_000_000, StartLine: 4, Lines: []string{"func Bar() {}"}},
		},
	}
	out := prettyBlame(b)
	for _, want := range []string{
		"§git blame: internal/foo.go @ abcdef1",
		"2 hunks, 4 lines",
		"abcdef1  Alice",
		"L1-3",
		"fedcba0  Bob",
		"L4-4",
		"  package foo",
		"  func Foo() {}",
		"  func Bar() {}",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("pretty output missing %q\nactual:\n%s", want, out)
		}
	}
}

func TestPrettyBlame_Truncated(t *testing.T) {
	b := &BlameResult{
		Path:      "huge.json",
		Rev:       "abcdef1234567890abcdef1234567890abcdef12",
		Hunks:     []BlameHunk{{SHA: "abcdef1", ShortSHA: "abcdef1", AuthorName: "A", StartLine: 1, Lines: []string{"x"}}},
		Truncated: true,
		TruncInfo: &proto.TruncInfo{Trunc: 5, Limit: 262144, Max: BlameMaxBytes},
	}
	out := prettyBlame(b)
	if !strings.Contains(out, "TRUNCATED") {
		t.Errorf("expected TRUNCATED marker:\n%s", out)
	}
	if !strings.Contains(out, "truncated 5 hunks") {
		t.Errorf("expected trunc footer with hunk count:\n%s", out)
	}
}

func TestPrettyBlame_Nil(t *testing.T) {
	out := prettyBlame(nil)
	if !strings.Contains(out, "empty blame") {
		t.Errorf("expected empty-blame marker, got %q", out)
	}
}

func TestRunBlame_ShelloutNotImplemented(t *testing.T) {
	prev := currentBackend()
	if err := SetBackend("shellout"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		switch prev {
		case backendShellout:
			_ = SetBackend("shellout")
		default:
			_ = SetBackend("go-git")
		}
	})
	_, perr := runBlame(&Args{Op: "blame", Path: "/tmp/anywhere/foo.go"}, nil)
	if perr == nil || perr.Code != "not_implemented" {
		t.Fatalf("expected not_implemented, got %+v", perr)
	}
}

func TestParseArgs_BlameRange(t *testing.T) {
	a, perr := ParseArgs(map[string]any{"op": "blame", "path": ".", "lines": "10:20", "rev": "HEAD~2"})
	if perr != nil {
		t.Fatalf("ParseArgs: %+v", perr)
	}
	if a.Lines != "10:20" {
		t.Errorf("Lines=%q want 10:20", a.Lines)
	}
	if a.Rev != "HEAD~2" {
		t.Errorf("Rev=%q want HEAD~2", a.Rev)
	}
}
