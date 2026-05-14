package git

import (
	"bufio"
	"bytes"
	"fmt"
	"strconv"
	"strings"

	"github.com/stazelabs/ash/internal/proto"
	"github.com/stazelabs/ash/internal/runner"
)

// StatusResult mirrors `git status --porcelain=v2 --branch`, with the
// changes split into agent-friendly buckets instead of a single mixed
// list. A file with "MM" (modified in index AND worktree) appears in
// both Staged and Unstaged.
type StatusResult struct {
	Head      string       `msgpack:"head,omitempty"`
	Branch    string       `msgpack:"branch,omitempty"`
	Upstream  string       `msgpack:"upstream,omitempty"`
	Ahead     int          `msgpack:"ahead,omitempty"`
	Behind    int          `msgpack:"behind,omitempty"`
	Detached  bool         `msgpack:"detached,omitempty"`
	Initial   bool         `msgpack:"initial,omitempty"`
	Staged    []FileChange `msgpack:"staged,omitempty"`
	Unstaged  []FileChange `msgpack:"unstaged,omitempty"`
	Untracked []string     `msgpack:"untracked,omitempty"`
	Ignored   []string     `msgpack:"ignored,omitempty"`
	Conflicts []string     `msgpack:"conflicts,omitempty"`
	Clean     bool         `msgpack:"clean"`
}

// FileChange names a single index-or-worktree change. Status is the
// single-char porcelain v2 code (M/A/D/R/C/U/T).
type FileChange struct {
	Path    string `msgpack:"path"`
	Status  string `msgpack:"status"`
	OldPath string `msgpack:"old_path,omitempty"`
}

// runStatus shells out to `git status --porcelain=v2 --branch [--ignored]`
// and parses the output. The exec phase is timed as IO since the cost is
// dominated by git's own filesystem traversal of the index.
// runStatusShellout shells out to system git for the status op.
// Selected by [git].backend = "shellout" in ash.toml.
func runStatusShellout(a *Args, tr *proto.Tracer) (*StatusResult, *proto.Error) {
	gitArgs := []string{"-C", a.Path, "status", "--porcelain=v2", "--branch"}
	if !a.Untracked {
		gitArgs = append(gitArgs, "--untracked-files=no")
	}
	if a.Ignored {
		gitArgs = append(gitArgs, "--ignored")
	}
	res, perr := runner.Run("git", gitArgs, runner.Opts{Tracer: tr})
	if perr != nil {
		return nil, perr
	}
	if res.ExitCode != 0 {
		return nil, gitRunError(a.Path, res.Stderr)
	}
	return parseStatus(res.Stdout)
}

// parseStatus is a pure function over porcelain-v2 bytes so we can unit
// test the parser without git on the runner.
func parseStatus(out []byte) (*StatusResult, *proto.Error) {
	s := &StatusResult{}
	var oid string
	scanner := bufio.NewScanner(bytes.NewReader(out))
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		switch {
		case strings.HasPrefix(line, "# branch.oid "):
			oid = strings.TrimPrefix(line, "# branch.oid ")
			if oid == "(initial)" {
				s.Initial = true
				oid = ""
			}
		case strings.HasPrefix(line, "# branch.head "):
			head := strings.TrimPrefix(line, "# branch.head ")
			if head == "(detached)" {
				s.Detached = true
			} else {
				s.Branch = head
			}
		case strings.HasPrefix(line, "# branch.upstream "):
			s.Upstream = strings.TrimPrefix(line, "# branch.upstream ")
		case strings.HasPrefix(line, "# branch.ab "):
			ahead, behind, ok := parseAheadBehind(strings.TrimPrefix(line, "# branch.ab "))
			if ok {
				s.Ahead = ahead
				s.Behind = behind
			}
		case strings.HasPrefix(line, "1 "):
			parseTrackedV2(line[2:], s)
		case strings.HasPrefix(line, "2 "):
			parseRenameV2(line[2:], s)
		case strings.HasPrefix(line, "u "):
			// Unmerged entry: "u <XY> <sub> <m1> <m2> <m3> <mW> <h1> <h2> <h3> <path>"
			// 10 space-separated fields after the "u " prefix; path is last.
			fields := strings.SplitN(line[2:], " ", 10)
			if len(fields) == 10 {
				s.Conflicts = append(s.Conflicts, fields[9])
			}
		case strings.HasPrefix(line, "? "):
			s.Untracked = append(s.Untracked, line[2:])
		case strings.HasPrefix(line, "! "):
			s.Ignored = append(s.Ignored, line[2:])
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, &proto.Error{Code: "parse", Msg: err.Error()}
	}
	s.Clean = len(s.Staged) == 0 && len(s.Unstaged) == 0 && len(s.Untracked) == 0 && len(s.Conflicts) == 0
	switch {
	case s.Initial:
		// Head stays empty — no commit exists yet.
	case s.Branch != "":
		s.Head = s.Branch
	case s.Detached && len(oid) >= 7:
		s.Head = oid[:7]
	case s.Detached && oid != "":
		s.Head = oid
	}
	return s, nil
}

