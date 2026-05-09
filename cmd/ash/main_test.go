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
	_, err := parseFlags("metrics", []string{"oops"})
	if err == nil {
		t.Fatal("expected error for positional on metrics (no slots), got nil")
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
