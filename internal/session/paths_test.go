package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRoot_FindsGoModMarker(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(dir, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := Root(sub)
	if err != nil {
		t.Fatalf("Root: %v", err)
	}
	if got != dir {
		t.Errorf("Root = %q, want %q (go.mod marker)", got, dir)
	}
}

func TestRoot_NoMarkerReturnsStart(t *testing.T) {
	sub := filepath.Join(t.TempDir(), "deep")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := Root(sub)
	if err != nil {
		t.Fatalf("Root: %v", err)
	}
	if got != sub {
		t.Errorf("Root = %q, want %q (no marker -> start)", got, sub)
	}
}

func TestSocketPath_StableAndScoped(t *testing.T) {
	a := SocketPath("/project/one")
	if a != SocketPath("/project/one") {
		t.Error("SocketPath is not stable for the same root")
	}
	if a == SocketPath("/project/two") {
		t.Error("distinct roots produced the same socket path")
	}
	if !strings.HasSuffix(a, ".sock") || !strings.Contains(filepath.Base(a), "ash-") {
		t.Errorf("unexpected socket path shape: %q", a)
	}
}

func TestProjectPaths(t *testing.T) {
	root := "/x/y"
	for _, c := range []struct{ name, got, want string }{
		{"ledger", LedgerPath(root), filepath.Join(root, ".ash", "ledger.db")},
		{"langcache", LangCachePath(root), filepath.Join(root, ".ash", "lang-cache.db")},
		{"pid", PIDPath(root), filepath.Join(root, ".ash", "ashd.pid")},
		{"log", LogPath(root), filepath.Join(root, ".ash", "ashd.log")},
	} {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.name, c.got, c.want)
		}
	}
}

func TestEnsureRuntimeDirs(t *testing.T) {
	root := t.TempDir()
	if err := EnsureRuntimeDirs(root); err != nil {
		t.Fatalf("EnsureRuntimeDirs: %v", err)
	}
	if fi, err := os.Stat(filepath.Join(root, ".ash")); err != nil || !fi.IsDir() {
		t.Errorf(".ash dir not created: err=%v", err)
	}
}