// parseTrackedV2 handles "1 <XY> <sub> <mH> <mI> <mW> <hH> <hI> <path>"
// (with the leading "1 " already trimmed). XY: index status, worktree
// status. '.' means no change in that slot.
func parseTrackedV2(s string, out *StatusResult) {
	fields := strings.SplitN(s, " ", 8)
	if len(fields) < 8 {
		return
	}
	xy := fields[0]
	if len(xy) != 2 {
		return
	}
	path := fields[7]
	if xy[0] != '.' {
		out.Staged = append(out.Staged, FileChange{Path: path, Status: string(xy[0])})
	}
	if xy[1] != '.' {
		out.Unstaged = append(out.Unstaged, FileChange{Path: path, Status: string(xy[1])})
	}
}

// parseRenameV2 handles "2 <XY> <sub> <mH> <mI> <mW> <hH> <hI> <X><score>
// <path>\t<origPath>". For renames/copies the path field contains a TAB
// separating new and old name (without -z, which we deliberately don't
// use to keep parsing line-oriented).
func parseRenameV2(s string, out *StatusResult) {
	fields := strings.SplitN(s, " ", 9)
	if len(fields) < 9 {
		return
	}
	xy := fields[0]
	if len(xy) != 2 {
		return
	}
	pathPair := fields[8]
	tab := strings.IndexByte(pathPair, '\t')
	if tab < 0 {
		return
	}
	path := pathPair[:tab]
	old := pathPair[tab+1:]
	if xy[0] != '.' {
		out.Staged = append(out.Staged, FileChange{Path: path, Status: string(xy[0]), OldPath: old})
	}
	if xy[1] != '.' {
		out.Unstaged = append(out.Unstaged, FileChange{Path: path, Status: string(xy[1]), OldPath: old})
	}
}

// parseAheadBehind extracts (ahead, behind) from "+N -M".
func parseAheadBehind(s string) (int, int, bool) {
	parts := strings.Fields(s)
	if len(parts) != 2 || !strings.HasPrefix(parts[0], "+") || !strings.HasPrefix(parts[1], "-") {
		return 0, 0, false
	}
	a, err1 := strconv.Atoi(parts[0][1:])
	b, err2 := strconv.Atoi(parts[1][1:])
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return a, b, true
}

func prettyStatus(s *StatusResult) string {
	if s == nil {
		return "ok\n<empty status>"
	}
	var b strings.Builder
	b.WriteString("§git status: ")
	switch {
	case s.Initial:
		b.WriteString("initial commit")
	case s.Detached:
		b.WriteString("HEAD detached")
	case s.Branch != "":
		b.WriteString("on ")
		b.WriteString(s.Branch)
	}
	if s.Upstream != "" {
		fmt.Fprintf(&b, " -> %s", s.Upstream)
		if s.Ahead != 0 || s.Behind != 0 {
			fmt.Fprintf(&b, " (ahead %d, behind %d)", s.Ahead, s.Behind)
		}
	}
	if s.Clean {
		b.WriteString(" — clean")
	}
	b.WriteString("\n")
	if len(s.Staged) > 0 {
		fmt.Fprintf(&b, "staged (%d):\n", len(s.Staged))
		writeChanges(&b, s.Staged)
	}
	if len(s.Unstaged) > 0 {
		fmt.Fprintf(&b, "unstaged (%d):\n", len(s.Unstaged))
		writeChanges(&b, s.Unstaged)
	}
	if len(s.Conflicts) > 0 {
		fmt.Fprintf(&b, "conflicts (%d):\n", len(s.Conflicts))
		for _, p := range s.Conflicts {
			b.WriteString("  ")
			b.WriteString(p)
			b.WriteByte('\n')
		}
	}
	if len(s.Untracked) > 0 {
		fmt.Fprintf(&b, "untracked (%d):\n", len(s.Untracked))
		for _, p := range s.Untracked {
			b.WriteString("  ")
			b.WriteString(p)
			b.WriteByte('\n')
		}
	}
	if len(s.Ignored) > 0 {
		fmt.Fprintf(&b, "ignored (%d):\n", len(s.Ignored))
		for _, p := range s.Ignored {
			b.WriteString("  ")
			b.WriteString(p)
			b.WriteByte('\n')
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func writeChanges(b *strings.Builder, fs []FileChange) {
	for _, f := range fs {
		b.WriteString("  ")
		b.WriteString(f.Status)
		b.WriteByte(' ')
		b.WriteString(f.Path)
		if f.OldPath != "" {
			b.WriteString(" <- ")
			b.WriteString(f.OldPath)
		}
		b.WriteByte('\n')
	}
}