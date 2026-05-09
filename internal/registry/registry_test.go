package registry

import (
	"os"
	"path/filepath"
	"testing"
)

// withTempXDG redirects XDG_CONFIG_HOME to a temp dir for the test so
// Path()/Add()/Remove()/List() all operate on a hermetic file.
func withTempXDG(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	return dir
}

func TestList_MissingFile(t *testing.T) {
	withTempXDG(t)
	got, err := List()
	if err != nil {
		t.Fatalf("List on missing file: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty, got %v", got)
	}
}

func TestAdd_CreatesAndDedupes(t *testing.T) {
	xdg := withTempXDG(t)
	added, err := Add("/some/repo")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if !added {
		t.Fatal("first Add should report added=true")
	}
	// File created where Path() points.
	if _, err := os.Stat(filepath.Join(xdg, "ash", "installed-repos.txt")); err != nil {
		t.Fatalf("registry file not created: %v", err)
	}
	// Second Add of the same path: no-op.
	added, err = Add("/some/repo")
	if err != nil {
		t.Fatalf("Add (dedupe): %v", err)
	}
	if added {
		t.Fatal("second Add of same path should report added=false")
	}
	// Add a second distinct path.
	if _, err := Add("/other/repo"); err != nil {
		t.Fatalf("Add second: %v", err)
	}
	got, err := List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 || got[0] != "/some/repo" || got[1] != "/other/repo" {
		t.Fatalf("List preserved insertion order? got %v", got)
	}
}

func TestRemove(t *testing.T) {
	withTempXDG(t)
	if _, err := Add("/a"); err != nil {
		t.Fatal(err)
	}
	if _, err := Add("/b"); err != nil {
		t.Fatal(err)
	}
	if _, err := Add("/c"); err != nil {
		t.Fatal(err)
	}
	removed, err := Remove("/b")
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if !removed {
		t.Fatal("Remove of present entry should report removed=true")
	}
	got, _ := List()
	if len(got) != 2 || got[0] != "/a" || got[1] != "/c" {
		t.Fatalf("after Remove: got %v", got)
	}
	// Remove non-existent: false, no error.
	removed, err = Remove("/missing")
	if err != nil {
		t.Fatalf("Remove (missing): %v", err)
	}
	if removed {
		t.Fatal("Remove of absent entry should report removed=false")
	}
}

func TestList_IgnoresCommentsAndBlanks(t *testing.T) {
	xdg := withTempXDG(t)
	dir := filepath.Join(xdg, "ash")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	contents := "# header\n/repo/one\n\n# inline comment\n/repo/two\n"
	if err := os.WriteFile(filepath.Join(dir, "installed-repos.txt"), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := List()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "/repo/one" || got[1] != "/repo/two" {
		t.Fatalf("comment-aware List: got %v", got)
	}
}
