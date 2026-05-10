package git

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stazelabs/ash/internal/proto"
)

// makeRecord builds a single commit record matching runLog's --format
// (NUL-separated commits, newline-separated fields). Tests can compose
// these into a stream without spawning git.
func makeRecord(sha, short, atime, aname, amail, ctime, cname, cmail, parents, subject, body string) string {
	return strings.Join([]string{
		sha, short, atime, aname, amail, ctime, cname, cmail, parents, subject, body,
	}, "\n")
}

func TestParseLog_SingleCommit(t *testing.T) {
	rec := makeRecord(
		"d31dd0a1d164161c929a327c999fedae60c12b9d",
		"d31dd0a",
		"1715128800",
		"Chris", "chris@example.com",
		"1715128801",
		"Chris", "chris@example.com",
		"abc123",
		"add foo",
		"")
	out := []byte(rec + "\x00")
	res, perr := parseLog(out, 20)
	if perr != nil {
		t.Fatal(perr)
	}
	if res.Count != 1 || len(res.Commits) != 1 {
		t.Fatalf("count=%d, commits=%d", res.Count, len(res.Commits))
	}
	c := res.Commits[0]
	if c.SHA != "d31dd0a1d164161c929a327c999fedae60c12b9d" {
		t.Errorf("sha=%q", c.SHA)
	}
	if c.ShortSHA != "d31dd0a" {
		t.Errorf("short=%q", c.ShortSHA)
	}
	if c.AuthorName != "Chris" || c.AuthorEmail != "chris@example.com" {
		t.Errorf("author=%q <%q>", c.AuthorName, c.AuthorEmail)
	}
	if c.Subject != "add foo" {
		t.Errorf("subject=%q", c.Subject)
	}
	if c.Body != "" {
		t.Errorf("body=%q want empty", c.Body)
	}
	if len(c.Parents) != 1 || c.Parents[0] != "abc123" {
		t.Errorf("parents=%v", c.Parents)
	}
	// 1715128800 unix seconds = 1715128800 * 1e9 nanos.
	if c.AuthorTime != 1715128800*1_000_000_000 {
		t.Errorf("author_time=%d", c.AuthorTime)
	}
	if res.Truncated {
		t.Errorf("single commit shouldn't be truncated")
	}
}

func TestParseLog_BodyKeepsEmbeddedNewlines(t *testing.T) {
	body := "line one\nline two\n\nline four"
	rec := makeRecord(
		"sha1", "sha1abc", "1", "a", "a@x", "1", "a", "a@x", "",
		"subject only",
		body,
	)
	out := []byte(rec + "\x00")
	res, _ := parseLog(out, 20)
	if len(res.Commits) != 1 {
		t.Fatalf("expected 1 commit, got %d", len(res.Commits))
	}
	if res.Commits[0].Body != body {
		t.Errorf("body lost newlines: %q", res.Commits[0].Body)
	}
	if res.Commits[0].Subject != "subject only" {
		t.Errorf("subject leaked into body: %q", res.Commits[0].Subject)
	}
}

func TestParseLog_RootCommitHasNoParents(t *testing.T) {
	rec := makeRecord("sha1", "s", "1", "a", "a@x", "1", "a", "a@x", "", "init", "")
	out := []byte(rec + "\x00")
	res, _ := parseLog(out, 20)
	if len(res.Commits[0].Parents) != 0 {
		t.Errorf("root commit should have no parents, got %v", res.Commits[0].Parents)
	}
}

func TestParseLog_MergeHasMultipleParents(t *testing.T) {
	rec := makeRecord("sha1", "s", "1", "a", "a@x", "1", "a", "a@x",
		"p1aaa p2bbb", "merge feature into main", "")
	out := []byte(rec + "\x00")
	res, _ := parseLog(out, 20)
	if len(res.Commits[0].Parents) != 2 {
		t.Fatalf("merge parents: %v", res.Commits[0].Parents)
	}
	if res.Commits[0].Parents[0] != "p1aaa" || res.Commits[0].Parents[1] != "p2bbb" {
		t.Errorf("parents=%v", res.Commits[0].Parents)
	}
}

func TestParseLog_MultipleCommits(t *testing.T) {
	var buf bytes.Buffer
	for i := 0; i < 3; i++ {
		buf.WriteString(makeRecord(
			fmt.Sprintf("sha%d", i),
			fmt.Sprintf("s%d", i),
			fmt.Sprintf("%d", 1715128800+i),
			"Chris", "chris@example.com",
			fmt.Sprintf("%d", 1715128800+i),
			"Chris", "chris@example.com",
			"",
			fmt.Sprintf("commit %d", i),
			"",
		))
		buf.WriteByte(0)
	}
	res, _ := parseLog(buf.Bytes(), 20)
	if res.Count != 3 || len(res.Commits) != 3 {
		t.Fatalf("count=%d commits=%d", res.Count, len(res.Commits))
	}
	for i, c := range res.Commits {
		if c.Subject != fmt.Sprintf("commit %d", i) {
			t.Errorf("[%d] subject=%q", i, c.Subject)
		}
	}
}

