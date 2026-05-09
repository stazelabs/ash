// Package grep implements the `grep` verb.
//
// Args:
//
//	pattern             string  (required) - RE2 regex (or literal string when fixed_string=true)
//	path                string  (required) - file or directory to search
//	glob                string  (optional) - doublestar pattern; only files whose
//	                                         path-relative-to-root matches are scanned. Default "**".
//	case                string  (optional) - "smart" (default) | "sensitive" | "insensitive"
//	                                         smart = insensitive unless pattern has an uppercase letter
//	fixed_string        bool    (optional) - treat pattern as literal text (default false)
//	word                bool    (optional) - require word boundaries (\b...\b) around the pattern
//	max_matches         int     (optional) - cap on total match records, default 256, hard cap 4096
//	max_per_file        int     (optional) - cap on records per file, 0 = unlimited (default)
//	context_before      int     (optional) - lines before each match, default 0, max 50
//	context_after       int     (optional) - lines after each match, default 0, max 50
//	files_only          bool    (optional) - return matching file paths only (no match records)
//	include_hidden      bool    (optional) - if false (default), descend skips dirs starting with "."
//	respect_gitignore   bool    (optional) - if true (default), .gitignore at the walk root is applied
//	exclude             string  (optional) - doublestar pattern; matching paths are skipped
//	max_depth           int     (optional) - 0 = unlimited
//
// Path semantics mirror find: relative-in -> relative-out, absolute-in -> absolute-out.
//
// Binary files (NUL byte in the first 8 KiB) are skipped silently. Files larger
// than 16 MiB are skipped silently. Both are reported in counters on the
// response so the agent can see why a hit it expected didn't appear.
//
// Symlinks are never followed during the walk; the same rule as find.
package grep

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/stazelabs/ash/internal/jail"
	"github.com/stazelabs/ash/internal/proto"
	"github.com/stazelabs/ash/internal/verbs/argutil"
	"github.com/stazelabs/ash/internal/walker"
)

const (
	DefaultMaxMatches = 256
	MaxMaxMatches     = 4096
	MaxContextLines   = 50
	DefaultGlob       = "**"

	binaryProbeBytes = 8 << 10  // 8 KiB
	maxLineTextBytes = 4 << 10  // truncate per-line text in records at 4 KiB
	maxFileSize      = 16 << 20 // skip files larger than 16 MiB
)

type Args struct {
	Pattern          string
	Path             string
	Glob             string
	Case             string
	FixedString      bool
	Word             bool
	MaxMatches       int
	MaxPerFile       int
	ContextBefore    int
	ContextAfter     int
	FilesOnly        bool
	NoText           bool
	IncludeHidden    bool
	RespectGitignore bool
	Exclude          string
	MaxDepth         int
}

type Match struct {
	Path string `msgpack:"path"`
	Line int    `msgpack:"line"`           // 1-indexed line number
	Col  int    `msgpack:"col,omitempty"`  // 1-indexed byte column of the first match start; only on match records
	Text string `msgpack:"text"`           // line content, possibly truncated
	Kind string `msgpack:"kind,omitempty"` // "" (= match) | "before" | "after"
}

type Result struct {
	Matches            []Match  `msgpack:"matches,omitempty"`
	Files              []string `msgpack:"files,omitempty"` // populated only when FilesOnly
	Count              int      `msgpack:"count"`           // number of records (matches+context, or files)
	MatchCount         int      `msgpack:"match_count"`     // number of "match" records (excludes context)
	FileCount          int      `msgpack:"file_count"`      // distinct files with at least one match
	FilesScanned       int      `msgpack:"files_scanned"`   // files actually opened and content-searched
	FilesSkippedBinary int      `msgpack:"files_skipped_binary,omitempty"`
	FilesSkippedLarge  int      `msgpack:"files_skipped_large,omitempty"`
	Truncated          bool     `msgpack:"truncated,omitempty"`
	TruncationHint     string   `msgpack:"truncation_hint,omitempty"`
}

