// Blame implementation as the fifth op on the git verb. Returns the
// authorship of each line in a file, run-compacted so consecutive lines
// sharing a commit collapse into one BlameHunk — roughly 4.5x cheaper
// in cl100k tokens than the naive per-line shape on typical files
// (~200 lines, ~30 distinct commits).
//
// Backends: go-git only in v1. The shellout path returns not_implemented;
// if real demand for rename-following blame appears later, the trigger
// to add `git blame --porcelain` parsing is real.

package git

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/stazelabs/ash/internal/proto"
)

// Blame caps. The line cap guards against runaway blame on generated /
// vendored files; the byte cap caps serialized hunk output.
const (
	BlameMaxLines     = 8000
	BlameDefaultBytes = 256 * 1024
	BlameMaxBytes     = 4 * 1024 * 1024
)

// BlameResult is the typed payload for op=blame.
type BlameResult struct {
	Path      string           `msgpack:"path"`
	Rev       string           `msgpack:"rev"`
	Hunks     []BlameHunk      `msgpack:"hunks"`
	Truncated bool             `msgpack:"truncated,omitempty"`
	TruncInfo *proto.TruncInfo `msgpack:"trunc,omitempty"`
}

// BlameHunk is a run of consecutive lines sharing the same commit.
// AuthorEmail and per-commit Subject are deliberately omitted: recover
// either via `ash git --op show --ref <sha>`.
type BlameHunk struct {
	SHA        string   `msgpack:"sha"`
	ShortSHA   string   `msgpack:"short"`
	AuthorName string   `msgpack:"aname"`
	AuthorTime int64    `msgpack:"atime"`
	StartLine  int      `msgpack:"start"`
	Lines      []string `msgpack:"lines"`
}

// blameLine is the intermediate per-line tuple the backend builds.
// Kept unexported so compactBlameLines stays decoupled from go-git's
// own Line type and is testable without a real repo.
type blameLine struct {
	SHA        string
	AuthorName string
	AuthorTime int64
	Text       string
}

// parseBlameLines parses the --lines start:end spec, returning ok=false
// when the spec is empty (whole file). Open endpoints are allowed:
// "5:" → start at 5, end at file end; ":10" → lines 1 through 10. Errors
// return a typed args error.
func parseBlameLines(spec string) (start, end int, ok bool, perr *proto.Error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return 0, 0, false, nil
	}
	parts := strings.SplitN(spec, ":", 2)
	if len(parts) != 2 {
		return 0, 0, false, &proto.Error{Code: "args", Msg: "invalid --lines: " + spec + " (expected start:end)"}
	}
	if parts[0] != "" {
		s, err := strconv.Atoi(parts[0])
		if err != nil || s < 1 {
			return 0, 0, false, &proto.Error{Code: "args", Msg: "invalid --lines start: " + parts[0]}
		}
		start = s
	}
	if parts[1] != "" {
		e, err := strconv.Atoi(parts[1])
		if err != nil || e < 1 {
			return 0, 0, false, &proto.Error{Code: "args", Msg: "invalid --lines end: " + parts[1]}
		}
		end = e
	}
	if start > 0 && end > 0 && end < start {
		return 0, 0, false, &proto.Error{Code: "args", Msg: fmt.Sprintf("invalid --lines: end %d < start %d", end, start)}
	}
	return start, end, true, nil
}

// compactBlameLines collapses runs of consecutive lines sharing the
// same SHA into a single BlameHunk. startLineOffset is the 1-based file
// line of lines[0]; subsequent hunks compute StartLine from it.
func compactBlameLines(lines []blameLine, startLineOffset int) []BlameHunk {
	if len(lines) == 0 {
		return nil
	}
	if startLineOffset < 1 {
		startLineOffset = 1
	}
	hunks := make([]BlameHunk, 0, 8)
	cur := newHunkFrom(lines[0], startLineOffset)
	for i := 1; i < len(lines); i++ {
		if lines[i].SHA == cur.SHA {
			cur.Lines = append(cur.Lines, lines[i].Text)
			continue
		}
		hunks = append(hunks, cur)
		cur = newHunkFrom(lines[i], startLineOffset+i)
	}
	hunks = append(hunks, cur)
	return hunks
}

func newHunkFrom(l blameLine, startLine int) BlameHunk {
	return BlameHunk{
		SHA:        l.SHA,
		ShortSHA:   shortSHAFor(l.SHA),
		AuthorName: l.AuthorName,
		AuthorTime: l.AuthorTime,
		StartLine:  startLine,
		Lines:      []string{l.Text},
	}
}

func shortSHAFor(sha string) string {
	if len(sha) >= 7 {
		return sha[:7]
	}
	return sha
}

// applyBlameByteCap drops trailing hunks once the cumulative serialized
// size exceeds limit. Always retains at least one hunk so the agent
// gets a valid (if heavily truncated) result.
func applyBlameByteCap(hunks []BlameHunk, limit int) ([]BlameHunk, bool, *proto.TruncInfo) {
	if limit <= 0 || len(hunks) == 0 {
		return hunks, false, nil
	}
	var bytes int
	for i := range hunks {
		hunkBytes := 80 // sha + author + numbers overhead
		for _, ln := range hunks[i].Lines {
			hunkBytes += len(ln) + 1
		}
		if bytes+hunkBytes > limit {
			if i == 0 {
				return hunks[:1], true, &proto.TruncInfo{Trunc: len(hunks) - 1, Limit: limit, Max: BlameMaxBytes}
			}
			return hunks[:i], true, &proto.TruncInfo{Trunc: len(hunks) - i, Limit: limit, Max: BlameMaxBytes}
		}
		bytes += hunkBytes
	}
	return hunks, false, nil
}

// prettyBlame renders the run-compacted blame for human reading.
//
// Header: §git blame: <path> @ <short-rev> (N hunks, M lines[, TRUNCATED])
// Body: per hunk, one author line followed by indented source lines.
func prettyBlame(b *BlameResult) string {
	if b == nil {
		return "ok\n<empty blame>"
	}
	var sb strings.Builder
	totalLines := 0
	for _, h := range b.Hunks {
		totalLines += len(h.Lines)
	}
	short := shortSHAFor(b.Rev)
	fmt.Fprintf(&sb, "§git blame: %s @ %s (%d hunk", b.Path, short, len(b.Hunks))
	if len(b.Hunks) != 1 {
		sb.WriteByte('s')
	}
	fmt.Fprintf(&sb, ", %d line", totalLines)
	if totalLines != 1 {
		sb.WriteByte('s')
	}
	if b.Truncated {
		sb.WriteString(", TRUNCATED")
	}
	sb.WriteString(")\n")
	for _, h := range b.Hunks {
		end := h.StartLine + len(h.Lines) - 1
		date := time.Unix(0, h.AuthorTime).UTC().Format("2006-01-02")
		fmt.Fprintf(&sb, "%s  %s  %s  L%d-%d\n", h.ShortSHA, h.AuthorName, date, h.StartLine, end)
		for _, ln := range h.Lines {
			sb.WriteString("  ")
			sb.WriteString(ln)
			sb.WriteByte('\n')
		}
	}
	if b.Truncated && b.TruncInfo != nil {
		fmt.Fprintf(&sb, "\n[truncated %d hunks, limit %d bytes]\n", b.TruncInfo.Trunc, b.TruncInfo.Limit)
	}
	return strings.TrimRight(sb.String(), "\n")
}
