package git

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	// ASH-150: dropped files set PatchTruncated; the file that fit does not.
	if res.Files[0].PatchTruncated {
		t.Errorf("first file patch_truncated should be false (full include)")
	}
	if !res.Files[1].PatchTruncated {
		t.Errorf("second file patch_truncated should be true (whole-file drop)")
	}
	if !res.Truncated {
		t.Errorf("expected truncated=true")
	}
	// Stats for both files should still be present.
	if res.TotalAdditions != 2 || res.TotalDeletions != 2 {
		t.Errorf("totals add=%d del=%d want 2/2", res.TotalAdditions, res.TotalDeletions)
	}
}

// TestParseDiffUnified_HunkLevelTruncation covers ASH-150's multi-hunk
// overflow path: a file's patch has 3 hunks, the budget fits 2, the
// third is dropped with a structured sentinel.
func TestParseDiffUnified_HunkLevelTruncation(t *testing.T) {
	patch := "" +
		"diff --git a/big.go b/big.go\n" +
		"index 1..2 100644\n" +
		"--- a/big.go\n" +
		"+++ b/big.go\n" +
		"@@ -1,3 +1,3 @@\n one\n-two\n+TWO\n three\n" +
		"@@ -10,3 +10,3 @@\n ten\n-eleven\n+ELEVEN\n twelve\n" +
		"@@ -20,3 +20,3 @@\n twenty\n-twentyone\n+TWENTYONE\n twentytwo\n"

	// Compute a budget that fits the header + first two hunks but not the third.
	header, hunks := splitPatchHunks(patch)
	if len(hunks) != 3 {
		t.Fatalf("fixture should split into 3 hunks, got %d", len(hunks))
	}
	budget := len(header) + len(hunks[0]) + len(hunks[1])

	res, perr := parseDiffUnified([]byte(patch), budget)
	if perr != nil {
		t.Fatal(perr)
	}
	if len(res.Files) != 1 {
		t.Fatalf("want 1 file, got %d", len(res.Files))
	}
	f := res.Files[0]
	if !f.PatchTruncated {
		t.Errorf("expected PatchTruncated=true")
	}
	if f.HunksTotal != 3 || f.HunksShown != 2 {
		t.Errorf("want hunks_total=3 hunks_shown=2; got %d / %d", f.HunksTotal, f.HunksShown)
	}
	if f.ContextElided {
		t.Errorf("context_elided should be false for multi-hunk overflow")
	}
	if !strings.Contains(f.Patch, "[hunks: 2 shown, 1 omitted") {
		t.Errorf("expected hunks-omitted sentinel, got patch:\n%s", f.Patch)
	}
	// Stats reflect the FULL upstream patch.
	if f.Additions != 3 || f.Deletions != 3 {
		t.Errorf("stats add=%d del=%d want 3/3 (full file)", f.Additions, f.Deletions)
	}
}

// TestParseDiffUnified_ContextElide covers ASH-150's single-huge-hunk
// path: one hunk that alone exceeds the budget gets its context lines
// stripped, leaving only +/- changes plus a sentinel.
func TestParseDiffUnified_ContextElide(t *testing.T) {
	// Build a hunk with one + and one -, surrounded by lots of context.
	var bodyB strings.Builder
	bodyB.WriteString("@@ -1,40 +1,40 @@\n")
	for i := 0; i < 20; i++ {
		bodyB.WriteString(" context line that is reasonably long to inflate the byte count\n")
	}
	bodyB.WriteString("-old changed line\n+new changed line\n")
	for i := 0; i < 19; i++ {
		bodyB.WriteString(" more context that pads the hunk size out further\n")
	}
	hunk := bodyB.String()
	patch := "diff --git a/json.json b/json.json\nindex 1..2 100644\n--- a/json.json\n+++ b/json.json\n" + hunk

	// Budget: enough for header + the elided hunk, but not the full hunk.
	header, _ := splitPatchHunks(patch)
	elided, _, _, ok := elideHunkContext(hunk)
	if !ok {
		t.Fatalf("elideHunkContext should succeed")
	}
	// Pad a little for the sentinel we append after the elided body.
	budget := len(header) + len(elided) + 120

	res, perr := parseDiffUnified([]byte(patch), budget)
	if perr != nil {
		t.Fatal(perr)
	}
	if len(res.Files) != 1 {
		t.Fatalf("want 1 file, got %d", len(res.Files))
	}
	f := res.Files[0]
	if !f.PatchTruncated {
		t.Errorf("expected PatchTruncated=true")
	}
	if !f.ContextElided {
		t.Errorf("expected ContextElided=true for single-huge-hunk")
	}
	if f.HunksTotal != 1 || f.HunksShown != 1 {
		t.Errorf("want hunks_total=1 hunks_shown=1; got %d / %d", f.HunksTotal, f.HunksShown)
	}
	if !strings.Contains(f.Patch, "context lines elided") {
		t.Errorf("expected context-elided sentinel in patch:\n%s", f.Patch)
	}
	if !strings.Contains(f.Patch, "-old changed line") || !strings.Contains(f.Patch, "+new changed line") {
		t.Errorf("changed lines should survive elision:\n%s", f.Patch)
	}
}