func ParseArgs(in map[string]any) (*Args, *proto.Error) {
	a := &Args{}
	var perr *proto.Error
	if a.Pattern, perr = argutil.RequireString(in, "pattern"); perr != nil {
		return nil, perr
	}
	if a.Path, perr = argutil.RequireString(in, "path"); perr != nil {
		return nil, perr
	}
	if a.Glob, perr = argutil.OptionalNonEmptyString(in, "glob", DefaultGlob); perr != nil {
		return nil, perr
	}
	if a.Case, perr = argutil.OptionalEnum(in, "case", "smart", []string{"smart", "sensitive", "insensitive"}); perr != nil {
		return nil, perr
	}
	if a.FixedString, perr = argutil.OptionalBool(in, "fixed_string", false); perr != nil {
		return nil, perr
	}
	if a.Word, perr = argutil.OptionalBool(in, "word", false); perr != nil {
		return nil, perr
	}
	if a.MaxMatches, perr = argutil.OptionalPosInt(in, "max_matches", DefaultMaxMatches, MaxMaxMatches); perr != nil {
		return nil, perr
	}
	if a.MaxPerFile, perr = argutil.OptionalNonNegInt(in, "max_per_file", 0, 0); perr != nil {
		return nil, perr
	}
	if a.ContextBefore, perr = argutil.OptionalNonNegInt(in, "context_before", 0, MaxContextLines); perr != nil {
		return nil, perr
	}
	if a.ContextAfter, perr = argutil.OptionalNonNegInt(in, "context_after", 0, MaxContextLines); perr != nil {
		return nil, perr
	}
	if a.FilesOnly, perr = argutil.OptionalBool(in, "files_only", false); perr != nil {
		return nil, perr
	}
	if a.NoText, perr = argutil.OptionalBool(in, "no_text", false); perr != nil {
		return nil, perr
	}
	if a.IncludeHidden, perr = argutil.OptionalBool(in, "include_hidden", false); perr != nil {
		return nil, perr
	}
	if a.RespectGitignore, perr = argutil.OptionalBool(in, "respect_gitignore", true); perr != nil {
		return nil, perr
	}
	if a.Exclude, perr = argutil.OptionalString(in, "exclude", ""); perr != nil {
		return nil, perr
	}
	if a.MaxDepth, perr = argutil.OptionalNonNegInt(in, "max_depth", 0, 0); perr != nil {
		return nil, perr
	}
	if !doublestar.ValidatePathPattern(a.Glob) {
		return nil, &proto.Error{Code: "args", Msg: "glob is not a valid pattern: " + a.Glob}
	}
	if a.Exclude != "" && !doublestar.ValidatePathPattern(a.Exclude) {
		return nil, &proto.Error{Code: "args", Msg: "exclude is not a valid pattern: " + a.Exclude}
	}
	if perr := jail.CheckPaths(map[string]string{
		"path": a.Path,
	}); perr != nil {
		return nil, perr
	}
	return a, nil
}