func TestParseLog_TruncatesAtLimit(t *testing.T) {
	// Stream limit+1 commits to force truncation; parser should emit the
	// first N and flag the result.
	var buf bytes.Buffer
	const limit = 3
	for i := 0; i < limit+1; i++ {
		buf.WriteString(makeRecord(
			fmt.Sprintf("sha%d", i), "s", "1", "a", "a@x", "1", "a", "a@x", "",
			fmt.Sprintf("c%d", i), ""))
		buf.WriteByte(0)
	}
	res, _ := parseLog(buf.Bytes(), limit)
	if !res.Truncated {
		t.Errorf("expected truncated=true with limit=%d and limit+1 records", limit)
	}
	if res.Count != limit {
		t.Errorf("count=%d want %d", res.Count, limit)
	}
	if res.TruncInfo == nil {
		t.Fatal("TruncInfo should be set when truncated")
	}
	if res.TruncInfo.Limit != 3 {
		t.Errorf("TruncInfo.Limit=%d want 3", res.TruncInfo.Limit)
	}
	// Below hard cap: Limit < Max means raising --limit is still possible.
	if res.TruncInfo.Limit >= res.TruncInfo.Max {
		t.Errorf("below cap: Limit=%d should be < Max=%d", res.TruncInfo.Limit, res.TruncInfo.Max)
	}
}

func TestLogTruncationHint_AtHardCap(t *testing.T) {
	// ASH-12: Limit==Max signals no raise possible; hint must say so.
	ti := &proto.TruncInfo{Trunc: 1, Limit: LogMaxLimit, Max: LogMaxLimit}
	hint := logTruncHint(ti)
	if strings.Contains(hint, "raise --limit") {
		t.Errorf("hint at cap must not suggest raising: %q", hint)
	}
	if !strings.Contains(hint, "hard cap") || !strings.Contains(hint, "narrow") {
		t.Errorf("hint at cap should mention cap and suggest narrowing: %q", hint)
	}
}

func TestParseLog_EmptyStreamIsEmptyResult(t *testing.T) {
	res, perr := parseLog(nil, 20)
	if perr != nil {
		t.Fatal(perr)
	}
	if res.Count != 0 || len(res.Commits) != 0 {
		t.Errorf("expected empty, got %+v", res)
	}
}

// -- ParseArgs additions for log -----------------------------------------

func TestParseArgs_LogDefaults(t *testing.T) {
	a, perr := ParseArgs(map[string]any{"op": "log"})
	if perr != nil {
		t.Fatalf("unexpected: %+v", perr)
	}
	if a.Limit != LogDefaultLimit {
		t.Errorf("limit default: %d want %d", a.Limit, LogDefaultLimit)
	}
	if a.Path != "." {
		t.Errorf("path default: %q want .", a.Path)
	}
}

func TestParseArgs_LogLimitClamped(t *testing.T) {
	a, _ := ParseArgs(map[string]any{"op": "log", "limit": LogMaxLimit + 100})
	if a.Limit != LogMaxLimit {
		t.Errorf("limit not clamped: %d", a.Limit)
	}
}

func TestParseArgs_LogStringsPassThrough(t *testing.T) {
	a, perr := ParseArgs(map[string]any{
		"op":       "log",
		"range":    "HEAD~5..HEAD",
		"author":   "Chris",
		"since":    "1 week ago",
		"until":    "2026-01-01",
		"pathspec": "src/",
	})
	if perr != nil {
		t.Fatal(perr)
	}
	if a.Range != "HEAD~5..HEAD" || a.Author != "Chris" ||
		a.Since != "1 week ago" || a.Until != "2026-01-01" ||
		a.Pathspec != "src/" {
		t.Errorf("strings not preserved: %+v", a)
	}
}

// -- integration smoke ----------------------------------------------------

func TestRunLog_Integration(t *testing.T) {
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not on PATH, skipping integration")
	}
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

	for i := 0; i < 3; i++ {
		path := filepath.Join(dir, fmt.Sprintf("f%d.txt", i))
		if err := os.WriteFile(path, []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		mustRun(gitBin, "add", ".")
		mustRun(gitBin, "commit", "-q", "-m", fmt.Sprintf("commit %d", i))
	}

	res, perr := runLog(&Args{Op: "log", Path: dir, Limit: 10}, nil)
	if perr != nil {
		t.Fatalf("runLog: %+v", perr)
	}
	if res.Count != 3 {
		t.Fatalf("expected 3 commits, got %d", res.Count)
	}
	// log is reverse-chronological: newest first.
	if res.Commits[0].Subject != "commit 2" {
		t.Errorf("newest commit subject=%q want %q", res.Commits[0].Subject, "commit 2")
	}
	if res.Commits[2].Subject != "commit 0" {
		t.Errorf("oldest commit subject=%q want %q", res.Commits[2].Subject, "commit 0")
	}
	// Author propagated from `git config`.
	if res.Commits[0].AuthorEmail != "test@example.com" {
		t.Errorf("author_email=%q", res.Commits[0].AuthorEmail)
	}
	// Root commit has no parents; later commits do.
	if len(res.Commits[2].Parents) != 0 {
		t.Errorf("root commit has parents: %v", res.Commits[2].Parents)
	}
	if len(res.Commits[1].Parents) != 1 || res.Commits[1].Parents[0] == "" {
		t.Errorf("non-root commit missing parent: %+v", res.Commits[1])
	}
}

func TestRunLog_EmptyRepo(t *testing.T) {
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	cmd := exec.Command(gitBin, "init", "-q", "-b", "main")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	// Empty repo should yield empty success, not an error.
	res, perr := runLog(&Args{Op: "log", Path: dir, Limit: 10}, nil)
	if perr != nil {
		t.Fatalf("expected empty success, got error: %+v", perr)
	}
	if res.Count != 0 {
		t.Errorf("expected 0 commits in empty repo, got %d", res.Count)
	}
}

func TestRunLog_NotARepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	_, perr := runLog(&Args{Op: "log", Path: dir, Limit: 10}, nil)
	if perr == nil {
		t.Fatal("expected error for non-repo dir")
	}
	if perr.Code != "not_a_repo" {
		t.Errorf("code=%q want not_a_repo (msg=%q)", perr.Code, perr.Msg)
	}
}
