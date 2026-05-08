package diff

import (
	"strings"
	"testing"
)

func TestLines_Empty(t *testing.T) {
	edits, err := Lines(nil, nil)
	if err != nil || len(edits) != 0 {
		t.Fatalf("empty: err=%v edits=%v", err, edits)
	}
}

func TestLines_Insert(t *testing.T) {
	a := []string{}
	b := []string{"line1", "line2"}
	edits, err := Lines(a, b)
	if err != nil {
		t.Fatal(err)
	}
	ops := opsStr(edits)
	if ops != "++" {
		t.Errorf("ops=%q want ++", ops)
	}
}

func TestLines_Delete(t *testing.T) {
	a := []string{"line1", "line2"}
	b := []string{}
	edits, err := Lines(a, b)
	if err != nil {
		t.Fatal(err)
	}
	ops := opsStr(edits)
	if ops != "--" {
		t.Errorf("ops=%q want --", ops)
	}
}

func TestLines_Replace(t *testing.T) {
	a := []string{"old"}
	b := []string{"new"}
	edits, err := Lines(a, b)
	if err != nil {
		t.Fatal(err)
	}
	ops := opsStr(edits)
	if ops != "-+" {
		t.Errorf("ops=%q want -+", ops)
	}
}

func TestLines_Identical(t *testing.T) {
	lines := []string{"a", "b", "c"}
	edits, err := Lines(lines, lines)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range edits {
		if e.Op != ' ' {
			t.Errorf("identical: unexpected op %c for line %q", e.Op, e.Line)
		}
	}
}

func TestLines_MiddleEdit(t *testing.T) {
	a := []string{"a", "b", "c"}
	b := []string{"a", "x", "c"}
	edits, err := Lines(a, b)
	if err != nil {
		t.Fatal(err)
	}
	ops := opsStr(edits)
	if ops != " -+ " {
		t.Errorf("ops=%q want \" -+ \"", ops)
	}
}

func TestLines_OverLimit(t *testing.T) {
	a := make([]string, MaxLines+1)
	_, err := Lines(a, []string{})
	if err == nil {
		t.Fatal("expected error for over-limit input")
	}
}

func TestStats(t *testing.T) {
	edits := []Edit{
		{' ', "ctx"},
		{'-', "del"},
		{'+', "ins1"},
		{'+', "ins2"},
	}
	add, del := Stats(edits)
	if add != 2 || del != 1 {
		t.Errorf("add=%d del=%d want 2,1", add, del)
	}
}

func TestUnified_NoChanges(t *testing.T) {
	lines := []string{"a", "b"}
	edits, _ := Lines(lines, lines)
	out := Unified(edits, "a.go", "b.go", 3)
	if out != "" {
		t.Errorf("expected empty for no-change diff, got %q", out)
	}
}

func TestUnified_Replace(t *testing.T) {
	a := []string{"line1", "old", "line3"}
	b := []string{"line1", "new", "line3"}
	edits, _ := Lines(a, b)
	out := Unified(edits, "a.go", "b.go", 1)
	if !strings.Contains(out, "-old") {
		t.Errorf("expected -old in diff: %s", out)
	}
	if !strings.Contains(out, "+new") {
		t.Errorf("expected +new in diff: %s", out)
	}
	if !strings.Contains(out, "--- a.go") {
		t.Errorf("missing --- header: %s", out)
	}
}

func TestUnified_HunkHeader(t *testing.T) {
	a := []string{"a", "b", "c", "d", "e"}
	b := []string{"a", "b", "X", "d", "e"}
	edits, _ := Lines(a, b)
	out := Unified(edits, "a", "b", 1)
	if !strings.Contains(out, "@@") {
		t.Errorf("missing @@ header: %s", out)
	}
}

func TestSplitLines(t *testing.T) {
	lines := SplitLines("a\nb\nc\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines want 3: %v", len(lines), lines)
	}
	if lines[0] != "a" || lines[2] != "c" {
		t.Errorf("unexpected lines: %v", lines)
	}
}

func TestSplitJoinRoundtrip(t *testing.T) {
	orig := "foo\nbar\nbaz\n"
	if got := JoinLines(SplitLines(orig)); got != orig {
		t.Errorf("roundtrip=%q want %q", got, orig)
	}
}

// -- helpers --------------------------------------------------------------

func opsStr(edits []Edit) string {
	var b strings.Builder
	for _, e := range edits {
		b.WriteByte(e.Op)
	}
	return b.String()
}