// Run executes the search. Path may be a file (no walking) or a directory
// (filepath.WalkDir with the same filtering as find). tr may be nil; tests
// pass nil to skip phase timing.
func Run(a *Args, tr *proto.Tracer) (*Result, *proto.Error) {
	regexStart := time.Now()
	re, perr := compilePattern(a)
	tr.AddRegexCompile(time.Since(regexStart))
	if perr != nil {
		return nil, perr
	}

	info, err := os.Stat(a.Path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, &proto.Error{Code: "not_found", Msg: a.Path + ": no such path"}
		}
		if errors.Is(err, fs.ErrPermission) {
			return nil, &proto.Error{Code: "permission", Msg: err.Error()}
		}
		return nil, &proto.Error{Code: "stat", Msg: err.Error()}
	}

	res := &Result{Matches: make([]Match, 0, 32)}
	st := &state{
		a:            a,
		re:           re,
		res:          res,
		tr:           tr,
		fileMatches:  map[string]int{},
		matchedFiles: map[string]struct{}{},
	}

	if !info.IsDir() {
		// Single-file search; path filters and gitignore are skipped.
		st.searchOne(a.Path)
	} else {
		walkStart := time.Now()
		walkErr := walker.Walk(a.Path, walker.Options{
			Glob:             a.Glob,
			Exclude:          a.Exclude,
			MaxDepth:         a.MaxDepth,
			IncludeHidden:    a.IncludeHidden,
			RespectGitignore: a.RespectGitignore,
		}, func(e walker.Entry) (walker.Action, error) {
			// grep only searches regular files. Dirs are descended by the
			// walker; symlinks are never followed.
			if e.Type != "file" {
				return walker.Continue, nil
			}
			if st.searchOne(e.Path) {
				return walker.Stop, nil
			}
			return walker.Continue, nil
		})
		tr.AddWalk(time.Since(walkStart))
		if walkErr != nil {
			return nil, &proto.Error{Code: "walk", Msg: walkErr.Error()}
		}
	}

	if a.FilesOnly {
		files := make([]string, 0, len(st.matchedFiles))
		for f := range st.matchedFiles {
			files = append(files, f)
		}
		sort.Strings(files)
		res.Files = files
		res.Matches = nil
		res.Count = len(files)
	} else {
		res.Count = len(res.Matches)
	}
	res.MatchCount = st.matchCount
	res.FileCount = len(st.matchedFiles)
	if st.limitHit {
		res.Truncated = true
		res.TruncationHint = truncationHint(a.MaxMatches, a.FilesOnly)
	}
	return res, nil
}

// truncationHint adapts the message to whether the user hit their own
// --max_matches (raisable up to MaxMaxMatches) or the hard cap itself
// (not raisable; only narrowing helps). ASH-12.
func truncationHint(limit int, filesOnly bool) string {
	if limit >= MaxMaxMatches {
		if filesOnly {
			return fmt.Sprintf(
				"hit hard cap of %d distinct files. narrow with --glob or --exclude — --max_matches cannot go higher.",
				MaxMaxMatches,
			)
		}
		return fmt.Sprintf(
			"hit hard cap of %d match records. narrow with --glob, --max_per_file, or --exclude — --max_matches cannot go higher.",
			MaxMaxMatches,
		)
	}
	if filesOnly {
		return fmt.Sprintf(
			"hit max_matches=%d distinct files. narrow with --glob, --exclude, or raise --max_matches (max %d).",
			limit, MaxMaxMatches,
		)
	}
	return fmt.Sprintf(
		"hit max_matches=%d. narrow with --glob, --max_per_file, --exclude, or raise --max_matches (max %d).",
		limit, MaxMaxMatches,
	)
}

// state threads per-walk bookkeeping through the per-file routines without
// turning Run into a closure soup.
type state struct {
	a            *Args
	re           *regexp.Regexp
	res          *Result
	tr           *proto.Tracer // nil-safe; per-file IO time accumulates here
	fileMatches  map[string]int
	matchedFiles map[string]struct{}
	matchCount   int  // count of "match" records (excludes context)
	limitHit     bool // global cap reached
}

