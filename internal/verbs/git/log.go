package git

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/stazelabs/ash/internal/proto"
	"github.com/stazelabs/ash/internal/runner"
	"github.com/stazelabs/ash/internal/verbs/argutil"
)

const (
	LogDefaultLimit = 20
	LogMaxLimit     = 200

	// logFormat encodes a commit as 11 newline-separated fields. Fields
	// 0-9 are single-line; field 10 (body) may contain newlines, so it
	// must be the last field. Records are NUL-separated via `git log -z`,
	// so embedded newlines in the body are unambiguous.
	//
	// Fields, in order:
	//   0  %H   full hash
	//   1  %h   abbreviated hash
	//   2  %at  author time, unix seconds
	//   3  %an  author name
	//   4  %ae  author email
	//   5  %ct  committer time, unix seconds
	//   6  %cn  committer name
	//   7  %ce  committer email
	//   8  %P   parent hashes, space-separated (empty for root commit)
	//   9  %s   subject (first line of message)
	//   10 %b   body (rest of message; may be empty or multi-line)
	logFormat = "%H%n%h%n%at%n%an%n%ae%n%ct%n%cn%n%ce%n%P%n%s%n%b"
	logFields = 11
)

// LogResult is the structured replacement for `git log` text scraping.
type LogResult struct {
	Commits        []Commit `msgpack:"commits,omitempty"`
	Count          int      `msgpack:"count"`
	Truncated      bool     `msgpack:"truncated,omitempty"`
	TruncationHint string   `msgpack:"truncation_hint,omitempty"`
}

// Commit captures one revision. Times are unix nanoseconds (consistent
// with mtime in find/read records). Parents are full SHAs of every
// parent (empty for root commits, multiple for merges).
type Commit struct {
	SHA            string   `msgpack:"sha"`
	ShortSHA       string   `msgpack:"short_sha"`
	AuthorName     string   `msgpack:"author_name"`
	AuthorEmail    string   `msgpack:"author_email"`
	AuthorTime     int64    `msgpack:"author_time"`
	CommitterName  string   `msgpack:"committer_name,omitempty"`
	CommitterEmail string   `msgpack:"committer_email,omitempty"`
	CommitterTime  int64    `msgpack:"committer_time,omitempty"`
	Subject        string   `msgpack:"subject"`
	Body           string   `msgpack:"body,omitempty"`
	Parents        []string `msgpack:"parents,omitempty"`
}

func runLog(a *Args, tr *proto.Tracer) (*LogResult, *proto.Error) {
	gitArgs := []string{"-C", a.Path, "log", "-z", "--format=" + logFormat,
		"-n", strconv.Itoa(a.Limit + 1), // +1 so we can detect truncation cheaply
	}
	if a.Author != "" {
		gitArgs = append(gitArgs, "--author="+a.Author)
	}
	if a.Since != "" {
		gitArgs = append(gitArgs, "--since="+a.Since)
	}
	if a.Until != "" {
		gitArgs = append(gitArgs, "--until="+a.Until)
	}
	if a.Range != "" {
		gitArgs = append(gitArgs, a.Range)
	}
	if a.Pathspec != "" {
		// "--" separates rev args from pathspec; required even with one
		// pathspec, since git might otherwise misread the pathspec as a rev.
		gitArgs = append(gitArgs, "--", a.Pathspec)
	}

	res, perr := runner.Run("git", gitArgs, runner.Opts{Tracer: tr})
	if perr != nil {
		return nil, perr
	}
	if res.ExitCode != 0 {
		msg := strings.TrimSpace(string(res.Stderr))
		lower := strings.ToLower(msg)
		if strings.Contains(lower, "not a git repository") {
			return nil, &proto.Error{Code: "not_a_repo", Msg: a.Path + " is not inside a git repository"}
		}
		// Empty repo: git log exits 128 with a message containing
		// "any commits" (variants: "does not yet have any commits",
		// "does not have any commits yet"). Treat as a successful empty
		// result rather than an error, since "log of empty repo" is a
		// normal state agents shouldn't have to special-case.
		if strings.Contains(lower, "any commits") ||
			strings.Contains(lower, "bad default revision 'head'") {
			return &LogResult{Count: 0}, nil
		}
		if msg == "" {
			msg = fmt.Sprintf("git exited %d", res.ExitCode)
		}
		return nil, &proto.Error{Code: "git_failed", Msg: msg}
	}
	return parseLog(res.Stdout, a.Limit)
}

