package gitignore

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadFromDir_NoFileReturnsNilNil(t *testing.T) {
	dir := t.TempDir()
	m, err := LoadFromDir(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m != nil {
		t.Fatal("expected nil matcher when no .gitignore present")
	}
	// nil matcher must be safe to call methods on.
	if m.Excludes("anything", false) {
		t.Error("nil matcher excluded a path")
	}
}

func TestExcludes_BasicPatterns(t *testing.T) {
	dir := t.TempDir()
	body := []byte("bin/\n*.log\nnode_modules/\n!important.log\n")
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := LoadFromDir(dir)
	if err != nil {
		t.Fatalf("LoadFromDir: %v", err)
	}
	if m == nil {
		t.Fatal("expected non-nil matcher")
	}
	cases := []struct {
		path    string
		isDir   bool
		exclude bool
	}{
		{"bin/ash", false, true},                    // file inside ignored dir
		{"bin", true, true},                         // the dir itself, with isDir=true
		{"bin", false, false},                       // a file literally named "bin" should not match "bin/"
		{"foo.log", false, true},                    // glob pattern
		{"deep/sub/foo.log", false, true},           // glob recurses
		{"important.log", false, false},             // negation pattern
		{"node_modules", true, true},                // dir-only pattern fires on dir
		{"node_modules/pkg/index.js", false, true},  // children of ignored dir
		{"src/main.go", false, false},               // not matched
		{"README.md", false, false},                 // not matched
	}
	for _, c := range cases {
		if got := m.Excludes(c.path, c.isDir); got != c.exclude {
			t.Errorf("Excludes(%q, isDir=%v) = %v, want %v", c.path, c.isDir, got, c.exclude)
		}
	}
}

func TestExcludes_AbsolutePathNormalized(t *testing.T) {
	dir := t.TempDir()
	body := []byte("bin/\n")
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := LoadFromDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	abs := filepath.Join(dir, "bin", "ash")
	if !m.Excludes(abs, false) {
		t.Errorf("absolute path %q should match bin/ rule", abs)
	}
}
