package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
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

// ASH-122 — bool flags whose registered name itself begins with "no-"
// (--no-text, --no-clobber, --no-registry) must accept the same four
// shapes every other flag accepts: presence-only, =value, and space-
// separated value (when bool-like). The pre-fix parser silently stripped
// the "no-" prefix and bound the trailing "true" as a positional, which
// then collided with --pattern/--path and surfaced as a confusing
// "set both as a flag and as a positional" error.

func TestParseFlags_BoolNoPrefixPresenceOnly(t *testing.T) {
	got, err := parseFlags("grep", []string{"--pattern", "x", "--path", "src", "--no-text"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	want := map[string]any{"pattern": "x", "path": "src", "no-text": "true"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseFlags_BoolNoPrefixEqualsForm(t *testing.T) {
	got, err := parseFlags("grep", []string{"--pattern", "x", "--path", "src", "--no-text=true"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	want := map[string]any{"pattern": "x", "path": "src", "no-text": "true"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseFlags_BoolNoPrefixSpaceTrue(t *testing.T) {
	// Repro from ASH-122: pre-fix, "true" floated free and collided with --pattern.
	got, err := parseFlags("grep", []string{"--pattern", "x", "--path", "src", "--no-text", "true"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	want := map[string]any{"pattern": "x", "path": "src", "no-text": "true"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseFlags_BoolNoPrefixSpaceFalse(t *testing.T) {
	got, err := parseFlags("write", []string{"--path", "/tmp/foo", "--content", "x", "--no-clobber", "false"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	want := map[string]any{"path": "/tmp/foo", "content": "x", "no-clobber": "false"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseFlags_BoolFlagSpaceNonBoolToken(t *testing.T) {
	// A non-bool-looking trailing token must NOT be consumed by a bool
	// flag — it stays available for positional binding (or, here,
	// errors loudly when no slot exists).
	_, err := parseFlags("grep", []string{"--pattern", "x", "--path", "src", "--no-text", "maybe"})
	if err == nil {
		t.Fatal("expected error: 'maybe' should not be consumed by --no-text and should fail positional binding")
	}
}

func TestParseFlags_BoolFlagSpaceBoolLiteralVariants(t *testing.T) {
	for _, in := range []string{"true", "True", "TRUE", "1", "yes", "YES", "false", "False", "0", "no", "No"} {
		got, err := parseFlags("write", []string{"--path", "f", "--content", "x", "--no-clobber", in})
		if err != nil {
			t.Fatalf("parseFlags %q: %v", in, err)
		}
		if got["no-clobber"] != in {
			t.Errorf("--no-clobber %q: got no-clobber=%v, want %q", in, got["no-clobber"], in)
		}
	}
}

func TestParseFlags_BoolFlagSpaceTrueOnPlainBool(t *testing.T) {
	// Non-no- bool flags also benefit: --all true / --stat false work
	// and don't drop the trailing token onto a positional.
	got, err := parseFlags("diff", []string{"--path", "a", "--other", "b", "--stat", "true"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	want := map[string]any{"path": "a", "other": "b", "stat": "true"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseFlags_NoShorthandStillWorksForRegisteredBool(t *testing.T) {
	// --no-mkdir is shorthand for mkdir=false (mkdir is registered with
	// default true on write); the new parser branch must not break this.
	got, err := parseFlags("write", []string{"--path", "f", "--content", "x", "--no-mkdir"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	want := map[string]any{"path": "f", "content": "x", "mkdir": "false"}
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

func TestResolveStdin_OldDash(t *testing.T) {
	args := map[string]any{"path": "f.go", "old": "-"}
	if err := resolveStdinFromReader(args, strings.NewReader("multi\nline\npattern\n"), false); err != nil {
		t.Fatalf("resolveStdinFromReader: %v", err)
	}
	want := map[string]any{"path": "f.go", "old": "multi\nline\npattern\n"}
	if !reflect.DeepEqual(args, want) {
		t.Errorf("got %v, want %v", args, want)
	}
}

func TestResolveStdin_NewDash(t *testing.T) {
	args := map[string]any{"path": "f.go", "new": "-"}
	if err := resolveStdinFromReader(args, strings.NewReader("replacement"), false); err != nil {
		t.Fatalf("resolveStdinFromReader: %v", err)
	}
	if args["new"] != "replacement" {
		t.Errorf("got %v, want \"replacement\"", args["new"])
	}
}

func TestResolveStdin_NoDash(t *testing.T) {
	args := map[string]any{"path": "f.go", "old": "literal"}
	before := map[string]any{"path": "f.go", "old": "literal"}
	if err := resolveStdinFromReader(args, strings.NewReader("ignored"), false); err != nil {
		t.Fatalf("resolveStdinFromReader: %v", err)
	}
	if !reflect.DeepEqual(args, before) {
		t.Errorf("args mutated when no - sentinel: got %v, want %v", args, before)
	}
}

func TestResolveStdin_BothOldAndNewConflict(t *testing.T) {
	args := map[string]any{"old": "-", "new": "-"}
	err := resolveStdinFromReader(args, strings.NewReader("x"), false)
	if err == nil {
		t.Fatal("expected conflict error for --old - and --new -, got nil")
	}
	if !strings.Contains(err.Error(), "only one arg can read from stdin") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestResolveStdin_TTYRefuses(t *testing.T) {
	args := map[string]any{"old": "-"}
	err := resolveStdinFromReader(args, strings.NewReader(""), true)
	if err == nil {
		t.Fatal("expected stdin_not_piped error, got nil")
	}
	if !strings.Contains(err.Error(), "stdin_not_piped") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestResolveAtFile_OldAndNew(t *testing.T) {
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "old.txt")
	newPath := filepath.Join(dir, "new.txt")
	if err := os.WriteFile(oldPath, []byte("old\n\tblock\n"), 0o600); err != nil {
		t.Fatalf("write old: %v", err)
	}
	if err := os.WriteFile(newPath, []byte("new\n\tblock\n"), 0o600); err != nil {
		t.Fatalf("write new: %v", err)
	}
	args := map[string]any{"path": "f.go", "old": "@" + oldPath, "new": "@" + newPath}
	if err := resolveAtFile(args, "old", "new"); err != nil {
		t.Fatalf("resolveAtFile: %v", err)
	}
	if args["old"] != "old\n\tblock\n" {
		t.Errorf("old: got %q, want %q", args["old"], "old\n\tblock\n")
	}
	if args["new"] != "new\n\tblock\n" {
		t.Errorf("new: got %q, want %q", args["new"], "new\n\tblock\n")
	}
}

func TestResolveAtFile_NoPrefixPassthrough(t *testing.T) {
	args := map[string]any{"old": "literal text", "new": ""}
	before := map[string]any{"old": "literal text", "new": ""}
	if err := resolveAtFile(args, "old", "new"); err != nil {
		t.Fatalf("resolveAtFile: %v", err)
	}
	if !reflect.DeepEqual(args, before) {
		t.Errorf("args mutated when no @ sentinel: got %v, want %v", args, before)
	}
}

func TestResolveAtFile_MissingFileErrors(t *testing.T) {
	args := map[string]any{"old": "@/nonexistent/path/to/file"}
	err := resolveAtFile(args, "old", "new")
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
	if !strings.Contains(err.Error(), "--old") {
		t.Errorf("error should name the flag: %v", err)
	}
}

func TestResolveAtFile_EmptyPathErrors(t *testing.T) {
	args := map[string]any{"old": "@"}
	err := resolveAtFile(args, "old", "new")
	if err == nil {
		t.Fatal("expected error for bare @, got nil")
	}
	if !strings.Contains(err.Error(), "empty path") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestResolveAtFile_DirectoryErrors(t *testing.T) {
	dir := t.TempDir()
	args := map[string]any{"old": "@" + dir}
	err := resolveAtFile(args, "old", "new")
	if err == nil {
		t.Fatal("expected error for directory, got nil")
	}
	if !strings.Contains(err.Error(), "regular file") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestResolveAtFile_CoexistsWithStdin verifies the load-bearing combo:
// --old @file resolves from disk while --new - reads stdin in the same
// invocation. This is the central ergonomic win of ASH-119.
func TestResolveAtFile_CoexistsWithStdin(t *testing.T) {
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "old.txt")
	if err := os.WriteFile(oldPath, []byte("from-disk"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	args := map[string]any{"old": "@" + oldPath, "new": "-"}
	if err := resolveAtFile(args, "old", "new"); err != nil {
		t.Fatalf("resolveAtFile: %v", err)
	}
	if err := resolveStdinFromReader(args, strings.NewReader("from-stdin"), false); err != nil {
		t.Fatalf("resolveStdinFromReader: %v", err)
	}
	if args["old"] != "from-disk" {
		t.Errorf("old: got %q, want \"from-disk\"", args["old"])
	}
	if args["new"] != "from-stdin" {
		t.Errorf("new: got %q, want \"from-stdin\"", args["new"])
	}
}

