package atomicwrite

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWrite_CreatesNewFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "new.txt")
	if err := Write(p, []byte("hello"), Options{}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "hello" {
		t.Errorf("content = %q, want hello", got)
	}
}

func TestWrite_ReplacesExisting(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f.txt")
	if err := os.WriteFile(p, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Write(p, []byte("new contents"), Options{}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, _ := os.ReadFile(p)
	if string(got) != "new contents" {
		t.Errorf("content = %q, want new contents", got)
	}
}

func TestWrite_NoLeftoverTempFile(t *testing.T) {
	dir := t.TempDir()
	if err := Write(filepath.Join(dir, "f.txt"), []byte("data"), Options{}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || entries[0].Name() != "f.txt" {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("dir contents = %v, want exactly [f.txt]", names)
	}
}

func TestWrite_PreserveMode(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f.txt")
	if err := os.WriteFile(p, []byte("old"), 0o640); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(p)
	if err := Write(p, []byte("new"), Options{PreserveMode: true}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	after, _ := os.Stat(p)
	if after.Mode() != before.Mode() {
		t.Errorf("mode = %v, want preserved %v", after.Mode(), before.Mode())
	}
}

func TestWrite_WithoutPreserveModeResetsTo0600(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f.txt")
	if err := os.WriteFile(p, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Write(p, []byte("new"), Options{}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	info, _ := os.Stat(p)
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600 (os.CreateTemp default, no PreserveMode)", info.Mode().Perm())
	}
}

func TestWrite_ErrorWhenParentMissing(t *testing.T) {
	p := filepath.Join(t.TempDir(), "does-not-exist", "f.txt")
	if err := Write(p, []byte("x"), Options{}); err == nil {
		t.Error("expected an error when the parent directory is missing")
	}
}
