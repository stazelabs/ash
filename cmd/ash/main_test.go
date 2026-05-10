package main

import (
	"reflect"
	"testing"
)

func TestParseFlags_FlagOnly(t *testing.T) {
	got, err := parseFlags("read", []string{"--path", "foo.go", "--range", "10:20"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	want := map[string]any{"path": "foo.go", "range": "10:20"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseFlags_EqualsForm(t *testing.T) {
	got, err := parseFlags("read", []string{"--path=foo.go", "--range=1:2"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	want := map[string]any{"path": "foo.go", "range": "1:2"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseFlags_PositionalSingle(t *testing.T) {
	got, err := parseFlags("read", []string{"foo.go"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if got["path"] != "foo.go" {
		t.Errorf("expected path=foo.go, got %v", got)
	}
}

func TestParseFlags_PositionalThenFlag(t *testing.T) {
	got, err := parseFlags("read", []string{"foo.go", "--range", "1:10"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	want := map[string]any{"path": "foo.go", "range": "1:10"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseFlags_TwoPositionalsGrep(t *testing.T) {
	got, err := parseFlags("grep", []string{"TODO", "internal"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	want := map[string]any{"pattern": "TODO", "path": "internal"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseFlags_PositionalConflictsWithFlag(t *testing.T) {
	_, err := parseFlags("read", []string{"--path", "x.go", "y.go"})
	if err == nil {
		t.Fatal("expected error when --path and positional both set, got nil")
	}
}

func TestParseFlags_PositionalRejectedWhenNoSlots(t *testing.T) {
	_, err := parseFlags("stop", []string{"oops"})
	if err == nil {
		t.Fatal("expected error for positional on stop (no slots), got nil")
	}
}

func TestParseFlags_UnknownVerbHasNoPositionals(t *testing.T) {
	_, err := parseFlags("madeup", []string{"x"})
	if err == nil {
		t.Fatal("expected error for positional on unknown verb, got nil")
	}
}

func TestParseFlags_FlagMissingValue(t *testing.T) {
	_, err := parseFlags("read", []string{"--path"})
	if err == nil {
		t.Fatal("expected error for flag without value, got nil")
	}
}

func TestParseFlags_RepeatedListFlagAccumulates(t *testing.T) {
	got, err := parseFlags("test", []string{"--packages", "./internal/a/...", "--packages", "./internal/b/..."})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	want := map[string]any{"packages": "./internal/a/...,./internal/b/..."}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseFlags_RepeatedListFlagAccumulatesEqualsForm(t *testing.T) {
	got, err := parseFlags("test", []string{"--packages=a", "--packages=b", "--packages=c"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	want := map[string]any{"packages": "a,b,c"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseFlags_RepeatedListFlagMixedWithComma(t *testing.T) {
	got, err := parseFlags("test", []string{"--packages", "a,b", "--packages", "c"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	want := map[string]any{"packages": "a,b,c"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseFlags_RepeatedListFlagStatPaths(t *testing.T) {
	got, err := parseFlags("stat", []string{"--paths", "a.go", "--paths", "b.go"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	want := map[string]any{"paths": "a.go,b.go"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseFlags_RepeatedNonListFlagErrors(t *testing.T) {
	_, err := parseFlags("read", []string{"--path", "a.go", "--path", "b.go"})
	if err == nil {
		t.Fatal("expected error for repeated --path, got nil")
	}
}

func TestParseFlags_RepeatedListFlagOnNonListVerbErrors(t *testing.T) {
	// packages is a list flag for "test" but not for any other verb.
	_, err := parseFlags("read", []string{"--packages", "a", "--packages", "b"})
	if err == nil {
		t.Fatal("expected error: packages is not a list flag on read")
	}
}

func TestParseFlags_PositionalThenSameKeyFlagErrors(t *testing.T) {
	_, err := parseFlags("read", []string{"foo.go", "--path", "bar.go"})
	if err == nil {
		t.Fatal("expected error when --path is set after the path positional, got nil")
	}
}

func TestParseFlags_NoPrefixBare(t *testing.T) {
	got, err := parseFlags("find", []string{"--path", ".", "--no-gi"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	want := map[string]any{"path": ".", "gi": "false"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseFlags_NoPrefixMultiple(t *testing.T) {
	got, err := parseFlags("write", []string{"--path", "f.go", "--no-mkdir"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	want := map[string]any{"path": "f.go", "mkdir": "false"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseFlags_NoPrefixDoesNotConsumeNextArg(t *testing.T) {
	// --no-gi should not consume the following --path argument.
	got, err := parseFlags("find", []string{"--no-gi", "--path", "src"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	want := map[string]any{"gi": "false", "path": "src"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseFlags_GitPositional(t *testing.T) {
	got, err := parseFlags("git", []string{"log"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	want := map[string]any{"op": "log"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseFlags_HelpPositional(t *testing.T) {
	got, err := parseFlags("help", []string{"find"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	want := map[string]any{"verb": "find"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseFlags_MetricsPositional(t *testing.T) {
	got, err := parseFlags("metrics", []string{"grep"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	want := map[string]any{"verb": "grep"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseFlags_ReportPositional(t *testing.T) {
	got, err := parseFlags("report", []string{"find"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	want := map[string]any{"verb": "find"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseFlags_BenchTwoPositionals(t *testing.T) {
	got, err := parseFlags("bench", []string{"grep", "large-file"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	want := map[string]any{"verb": "grep", "case": "large-file"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}