// parseLog is a pure function over the NUL-separated commit stream
// produced by runLog's --format. Asks for limit+1 commits and truncates
// to limit; if the (limit+1)th came back, the result is marked truncated.
func parseLog(out []byte, limit int) (*LogResult, *proto.Error) {
	res := &LogResult{}
	// Trim a trailing NUL if present so the final split chunk isn't empty.
	body := bytes.TrimRight(out, "\x00")
	if len(body) == 0 {
		return res, nil
	}
	records := bytes.Split(body, []byte{0})
	for _, rec := range records {
		if len(rec) == 0 {
			continue
		}
		c, ok := parseCommit(rec)
		if !ok {
			continue
		}
		res.Commits = append(res.Commits, c)
	}

	if len(res.Commits) > limit {
		res.Commits = res.Commits[:limit]
		res.Truncated = true
		res.TruncationHint = logTruncationHint(limit)
	}
	res.Count = len(res.Commits)
	return res, nil
}

func parseCommit(rec []byte) (Commit, bool) {
	fields := strings.SplitN(string(rec), "\n", logFields)
	if len(fields) < logFields {
		return Commit{}, false
	}
	c := Commit{
		SHA:            fields[0],
		ShortSHA:       fields[1],
		AuthorName:     fields[3],
		AuthorEmail:    fields[4],
		CommitterName:  fields[6],
		CommitterEmail: fields[7],
		Subject:        fields[9],
		Body:           strings.TrimRight(fields[10], "\n"),
	}
	if t, err := strconv.ParseInt(fields[2], 10, 64); err == nil {
		c.AuthorTime = t * int64(time.Second/time.Nanosecond)
	}
	if t, err := strconv.ParseInt(fields[5], 10, 64); err == nil {
		c.CommitterTime = t * int64(time.Second/time.Nanosecond)
	}
	if p := strings.TrimSpace(fields[8]); p != "" {
		c.Parents = strings.Split(p, " ")
	}
	return c, true
}

func logTruncationHint(limit int) string {
	if limit >= LogMaxLimit {
		return fmt.Sprintf(
			"hit hard cap of %d commits. narrow with --range/--author/--since/--pathspec — --limit cannot go higher.",
			LogMaxLimit,
		)
	}
	return fmt.Sprintf(
		"hit limit of %d commits. narrow with --range/--author/--since/--pathspec, or raise --limit (max %d).",
		limit, LogMaxLimit,
	)
}

func prettyLog(l *LogResult) string {
	if l == nil {
		return "ok\n<empty log>"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "=== ash git log: %d commit", l.Count)
	if l.Count != 1 {
		b.WriteByte('s')
	}
	if l.Truncated {
		b.WriteString(" TRUNCATED")
	}
	b.WriteString(" ===\n")
	for _, c := range l.Commits {
		// One line per commit: `<short> <date> <author>  <subject>`. Body is
		// dropped from pretty form to keep tokens cheap; the structured
		// caller still has it on the wire.
		date := time.Unix(0, c.AuthorTime).UTC().Format("2006-01-02")
		fmt.Fprintf(&b, "%s  %s  %s  %s\n", c.ShortSHA, date, c.AuthorName, c.Subject)
	}
	if l.Truncated && l.TruncationHint != "" {
		b.WriteString("\n[truncation: ")
		b.WriteString(l.TruncationHint)
		b.WriteString("]")
	}
	return strings.TrimRight(b.String(), "\n")
}

func decodeLog(m map[string]any) *LogResult {
	l := &LogResult{}
	if raw, ok := m["commits"].([]any); ok {
		for _, x := range raw {
			cm, ok := x.(map[string]any)
			if !ok {
				continue
			}
			c := Commit{}
			if v, ok := cm["sha"].(string); ok {
				c.SHA = v
			}
			if v, ok := cm["short_sha"].(string); ok {
				c.ShortSHA = v
			}
			if v, ok := cm["author_name"].(string); ok {
				c.AuthorName = v
			}
			if v, ok := cm["author_email"].(string); ok {
				c.AuthorEmail = v
			}
			if v, ok := argutil.ToInt64(cm["author_time"]); ok {
				c.AuthorTime = v
			}
			if v, ok := cm["committer_name"].(string); ok {
				c.CommitterName = v
			}
			if v, ok := cm["committer_email"].(string); ok {
				c.CommitterEmail = v
			}
			if v, ok := argutil.ToInt64(cm["committer_time"]); ok {
				c.CommitterTime = v
			}
			if v, ok := cm["subject"].(string); ok {
				c.Subject = v
			}
			if v, ok := cm["body"].(string); ok {
				c.Body = v
			}
			if raw, ok := cm["parents"].([]any); ok {
				for _, x := range raw {
					if s, ok := x.(string); ok {
						c.Parents = append(c.Parents, s)
					}
				}
			}
			l.Commits = append(l.Commits, c)
		}
	}
	if v, ok := argutil.ToInt(m["count"]); ok {
		l.Count = v
	}
	if v, ok := m["truncated"].(bool); ok {
		l.Truncated = v
	}
	if v, ok := m["truncation_hint"].(string); ok {
		l.TruncationHint = v
	}
	return l
}
