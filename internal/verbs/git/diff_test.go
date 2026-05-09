package git

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// -- parseDiffNumstat unit tests ------------------------------------------

func TestParseDiffNumstat_Empty(t *testing.T) {
	res, perr := parseDiffNumstat(nil)
	if perr != nil {
		t.Fatal(perr)
	}
	if len(res.Files) != 0 || res.TotalAdditions != 0 || res.TotalDeletions != 0 {
		t.Errorf("expected empty result, got %+v", res)
	}
	if !res.StatOnly {
		t.Errorf("expected stat_only=true")
	}
}

func TestParseDiffNumstat_BasicLines(t *testing.T) {
	out := []byte("10\t3\tinternal/foo.go\n5\t0\tcmd/bar.go\n")
	res, perr := parseDiffNumstat(out)
	if perr != nil {
		t.Fatal(perr)
	}
	if len(res.Files) != 2 {
		t.Fatalf("want 2 files, got %d", len(res.Files))
	}
	if res.Files[0].Path != "internal/foo.go" {
		t.Errorf("path=%q want internal/foo.go", res.Files[0].Path)
	}
	if res.Files[0].Additions != 10 || res.Files[0].Deletions != 3 {
		t.Errorf("additions/deletions=%d/%d want 10/3", res.Files[0].Additions, res.Files[0].Deletions)
	}
	if res.TotalAdditions != 15 || res.TotalDeletions != 3 {
		t.Errorf("totals add=%d del=%d want 15/3", res.TotalAdditions, res.TotalDeletions)
	}
}

func TestParseDiffNumstat_BinaryFile(t *testing.T) {
	out := []byte("-\t-\tassets/logo.png\n")
	res, _ := parseDiffNumstat(out)
	if len(res.Files) != 1 {
		t.Fatalf("want 1 file, got %d", len(res.Files))
	}
	if !res.Files[0].Binary {
		t.Errorf("expected binary=true for logo.png")
	}
	if res.Files[0].Additions != 0 || res.Files[0].Deletions != 0 {
		t.Errorf("binary file should have 0 additions/deletions")
	}
}

// -- parseDiffUnified unit tests ------------------------------------------

func TestParseDiffUnified_Empty(t *testing.T) {
	res, perr := parseDiffUnified(nil, DiffDefaultLimitBytes)
	if perr != nil {
		t.Fatal(perr)
	}
	if len(res.Files) != 0 {
		t.Errorf("expected no files, got %d", len(res.Files))
	}
}

func TestParseDiffUnified_ModifiedFile(t *testing.T) {
	out := []byte(`diff --git a/internal/foo.go b/internal/foo.go
index abc1234..def5678 100644
--- a/internal/foo.go
+++ b/internal/foo.go
@@ -10,4 +10,5 @@ func Foo() {
 context
-old line
+new line
+extra line
 end
`)
	res, perr := parseDiffUnified(out, DiffDefaultLimitBytes)
	if perr != nil {
		t.Fatal(perr)
	}
	if len(res.Files) != 1 {
		t.Fatalf("want 1 file, got %d", len(res.Files))
	}
	f := res.Files[0]
	if f.Path != "internal/foo.go" {
		t.Errorf("path=%q want internal/foo.go", f.Path)
	}
	if f.Status != "M" {
		t.Errorf("status=%q want M", f.Status)
	}
	if f.Additions != 2 {
		t.Errorf("additions=%d want 2", f.Additions)
	}
	if f.Deletions != 1 {
		t.Errorf("deletions=%d want 1", f.Deletions)
	}
	if f.Patch == "" {
		t.Errorf("expected non-empty patch")
	}
	if res.TotalAdditions != 2 || res.TotalDeletions != 1 {
		t.Errorf("totals add=%d del=%d want 2/1", res.TotalAdditions, res.TotalDeletions)
	}
}

func TestParseDiffUnified_NewFile(t *testing.T) {
	out := []byte(`diff --git a/newfile.go b/newfile.go
new file mode 100644
index 0000000..abc1234
--- /dev/null
+++ b/newfile.go
@@ -0,0 +1,3 @@
+line1
+line2
+line3
`)
	res, _ := parseDiffUnified(out, DiffDefaultLimitBytes)
	if len(res.Files) != 1 {
		t.Fatalf("want 1 file, got %d", len(res.Files))
	}
	f := res.Files[0]
	if f.Status != "A" {
		t.Errorf("status=%q want A", f.Status)
	}
	if f.Additions != 3 || f.Deletions != 0 {
		t.Errorf("add/del=%d/%d want 3/0", f.Additions, f.Deletions)
	}
}