// searchOne reads, screens, and searches a single file. Returns true if the
// caller should stop walking entirely (global limit reached). Opens the file
// and fstats from the open fd rather than depending on a pre-walk Lstat —
// the walker leaves WantInfo false for grep so it doesn't pay per-entry
// stat cost on dirs and pre-rejected entries.
func (s *state) searchOne(path string) bool {
	ioStart := time.Now()
	f, err := os.Open(path)
	if err != nil {
		// Unreadable files are silently skipped, matching ripgrep's default.
		// Permission and transient errors don't abort the whole search.
		s.tr.AddIO(time.Since(ioStart))
		return false
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		s.tr.AddIO(time.Since(ioStart))
		return false
	}
	if fi.Size() > maxFileSize {
		s.res.FilesSkippedLarge++
		s.tr.AddIO(time.Since(ioStart))
		return false
	}
	body, err := io.ReadAll(f)
	s.tr.AddIO(time.Since(ioStart))
	if err != nil {
		return false
	}
	s.res.FilesScanned++
	if isBinary(body) {
		s.res.FilesSkippedBinary++
		return false
	}
	return s.searchBody(path, body)
}

// searchBody scans body line-by-line and appends records. Returns true if the
// global cap was hit and the walk should stop.
func (s *state) searchBody(path string, body []byte) bool {
	matchStart := time.Now()
	defer func() { s.tr.AddRegex(time.Since(matchStart)) }()
	a := s.a
	re := s.re

	// In files_only mode, we just need the boolean answer per file.
	if a.FilesOnly {
		if !re.Match(body) {
			return false
		}
		if _, seen := s.matchedFiles[path]; seen {
			return false
		}
		s.matchedFiles[path] = struct{}{}
		if len(s.matchedFiles) >= a.MaxMatches {
			s.limitHit = true
			return true
		}
		return false
	}

	lines := splitLines(body)

	pendingAfter := 0   // remaining after-context lines to emit
	lastEmittedLine := -1

	// appendRec appends a record and reports whether the global cap was hit.
	appendRec := func(rec Match, isMatch bool) bool {
		s.res.Matches = append(s.res.Matches, rec)
		if isMatch {
			s.matchCount++
		}
		if len(s.res.Matches) >= a.MaxMatches {
			s.limitHit = true
			return true
		}
		return false
	}

	for i, line := range lines {
		matches := re.FindAllIndex(line, -1)
		if len(matches) == 0 {
			if pendingAfter > 0 && i > lastEmittedLine {
				afterText := ""
				if !a.NoText {
					afterText = clipText(line)
				}
				if appendRec(Match{Path: path, Line: i + 1, Text: afterText, Kind: "after"}, false) {
					return true
				}
				lastEmittedLine = i
				pendingAfter--
			}
			continue
		}

		// Before-context, clamped to start-of-file and to anything we've
		// already emitted (handles overlap with a prior match's after-context).
		startCtx := i - a.ContextBefore
		if startCtx < 0 {
			startCtx = 0
		}
		if startCtx <= lastEmittedLine {
			startCtx = lastEmittedLine + 1
		}
		for j := startCtx; j < i; j++ {
			beforeText := ""
			if !a.NoText {
				beforeText = clipText(lines[j])
			}
			if appendRec(Match{Path: path, Line: j + 1, Text: beforeText, Kind: "before"}, false) {
				return true
			}
			lastEmittedLine = j
		}

		// Match line.
		col := matches[0][0] + 1
		matchText := ""
		if !a.NoText {
			matchText = clipText(line)
		}
		if appendRec(Match{Path: path, Line: i + 1, Col: col, Text: matchText}, true) {
			return true
		}
		lastEmittedLine = i
		s.matchedFiles[path] = struct{}{}
		s.fileMatches[path]++

		if a.MaxPerFile > 0 && s.fileMatches[path] >= a.MaxPerFile {
			return false // stop this file, keep walking
		}
		pendingAfter = a.ContextAfter
	}
	return false
}

// compilePattern builds the final RE2 pattern from the user's args.
func compilePattern(a *Args) (*regexp.Regexp, *proto.Error) {
	pat := a.Pattern
	if a.FixedString {
		pat = regexp.QuoteMeta(pat)
	}
	if a.Word {
		pat = `\b(?:` + pat + `)\b`
	}
	caseInsensitive := false
	switch a.Case {
	case "sensitive":
	case "insensitive":
		caseInsensitive = true
	case "smart":
		caseInsensitive = !hasUpper(a.Pattern)
	}
	if caseInsensitive {
		pat = `(?i)` + pat
	}
	re, err := regexp.Compile(pat)
	if err != nil {
		return nil, &proto.Error{Code: "args", Msg: "invalid pattern: " + err.Error()}
	}
	return re, nil
}

