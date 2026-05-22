package gitignore

import (
	"os"
	"path/filepath"
	"testing"
	"time"
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
		{"bin/ash", false, true},                   // file inside ignored dir
		{"bin", true, true},                        // the dir itself, with isDir=true
		{"bin", false, false},                      // a file literally named "bin" should not match "bin/"
		{"foo.log", false, true},                   // glob pattern
		{"deep/sub/foo.log", false, true},          // glob recurses
		{"important.log", false, false},            // negation pattern
		{"node_modules", true, true},               // dir-only pattern fires on dir
		{"node_modules/pkg/index.js", false, true}, // children of ignored dir
		{"src/main.go", false, false},              // not matched
		{"README.md", false, false},                // not matched
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

func TestLoadFromDir_CacheHit(t *testing.T) {
	dir := t.TempDir()
	body := []byte("bin/\n*.log\n")
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	m1, err := LoadFromDir(dir)
	if err != nil {
		t.Fatalf("first load: %v", err)
	}
	m2, err := LoadFromDir(dir)
	if err != nil {
		t.Fatalf("second load: %v", err)
	}
	if m1 != m2 {
		t.Fatalf("cache miss: got distinct *Matcher pointers (%p vs %p), expected same", m1, m2)
	}
}

func TestLoadFromDir_MtimeInvalidatesCache(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".gitignore")
	if err := os.WriteFile(path, []byte("bin/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m1, err := LoadFromDir(dir)
	if err != nil {
		t.Fatalf("first load: %v", err)
	}
	if m1 == nil || !m1.Excludes("bin", true) {
		t.Fatal("first load should match bin/")
	}
	// Replace contents and bump mtime forward to ensure the cache
	// guard fires regardless of filesystem mtime resolution.
	if err := os.WriteFile(path, []byte("vendor/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}
	m2, err := LoadFromDir(dir)
	if err != nil {
		t.Fatalf("second load: %v", err)
	}
	if m2 == m1 {
		t.Fatal("cache should have invalidated on mtime change")
	}
	if !m2.Excludes("vendor", true) {
		t.Error("new rules not applied: vendor/ should match")
	}
	if m2.Excludes("bin", true) {
		t.Error("old rules still applied: bin/ should no longer match")
	}
}

func TestExcludes_MemoizesSamePath(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("bin/\n*.log\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := LoadFromDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	// First call populates the cache; subsequent calls must agree and
	// must read the same cache entry (verified by Load returning ok).
	cases := []struct {
		path    string
		isDir   bool
		exclude bool
	}{
		{"bin", true, true},
		{"bin", false, false}, // distinct cache key from "bin" with isDir=true
		{"foo.log", false, true},
		{"src/main.go", false, false},
	}
	for _, c := range cases {
		if got := m.Excludes(c.path, c.isDir); got != c.exclude {
			t.Errorf("Excludes(%q, isDir=%v) = %v, want %v", c.path, c.isDir, got, c.exclude)
		}
		if got := m.Excludes(c.path, c.isDir); got != c.exclude {
			t.Errorf("Excludes(%q, isDir=%v) second call = %v, want %v", c.path, c.isDir, got, c.exclude)
		}
	}
	// Spot-check the cache map directly: each (path, isDir) produced one entry.
	want := []string{"bin/", "bin", "foo.log", "src/main.go"}
	for _, k := range want {
		if _, ok := m.resCache.Load(k); !ok {
			t.Errorf("resCache missing entry for %q", k)
		}
	}
}

func TestExcludes_MemoCacheNotSharedAcrossMatchers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".gitignore")
	if err := os.WriteFile(path, []byte("bin/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m1, err := LoadFromDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !m1.Excludes("bin", true) {
		t.Fatal("m1 should exclude bin/")
	}
	// Mutate .gitignore: invalidates LoadFromDir cache (different m2),
	// which in turn means a fresh resCache. The old "bin/=true" memo
	// must not leak into the new matcher.
	if err := os.WriteFile(path, []byte("vendor/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}
	m2, err := LoadFromDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if m2 == m1 {
		t.Fatal("expected new matcher after .gitignore change")
	}
	if m2.Excludes("bin", true) {
		t.Error("new matcher should not exclude bin/ — old memo leaked")
	}
}

func TestLoadFromDir_SizeInvalidatesCacheOnSameMtime(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".gitignore")
	if err := os.WriteFile(path, []byte("bin/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Pin mtime so only size changes between loads. Guards against
	// rapid same-second edits that mtime alone would miss.
	t0 := time.Unix(1_700_000_000, 0)
	if err := os.Chtimes(path, t0, t0); err != nil {
		t.Fatal(err)
	}
	m1, err := LoadFromDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("vendor/\nnode_modules/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, t0, t0); err != nil {
		t.Fatal(err)
	}
	m2, err := LoadFromDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if m2 == m1 {
		t.Fatal("cache should have invalidated on size change despite identical mtime")
	}
	if !m2.Excludes("vendor", true) {
		t.Error("new rules not applied")
	}
}