func TestParseDiffUnified_DeletedFile(t *testing.T) {
	out := []byte(`diff --git a/old.go b/old.go
deleted file mode 100644
index abc1234..0000000
--- a/old.go
+++ /dev/null
@@ -1,2 +0,0 @@
-line1
-line2
`)
	res, _ := parseDiffUnified(out, DiffDefaultLimitBytes)
	if len(res.Files) != 1 {
		t.Fatalf("want 1 file, got %d", len(res.Files))
	}
	f := res.Files[0]
	if f.Status != "D" {
		t.Errorf("status=%q want D", f.Status)
	}
	if f.Deletions != 2 {
		t.Errorf("deletions=%d want 2", f.Deletions)
	}
}

func TestParseDiffUnified_RenameFile(t *testing.T) {
	out := []byte(`diff --git a/old.go b/new.go
similarity index 90%
rename from old.go
rename to new.go
index abc1234..def5678 100644
--- a/old.go
+++ b/new.go
@@ -1,3 +1,4 @@
 context
+added
 end
`)
	res, _ := parseDiffUnified(out, DiffDefaultLimitBytes)
	if len(res.Files) != 1 {
		t.Fatalf("want 1 file, got %d", len(res.Files))
	}
	f := res.Files[0]
	if f.Status != "R" {
		t.Errorf("status=%q want R", f.Status)
	}
	if f.Path != "new.go" {
		t.Errorf("path=%q want new.go", f.Path)
	}
	if f.OldPath != "old.go" {
		t.Errorf("old_path=%q want old.go", f.OldPath)
	}
}

func TestParseDiffUnified_BinaryFile(t *testing.T) {
	out := []byte(`diff --git a/image.png b/image.png
index abc1234..def5678 100644
Binary files a/image.png and b/image.png differ
`)
	res, _ := parseDiffUnified(out, DiffDefaultLimitBytes)
	if len(res.Files) != 1 {
		t.Fatalf("want 1 file, got %d", len(res.Files))
	}
	if !res.Files[0].Binary {
		t.Errorf("expected binary=true")
	}
}

func TestParseDiffUnified_MultipleFiles(t *testing.T) {
	out := []byte(`diff --git a/a.go b/a.go
index 111..222 100644
--- a/a.go
+++ b/a.go
@@ -1,1 +1,2 @@
 unchanged
+added in a
diff --git a/b.go b/b.go
index 333..444 100644
--- a/b.go
+++ b/b.go
@@ -1,2 +1,1 @@
-removed from b
 unchanged
`)
	res, _ := parseDiffUnified(out, DiffDefaultLimitBytes)
	if len(res.Files) != 2 {
		t.Fatalf("want 2 files, got %d", len(res.Files))
	}
	if res.Files[0].Path != "a.go" || res.Files[1].Path != "b.go" {
		t.Errorf("paths=%q %q", res.Files[0].Path, res.Files[1].Path)
	}
	if res.TotalAdditions != 1 || res.TotalDeletions != 1 {
		t.Errorf("totals add=%d del=%d want 1/1", res.TotalAdditions, res.TotalDeletions)
	}
}

func TestParseDiffUnified_ByteCapDropsLaterPatches(t *testing.T) {
	// Build a diff with two files. We set the limit to cover only the first
	// file's patch so the second file's patch is omitted.
	file1Patch := "diff --git a/a.go b/a.go\nindex 1..2 100644\n--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n-old\n+new\n"
	file2Patch := "diff --git a/b.go b/b.go\nindex 3..4 100644\n--- a/b.go\n+++ b/b.go\n@@ -1 +1 @@\n-old\n+new\n"
	combined := file1Patch + file2Patch

	// Cap at exactly the first file's length — second file patch gets dropped.
	res, perr := parseDiffUnified([]byte(combined), len(file1Patch))
	if perr != nil {
		t.Fatal(perr)
	}
	if len(res.Files) != 2 {
		t.Fatalf("want 2 files (stats preserved), got %d", len(res.Files))
	}
	if res.Files[0].Patch == "" {
		t.Errorf("first file patch should be included")
	}
	if res.Files[1].Patch != "" {
		t.Errorf("second file patch should be omitted (byte cap)")
	}
	if !res.Truncated {
		t.Errorf("expected truncated=true")
	}
	// Stats for both files should still be present.
	if res.TotalAdditions != 2 || res.TotalDeletions != 2 {
		t.Errorf("totals add=%d del=%d want 2/2", res.TotalAdditions, res.TotalDeletions)
	}
}