func hasUpper(s string) bool {
	for _, r := range s {
		if unicode.IsUpper(r) {
			return true
		}
	}
	return false
}

// isBinary mirrors ripgrep's heuristic: a NUL byte in the leading probe window
// flags the file as binary and the whole file is skipped.
func isBinary(b []byte) bool {
	probe := b
	if len(probe) > binaryProbeBytes {
		probe = probe[:binaryProbeBytes]
	}
	for _, c := range probe {
		if c == 0 {
			return true
		}
	}
	return false
}

func splitLines(b []byte) [][]byte {
	if len(b) == 0 {
		return nil
	}
	lines := make([][]byte, 0, 64)
	start := 0
	for i := 0; i < len(b); i++ {
		if b[i] == '\n' {
			line := b[start:i]
			// Strip a trailing CR so CRLF files don't bleed \r into match text.
			if len(line) > 0 && line[len(line)-1] == '\r' {
				line = line[:len(line)-1]
			}
			lines = append(lines, line)
			start = i + 1
		}
	}
	if start < len(b) {
		lines = append(lines, b[start:])
	}
	return lines
}

// plural formats "1 match" / "0 matches" / "2 matches" without an extra
// helper-per-callsite for the various nouns the verb prints.
func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

func clipText(b []byte) string {
	if len(b) > maxLineTextBytes {
		return string(b[:maxLineTextBytes]) + "…"
	}
	return string(b)
}

// PrettyResponse renders a grep response in canonical line-oriented form.
// Matches are grouped by file: one header line per file, then one record
// per emitted line. Match lines use "LINE:" and context lines use "LINE-".
// files_only mode emits one path per line.
func PrettyResponse(req *proto.Request, rsp *proto.Response) string {
	if !rsp.OK {
		return proto.PrettyResponseHeader(rsp)
	}
	var r Result
	if err := proto.UnmarshalData(rsp, &r); err != nil {
		return "ok\n<unrecognized grep result>"
	}
	var b strings.Builder

	noText := false
	if req != nil {
		// Files-only header
		if v, ok := req.Args["files_only"]; ok {
			if got, ok := argutil.ToBool(v); ok && got {
				return prettyFilesOnly(req, &r)
			}
		}
		if v, ok := req.Args["no_text"]; ok {
			noText, _ = argutil.ToBool(v)
		}
	}

	fmt.Fprintf(&b, "=== ash grep: %s in %s", plural(r.MatchCount, "match", "matches"), plural(r.FileCount, "file", "files"))
	if scope := scopeFromArgs(req); scope != "" {
		fmt.Fprintf(&b, " [%s]", scope)
	}
	if r.Truncated {
		b.WriteString(" TRUNCATED")
	}
	b.WriteString(" ===\n")
	writeSkippedSummary(&b, &r)

	// Group records by path, preserving insertion order.
	curPath := ""
	curStart := -1
	flush := func(end int) {
		if curStart < 0 {
			return
		}
		group := r.Matches[curStart:end]
		writeFileGroup(&b, curPath, group, noText)
	}
	for i, rec := range r.Matches {
		if rec.Path != curPath {
			flush(i)
			curPath = rec.Path
			curStart = i
		}
	}
	flush(len(r.Matches))

	if r.Truncated && r.TruncationHint != "" {
		b.WriteString("\n[truncation: ")
		b.WriteString(r.TruncationHint)
		b.WriteString("]")
	}
	return strings.TrimRight(b.String(), "\n")
}