// TestParseDiffUnified_SingleLineJSONOverflow is the regression fixture
// from the ASH-150 ticket: a single-line JSON file whose diff expands
// into a huge hunk via context. The new path returns a useful partial
// patch, not a bare stat line.
func TestParseDiffUnified_SingleLineJSONOverflow(t *testing.T) {
	// Synthesize a one-line-JSON-style diff: one massive +/- pair with
	// no surrounding context (single line, full-file replacement).
	oldBlob := strings.Repeat("x", 200<<10) // 200 KiB of x's
	newBlob := strings.Repeat("y", 200<<10)
	patch := "diff --git a/inventory.json b/inventory.json\n" +
		"index 1..2 100644\n" +
		"--- a/inventory.json\n" +
		"+++ b/inventory.json\n" +
		"@@ -1 +1 @@\n" +
		"-" + oldBlob + "\n" +
		"+" + newBlob + "\n"

	// Budget of 4 KiB — old behavior would mid-slice the 200 KiB +/-
	// lines and emit garbage. New behavior: drop the whole patch
	// (single hunk doesn't fit even after elision since +/- ARE the
	// changes) and mark PatchTruncated so the caller knows.
	res, perr := parseDiffUnified([]byte(patch), 4096)
	if perr != nil {
		t.Fatal(perr)
	}
	if len(res.Files) != 1 {
		t.Fatalf("want 1 file, got %d", len(res.Files))
	}
	f := res.Files[0]
	if !f.PatchTruncated {
		t.Errorf("expected PatchTruncated=true for over-budget single-line JSON")
	}
	// Either we got 0 hunks shown (whole drop) or 1 (context-elided)
	// — both are valid graceful-degradation outcomes. What is NOT
	// valid is a mid-line slice.
	if !strings.Contains(f.Patch, "hunks:") && f.HunksShown != 1 {
		t.Errorf("expected hunks sentinel or context-elided fragment, got:\n%s", f.Patch)
	}
	// Stats reflect the FULL upstream change regardless of patch shape.
	if f.Additions == 0 || f.Deletions == 0 {
		t.Errorf("stats lost; got +%d -%d", f.Additions, f.Deletions)
	}
}

func TestSplitPatchHunks_NoHunks(t *testing.T) {
	patch := "diff --git a/x.png b/x.png\nindex 1..2 100644\nBinary files a/x.png and b/x.png differ\n"
	header, hunks := splitPatchHunks(patch)
	if header != patch {
		t.Errorf("header should equal full patch when no hunks")
	}
	if len(hunks) != 0 {
		t.Errorf("want 0 hunks, got %d", len(hunks))
	}
}

func TestElideHunkContext_KeepsChanges(t *testing.T) {
	hunk := "@@ -1,5 +1,5 @@\n a\n b\n-OLD\n+NEW\n c\n d\n"
	out, kept, elided, ok := elideHunkContext(hunk)
	if !ok {
		t.Fatalf("elideHunkContext returned ok=false on a valid hunk")
	}
	if kept != 2 {
		t.Errorf("want kept=2 (one + one -), got %d", kept)
	}
	if elided != 4 {
		t.Errorf("want elided=4 context lines, got %d", elided)
	}
	if !strings.Contains(out, "-OLD") || !strings.Contains(out, "+NEW") {
		t.Errorf("changed lines missing from elided output:\n%s", out)
	}
	if strings.Contains(out, " a\n") || strings.Contains(out, " b\n") {
		t.Errorf("context line survived elision:\n%s", out)
	}
}

// -- integration smoke test -----------------------------------------------

func TestRunDiff_Integration(t *testing.T) {
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not on PATH, skipping integration")
	}
	// ASH-66: both backends now produce full patch text for unstaged
	// worktree changes, so this test runs against whichever backend is
	// active. No shellout pin needed.
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