// -- integration smoke test -----------------------------------------------

func TestRunDiff_Integration(t *testing.T) {
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not on PATH, skipping integration")
	}
	// This test asserts full patch text on an unstaged worktree change.
	// gogit backend returns counts-only for that mode (see
	// gogit_diff.go); pin to shellout for this case. The gogit
	// equivalent (counts present, Patch empty) is covered by a
	// separate test.
	prev := currentBackend()
	if err := SetBackend("shellout"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		switch prev {
		case backendShellout:
			_ = SetBackend("shellout")
		default:
			_ = SetBackend("go-git")
		}
	}()

	dir := t.TempDir()
	mustRun := func(name string, args ...string) {
		t.Helper()
		cmd := exec.Command(name, args...)
		cmd.Dir = dir
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("%s %v: %v\n%s", name, args, err, stderr.String())
		}
	}

	mustRun(gitBin, "init", "-q", "-b", "main")
	mustRun(gitBin, "config", "user.email", "test@example.com")
	mustRun(gitBin, "config", "user.name", "Test")

	// Commit a base file.
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(gitBin, "add", "main.go")
	mustRun(gitBin, "commit", "-q", "-m", "init")

	// Modify the file (unstaged).
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {\n\tprintln(\"hello\")\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	a := &Args{Op: "diff", Path: dir, Context: DiffDefaultContext, LimitBytes: DiffDefaultLimitBytes}

	// Full diff: unstaged changes.
	res, perr := runDiff(a, nil)
	if perr != nil {
		t.Fatalf("runDiff: %+v", perr)
	}
	if len(res.Files) != 1 {
		t.Fatalf("want 1 file, got %d", len(res.Files))
	}
	if res.Files[0].Path != "main.go" {
		t.Errorf("path=%q want main.go", res.Files[0].Path)
	}
	if res.Files[0].Additions == 0 {
		t.Errorf("expected additions > 0")
	}
	if res.Files[0].Patch == "" {
		t.Errorf("expected non-empty patch")
	}

	// Stat-only mode.
	a.StatOnly = true
	statRes, perr := runDiff(a, nil)
	if perr != nil {
		t.Fatalf("runDiff stat: %+v", perr)
	}
	if !statRes.StatOnly {
		t.Errorf("expected stat_only=true")
	}
	if len(statRes.Files) != 1 {
		t.Fatalf("stat: want 1 file, got %d", len(statRes.Files))
	}
	if statRes.Files[0].Patch != "" {
		t.Errorf("stat mode should not include patch text")
	}

	// Staged diff (nothing staged → empty result).
	a.StatOnly = false
	a.Staged = true
	stagedRes, perr := runDiff(a, nil)
	if perr != nil {
		t.Fatalf("runDiff staged: %+v", perr)
	}
	if len(stagedRes.Files) != 0 {
		t.Errorf("expected 0 staged files, got %d", len(stagedRes.Files))
	}

	// Range diff: HEAD~1..HEAD using a two-commit repo.
	a.Staged = false
	a.Range = "HEAD~1..HEAD"

	// Create a second commit first.
	mustRun(gitBin, "add", "main.go")
	mustRun(gitBin, "commit", "-q", "-m", "add println")

	rangeRes, perr := runDiff(a, nil)
	if perr != nil {
		t.Fatalf("runDiff range: %+v", perr)
	}
	if len(rangeRes.Files) != 1 {
		t.Fatalf("range: want 1 file, got %d", len(rangeRes.Files))
	}
}

func TestRunDiff_NotARepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	a := &Args{Op: "diff", Path: dir, Context: DiffDefaultContext, LimitBytes: DiffDefaultLimitBytes}
	_, perr := runDiff(a, nil)
	if perr == nil {
		t.Fatal("expected error for non-repo dir")
	}
	if perr.Code != "not_a_repo" {
		t.Errorf("code=%q want not_a_repo", perr.Code)
	}
}