func prettyFilesOnly(req *proto.Request, r *Result) string {
	var b strings.Builder
	fmt.Fprintf(&b, "=== ash grep: %s", plural(r.Count, "file", "files"))
	if scope := scopeFromArgs(req); scope != "" {
		fmt.Fprintf(&b, " [%s]", scope)
	}
	if r.Truncated {
		b.WriteString(" TRUNCATED")
	}
	b.WriteString(" ===\n")
	writeSkippedSummary(&b, r)
	for _, f := range r.Files {
		b.WriteString(f)
		b.WriteByte('\n')
	}
	if r.Truncated && r.TruncationHint != "" {
		b.WriteString("\n[truncation: ")
		b.WriteString(r.TruncationHint)
		b.WriteString("]")
	}
	return strings.TrimRight(b.String(), "\n")
}

func writeSkippedSummary(b *strings.Builder, r *Result) {
	if r.FilesSkippedBinary == 0 && r.FilesSkippedLarge == 0 {
		return
	}
	parts := make([]string, 0, 2)
	if r.FilesSkippedBinary > 0 {
		parts = append(parts, fmt.Sprintf("%d binary", r.FilesSkippedBinary))
	}
	if r.FilesSkippedLarge > 0 {
		parts = append(parts, fmt.Sprintf("%d >16MiB", r.FilesSkippedLarge))
	}
	fmt.Fprintf(b, "[skipped: %s]\n", strings.Join(parts, ", "))
}

func writeFileGroup(b *strings.Builder, path string, group []Match, noText bool) {
	matchN := 0
	for _, rec := range group {
		if rec.Kind == "" {
			matchN++
		}
	}
	fmt.Fprintf(b, "%s (%s)\n", path, plural(matchN, "match", "matches"))
	for _, rec := range group {
		if noText {
			if rec.Kind == "" {
				fmt.Fprintf(b, "  %d:%d\n", rec.Line, rec.Col)
			} else {
				fmt.Fprintf(b, "  %d-\n", rec.Line)
			}
		} else {
			sep := ":"
			if rec.Kind == "before" || rec.Kind == "after" {
				sep = "-"
			}
			fmt.Fprintf(b, "  %d%s %s\n", rec.Line, sep, rec.Text)
		}
	}
}

func scopeFromArgs(req *proto.Request) string {
	if req == nil || req.Args == nil {
		return ""
	}
	parts := make([]string, 0, 6)
	if v, ok := req.Args["pattern"].(string); ok {
		parts = append(parts, "pattern="+strconv.Quote(v))
	}
	if v, ok := req.Args["path"].(string); ok {
		parts = append(parts, "path="+v)
	}
	if v, ok := req.Args["glob"].(string); ok && v != "" && v != DefaultGlob {
		parts = append(parts, "glob="+v)
	}
	if v, ok := req.Args["case"].(string); ok && v != "" && v != "smart" {
		parts = append(parts, "case="+v)
	}
	if v, ok := req.Args["fixed_string"]; ok {
		if b, ok := argutil.ToBool(v); ok && b {
			parts = append(parts, "fixed_string=true")
		}
	}
	if v, ok := req.Args["word"]; ok {
		if b, ok := argutil.ToBool(v); ok && b {
			parts = append(parts, "word=true")
		}
	}
	// Hide defaults the way find does, surface only overrides.
	if v, ok := req.Args["respect_gitignore"]; ok {
		if b, ok := argutil.ToBool(v); ok && !b {
			parts = append(parts, "respect_gitignore=false")
		}
	}
	if v, ok := req.Args["include_hidden"]; ok {
		if b, ok := argutil.ToBool(v); ok && b {
			parts = append(parts, "include_hidden=true")
		}
	}
	if v, ok := req.Args["no_text"]; ok {
		if b, ok := argutil.ToBool(v); ok && b {
			parts = append(parts, "no_text=true")
		}
	}
	return strings.Join(parts, ", ")
}
