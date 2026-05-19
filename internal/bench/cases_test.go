package bench

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindCase_FoundAndMissing(t *testing.T) {
	c := FindCase("find_shallow")
	if c == nil {
		t.Fatal("expected canonical case 'find_shallow' to resolve")
	}
	if c.Verb != "find" {
		t.Errorf("find_shallow verb: got %q, want find", c.Verb)
	}
	if FindCase("does-not-exist") != nil {
		t.Errorf("missing case should return nil")
	}
}

func TestExpandArgs_ReplacesRootPlaceholder(t *testing.T) {
	in := map[string]any{
		"path":  "{root}/internal",
		"glob":  "**/*.go",
		"depth": 1, // non-string survives untouched
	}
	out := ExpandArgs(in, "/abs/repo")
	if out["path"] != "/abs/repo/internal" {
		t.Errorf("path: got %v", out["path"])
	}
	if out["glob"] != "**/*.go" {
		t.Errorf("glob (no placeholder) should be unchanged: got %v", out["glob"])
	}
	if out["depth"] != 1 {
		t.Errorf("non-string value should pass through: got %v", out["depth"])
	}
	// Input must not be mutated — ExpandArgs returns a copy.
	if in["path"] != "{root}/internal" {
		t.Errorf("input map mutated: %v", in["path"])
	}
}

func TestExpandArgs_EmptyMap(t *testing.T) {
	out := ExpandArgs(map[string]any{}, "/abs/repo")
	if len(out) != 0 {
		t.Errorf("empty input should produce empty output: got %v", out)
	}
}

// TestCasesIntegrity guards the static case list against rot. Each
// canonical case must have a non-empty Name + Verb, a Why text, an
// AshArgs map, a verb that is in MeasuredVerbs, and a unique name.
// Coverage_test.go in verbs/bench already gates verb-vs-MeasuredVerbs
// from the registry side; this gate is the case-side counterpart.
func TestCasesIntegrity(t *testing.T) {
	measured := map[string]bool{}
	for _, v := range MeasuredVerbs {
		measured[v] = true
	}
	seen := map[string]bool{}
	for i, c := range Cases {
		if c.Name == "" {
			t.Errorf("Cases[%d]: empty Name", i)
		}
		if c.Verb == "" {
			t.Errorf("Cases[%d] (%s): empty Verb", i, c.Name)
		}
		if c.Why == "" {
			t.Errorf("Cases[%d] (%s): empty Why — bench output cites this", i, c.Name)
		}
		if c.AshArgs == nil {
			t.Errorf("Cases[%d] (%s): nil AshArgs", i, c.Name)
		}
		if !measured[c.Verb] {
			t.Errorf("Cases[%d] (%s): verb %q not in MeasuredVerbs", i, c.Name, c.Verb)
		}
		if seen[c.Name] {
			t.Errorf("Cases[%d]: duplicate name %q", i, c.Name)
		}
		seen[c.Name] = true
	}
}

func TestEnsureBenchTmpDirAndWriteFixture(t *testing.T) {
	// Work from a tmpdir so we don't litter the repo.
	orig, _ := os.Getwd()
	defer os.Chdir(orig)
	tmp := t.TempDir()
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	if err := ensureBenchTmpDir(); err != nil {
		t.Fatalf("ensureBenchTmpDir: %v", err)
	}
	if _, err := os.Stat(BenchTmpDir); err != nil {
		t.Errorf("BenchTmpDir not created: %v", err)
	}
	// Idempotent — calling again should not error.
	if err := ensureBenchTmpDir(); err != nil {
		t.Errorf("ensureBenchTmpDir second call: %v", err)
	}

	if err := writeFixture("sub/x.txt", "hello\n"); err != nil {
		t.Fatalf("writeFixture: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(BenchTmpDir, "sub", "x.txt"))
	if err != nil {
		t.Fatalf("read back fixture: %v", err)
	}
	if string(got) != "hello\n" {
		t.Errorf("fixture content: got %q, want %q", string(got), "hello\n")
	}

	if err := CleanupBenchTmpDir(); err != nil {
		t.Errorf("CleanupBenchTmpDir: %v", err)
	}
	if _, err := os.Stat(BenchTmpDir); !os.IsNotExist(err) {
		t.Errorf("BenchTmpDir should be gone after cleanup; stat err=%v", err)
	}
}
