// Package hook implements the `hook` verb: a Claude Code PreToolUse decision
// engine that denies harness tool calls (Grep/Glob/Edit/Write/Read/Bash) and
// returns the equivalent ash invocation as the deny reason.
//
// Unlike every other ash verb, `hook` runs both client-side and daemon-side:
//
//   - **Client-side** (cmd/ash, special-cased in main.go): reads the Claude
//     hook payload from stdin, calls Decide, writes the Claude decision JSON
//     to stdout, then best-effort fires a normal ash request to the daemon
//     for ledger instrumentation. The decision path never blocks on the
//     daemon — auto-start is intentionally skipped to keep hook latency low.
//
//   - **Daemon-side** (registered in internal/verbs/verbs.go like any other
//     verb): re-runs the same Decide over the same Args so the ledger row
//     records the canonical decision. Client and daemon agree by
//     construction; both call decideFromArgs.
//
// Decision rules port the previous python implementation
// (.claude/hooks/prefer-ash.py) one-for-one, plus newly-added Write and
// Read denials now that ash write/edit are live.
//
// Soft-fail: any decoding error or unrecognized tool falls through to
// "allow". The hook should steer agents, never break their tool calls.
package hook

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/stazelabs/ash/internal/proto"
	"github.com/stazelabs/ash/internal/verbs/argutil"
)

// Args is the daemon-side typed view of the Claude hook payload. The
// client extracts these fields from stdin and sends them in proto.Request
// args; ParseArgs reconstructs them on the daemon.
//
// Fields are tool-specific and most are optional — different tool_name
// values populate different subsets. Decide tolerates missing fields per
// tool's expected shape.
type Args struct {
	ToolName  string `msgpack:"tool"`
	Pattern   string `msgpack:"pattern,omitempty"`    // Grep/Glob
	Path      string `msgpack:"path,omitempty"`       // Grep/Glob (and harness Read in some shapes)
	Glob      string `msgpack:"glob,omitempty"`       // Grep
	Command   string `msgpack:"command,omitempty"`    // Bash
	FilePath  string `msgpack:"file,omitempty"`       // Read/Edit/Write (harness key)
	OldString string `msgpack:"old,omitempty"`        // Edit
	NewString string `msgpack:"new,omitempty"`        // Edit
	Content      string   `msgpack:"content,omitempty"`       // Write
	ExcludeVerbs []string `msgpack:"exclude_verbs,omitempty"` // from ash.toml [hook].exclude_verbs
}

// Result is the structured decision. The client renders this as Claude
// hook JSON for stdout; the daemon uses it as the verb result so tokens
// and ledger row reflect what was decided.
type Result struct {
	Decision    string `msgpack:"decision"`               // "allow" | "deny"
	ToolName    string `msgpack:"tool_name,omitempty"`    // echoed for ledger queryability
	MatchedRule string `msgpack:"matched_rule,omitempty"` // e.g. "Grep", "Bash:grep", "Read:.png-allow"
	Suggested   string `msgpack:"suggested,omitempty"`    // the ash invocation, when denied
	Reason      string `msgpack:"reason,omitempty"`       // human-readable deny reason (matches what Claude shows)
}

const nudgeTail = `See CLAUDE.md "When to prefer ash over bash". If ash genuinely falls short, run it anyway and write a session note in docs/session-notes/.`

// Read is denied for source-text files but allowed for image/PDF/notebook
// formats that ash read can't render meaningfully.
var allowedReadExts = map[string]bool{
	".png":  true,
	".jpg":  true,
	".jpeg": true,
	".gif":  true,
	".webp": true,
	".pdf":  true,
	".ipynb": true,
}

// ExtractArgs parses a Claude PreToolUse hook payload (stdin JSON) into
// (wire-args map, typed Args). The map is what the client sends to the
// daemon as proto.Request.Args; the Args is what Decide consumes
// directly. Returns nil/nil/error only on malformed JSON — soft-fail
// behavior (allow) is the caller's responsibility.
func ExtractArgs(payload []byte) (map[string]any, *Args, error) {
	var raw struct {
		ToolName  string         `json:"tool_name"`
		ToolInput map[string]any `json:"tool_input"`
	}
	if err := json.Unmarshal(payload, &raw); err != nil {
		return nil, nil, err
	}
	a := &Args{ToolName: raw.ToolName}
	getStr := func(k string) string {
		if v, ok := raw.ToolInput[k]; ok {
			if s, ok := v.(string); ok {
				return s
			}
		}
		return ""
	}
	a.Pattern = getStr("pattern")
	a.Path = getStr("path")
	a.Glob = getStr("glob")
	if a.Glob == "" {
		a.Glob = getStr("include") // Grep historically used "include"
	}
	a.Command = getStr("command")
	a.FilePath = getStr("file_path")
	a.OldString = getStr("old_string")
	a.NewString = getStr("new_string")
	a.Content = getStr("content")

	wire := map[string]any{"tool": a.ToolName}
	if a.Pattern != "" {
		wire["pattern"] = a.Pattern
	}
	if a.Path != "" {
		wire["path"] = a.Path
	}
	if a.Glob != "" {
		wire["glob"] = a.Glob
	}
	if a.Command != "" {
		wire["command"] = a.Command
	}
	if a.FilePath != "" {
		wire["file"] = a.FilePath
	}
	if a.OldString != "" {
		wire["old"] = a.OldString
	}
	if a.NewString != "" {
		wire["new"] = a.NewString
	}
	if a.Content != "" {
		wire["content"] = a.Content
	}
	return wire, a, nil
}

// ParseArgs is the daemon-side wire decoder. Every field is optional: a
// hook payload can deny on tool_name alone (e.g. Grep) without any
// tool-specific args present.
func ParseArgs(in map[string]any) (*Args, *proto.Error) {
	a := &Args{}
	var perr *proto.Error
	if a.ToolName, perr = argutil.OptionalString(in, "tool", ""); perr != nil {
		return nil, perr
	}
	if a.Pattern, perr = argutil.OptionalString(in, "pattern", ""); perr != nil {
		return nil, perr
	}
	if a.Path, perr = argutil.OptionalString(in, "path", ""); perr != nil {
		return nil, perr
	}
	if a.Glob, perr = argutil.OptionalString(in, "glob", ""); perr != nil {
		return nil, perr
	}
	if a.Command, perr = argutil.OptionalString(in, "command", ""); perr != nil {
		return nil, perr
	}
	if a.FilePath, perr = argutil.OptionalString(in, "file", ""); perr != nil {
		return nil, perr
	}
	if a.OldString, perr = argutil.OptionalString(in, "old", ""); perr != nil {
		return nil, perr
	}
	if a.NewString, perr = argutil.OptionalString(in, "new", ""); perr != nil {
		return nil, perr
	}
	if a.Content, perr = argutil.OptionalString(in, "content", ""); perr != nil {
		return nil, perr
	}
	// exclude_verbs is set by the client from ash.toml; arrives as
	// []interface{} from msgpack decode of map[string]any.
	if v, ok := in["exclude_verbs"]; ok {
		if items, ok := v.([]interface{}); ok {
			for _, item := range items {
				if s, ok := item.(string); ok {
					a.ExcludeVerbs = append(a.ExcludeVerbs, s)
				}
			}
		}
	}
	return a, nil
}

// Run is the daemon-side runner. It wraps Decide with the standard verb
// signature so the registry in internal/verbs/verbs.go can dispatch it
// like any other verb. The tracer is unused — there are no
// instrumentable sub-phases.
func Run(a *Args, _ *proto.Tracer) (*Result, *proto.Error) {
	return Decide(a), nil
}

// Decide is the pure decision engine, shared by client and daemon. It
// never returns an error: unrecognized tool names and missing fields
// fall through to "allow". The hook steers, never breaks.
func Decide(a *Args) *Result {
	switch a.ToolName {
	case "Grep":
		if r := allowedByExclusion(a.ToolName, "Grep", a.ExcludeVerbs); r != nil {
			return r
		}
		return denyResult(a.ToolName, "Grep",
			"Use ash instead: `"+suggestGrep(a.Pattern, a.Path, a.Glob)+"`.")
	case "Glob":
		if r := allowedByExclusion(a.ToolName, "Glob", a.ExcludeVerbs); r != nil {
			return r
		}
		return denyResult(a.ToolName, "Glob",
			"Use ash instead: `"+suggestFind(a.Path, a.Pattern, "file")+"`.")
	case "Edit":
		if r := allowedByExclusion(a.ToolName, "Edit", a.ExcludeVerbs); r != nil {
			return r
		}
		path := pickPath(a.FilePath, a.Path)
		return denyResult(a.ToolName, "Edit",
			"Use ash instead: `"+suggestEdit(path, a.OldString, a.NewString)+"`.")
	case "Write":
		if r := allowedByExclusion(a.ToolName, "Write", a.ExcludeVerbs); r != nil {
			return r
		}
		path := pickPath(a.FilePath, a.Path)
		return denyResult(a.ToolName, "Write",
			"Use ash instead: `"+suggestWrite(path)+"`.")
	case "Read":
		path := pickPath(a.FilePath, a.Path)
		ext := strings.ToLower(filepath.Ext(path))
		if allowedReadExts[ext] {
			return &Result{Decision: "allow", ToolName: a.ToolName, MatchedRule: "Read:" + ext + "-allow"}
		}
		if r := allowedByExclusion(a.ToolName, "Read", a.ExcludeVerbs); r != nil {
			return r
		}
		return denyResult(a.ToolName, "Read",
			"Use ash instead: `"+suggestRead(path)+"`.")
	case "Bash":
		return decideBash(a.Command, a.ExcludeVerbs)
	}
	return &Result{Decision: "allow", ToolName: a.ToolName}
}

func denyResult(toolName, rule, reason string) *Result {
	r := &Result{
		Decision:    "deny",
		ToolName:    toolName,
		MatchedRule: rule,
		Reason:      reason + " " + nudgeTail,
	}
	r.Suggested = extractSuggested(reason)
	return r
}

// extractSuggested pulls the backtick-wrapped ash invocation out of a
// "Use ash instead: `ash …`." reason for separate ledger queryability.
// Best-effort; missing backticks just leave Suggested empty.
func extractSuggested(reason string) string {
	first := strings.IndexByte(reason, '`')
	if first < 0 {
		return ""
	}
	last := strings.IndexByte(reason[first+1:], '`')
	if last < 0 {
		return ""
	}
	return reason[first+1 : first+1+last]
}

// PrettyResponse renders a one-line summary used both for terminal
// display (rare — clients almost always emit Claude JSON instead) and
// for daemon-side token counting on the ledger row.
func PrettyResponse(_ *proto.Request, rsp *proto.Response) string {
	if !rsp.OK {
		return proto.PrettyResponseHeader(rsp)
	}
	var r Result
	if err := proto.UnmarshalData(rsp, &r); err != nil {
		return "ok\n<unrecognized hook result>"
	}
	if r.Decision == "deny" {
		if r.Suggested != "" {
			return fmt.Sprintf("deny %s [%s] -> %s", r.ToolName, r.MatchedRule, r.Suggested)
		}
		return fmt.Sprintf("deny %s [%s]", r.ToolName, r.MatchedRule)
	}
	if r.MatchedRule != "" {
		return fmt.Sprintf("allow %s [%s]", r.ToolName, r.MatchedRule)
	}
	return fmt.Sprintf("allow %s", r.ToolName)
}

// EncodeClaudeDecision renders the Result as the Claude PreToolUse hook
// JSON shape. Returns nil for "allow" — the harness treats no-output as
// pass-through, which is what we want. HTML escaping is disabled so
// placeholders like `<text>` round-trip without < noise.
func EncodeClaudeDecision(r *Result) ([]byte, error) {
	if r == nil || r.Decision != "deny" {
		return nil, nil
	}
	out := map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":            "PreToolUse",
			"permissionDecision":       "deny",
			"permissionDecisionReason": r.Reason,
		},
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(out); err != nil {
		return nil, err
	}
	// json.Encoder.Encode appends a trailing newline; strip it for parity
	// with json.Marshal.
	b := buf.Bytes()
	return bytes.TrimRight(b, "\n"), nil
}

// -- suggestion builders ---------------------------------------------------

func suggestGrep(pattern, path, glob string) string {
	parts := []string{"ash grep"}
	if pattern != "" {
		parts = append(parts, "--pattern "+shellquote(pattern))
	}
	if path != "" {
		parts = append(parts, "--path "+shellquote(path))
	} else {
		parts = append(parts, "--path .")
	}
	if glob != "" {
		parts = append(parts, "--glob "+shellquote(glob))
	}
	return strings.Join(parts, " ")
}

func suggestFind(path, glob, type_ string) string {
	parts := []string{"ash find"}
	if path != "" {
		parts = append(parts, "--path "+shellquote(path))
	} else {
		parts = append(parts, "--path .")
	}
	if glob != "" {
		parts = append(parts, "--glob "+shellquote(glob))
	}
	if type_ != "" {
		parts = append(parts, "--type "+type_)
	}
	return strings.Join(parts, " ")
}

func suggestRead(path string) string {
	if path == "" {
		return "ash read --path <file>"
	}
	return "ash read --path " + shellquote(path)
}

func suggestEdit(path, old, new string) string {
	if path == "" {
		path = "<file>"
	}
	if old == "" {
		return "ash edit --path " + shellquote(path) + " --old <text> --new <replacement>"
	}
	return "ash edit --path " + shellquote(path) +
		" --old " + shellquote(old) +
		" --new " + shellquote(new)
}

func suggestWrite(path string) string {
	if path == "" {
		path = "<file>"
	}
	return "ash write --path " + shellquote(path) + " --content <text>"
}

// suggestWriteRedirect renders the canonical heredoc write form for the
// `cat/echo/printf/tee > FILE` idioms (ASH-69). Heredoc form is the
// CLAUDE.md prescription — it sidesteps shell-quoting hazards on
// non-trivial content.
func suggestWriteRedirect(path string) string {
	if path == "" {
		path = "<file>"
	}
	return "ash write --path " + shellquote(path) + " --content - << 'EOF'"
}

func suggestTest(packages []string) string {
	if len(packages) == 0 {
		return "ash test"
	}
	return "ash test --packages " + shellquote(strings.Join(packages, " "))
}

func suggestStat(paths []string) string {
	if len(paths) == 0 {
		return "ash stat --paths <path>"
	}
	return "ash stat --paths " + shellquote(strings.Join(paths, ","))
}

// pickPath returns the first non-empty path candidate. Harness uses
// "file_path" for Read/Edit/Write but some payloads use "path"; checking
// both keeps us robust to either shape.
func pickPath(candidates ...string) string {
	for _, c := range candidates {
		if c != "" {
			return c
		}
	}
	return ""
}

// -- exclusion helpers -------------------------------------------------------

// verbRuleMap maps ash verb names (as used in ash.toml [hook].exclude_verbs)
// to the MatchedRule strings they suppress.
var verbRuleMap = map[string][]string{
	"grep":  {"Grep", "Bash:grep", "Bash:rg", "Bash:egrep", "Bash:fgrep"},
	"find":  {"Glob", "Bash:find", "Bash:ls-R"},
	"read":  {"Read", "Bash:cat", "Bash:head", "Bash:tail"},
	"edit":  {"Edit"},
	"write": {"Write", "Bash:redirect-write"},
	"stat":  {"Bash:stat"},
	"git":   {"Bash:git-status", "Bash:git-log", "Bash:git-diff", "Bash:git-show"},
	"test":  {"Bash:go-test"},
}

func isRuleExcluded(rule string, excludeVerbs []string) bool {
	for _, verb := range excludeVerbs {
		for _, r := range verbRuleMap[verb] {
			if r == rule {
				return true
			}
		}
	}
	return false
}

// allowedByExclusion returns a non-nil allow Result when rule is
// excluded by the caller's ExcludeVerbs list; otherwise nil so the
// caller falls through to denyResult.
func allowedByExclusion(toolName, rule string, excludeVerbs []string) *Result {
	if !isRuleExcluded(rule, excludeVerbs) {
		return nil
	}
	return &Result{Decision: "allow", ToolName: toolName, MatchedRule: rule + ":excluded"}
}

// -- bash command analysis -------------------------------------------------

// segments splits a bash command string on shell operators (||, &&, ;, |)
// while treating quoted regions and subshell expressions as opaque so that
// operator characters inside string literals do not produce false-positive splits.
//
// Tracking rules:
//   - Single quotes: everything between ' and ' is literal; no splitting.
//   - Double quotes: treated as a string context; $(...) nesting is tracked.
//   - Backslash outside single-quotes: next byte is escaped and passed through.
//   - Parentheses (bare and via $(...)): depth-tracked; no splitting inside.
//   - Backtick substitutions: depth-tracked; no splitting inside.
//   - Heredocs (<<EOF, <<-EOF, <<'EOF', <<"EOF") at top level:
//     scanHeredoc locates the body and the operator + delimiter stay in
//     the segment, but the body bytes never enter cur. Without this,
//     content like markdown tables (| find | path |) or prose mentions
//     of redirected programs in `ash write --content - <<EOF...EOF`
//     would produce false segments and false denies. Heredocs inside
//     $(...) or quoted contexts are still opaque via paren/quote
//     tracking and don't need separate handling.
func segments(command string) []string {
	var segs []string
	var cur strings.Builder
	inSingle := false
	inDouble := false
	parenDepth := 0
	backtickDepth := 0

	flush := func() {
		if s := strings.TrimSpace(cur.String()); s != "" {
			segs = append(segs, s)
		}
		cur.Reset()
	}

	i := 0
	n := len(command)
	for i < n {
		c := command[i]

		if inSingle {
			if c == '\'' {
				inSingle = false
			}
			cur.WriteByte(c)
			i++
			continue
		}

		if c == '\\' {
			cur.WriteByte(c)
			i++
			if i < n {
				cur.WriteByte(command[i])
				i++
			}
			continue
		}

		if inDouble {
			switch {
			case c == '"':
				inDouble = false
				cur.WriteByte(c)
			case c == '$' && i+1 < n && command[i+1] == '(':
				parenDepth++
				cur.WriteByte(c)
				cur.WriteByte(command[i+1])
				i += 2
				continue
			case c == '(':
				parenDepth++
				cur.WriteByte(c)
			case c == ')':
				if parenDepth > 0 {
					parenDepth--
				}
				cur.WriteByte(c)
			case c == '`':
				backtickDepth++
				cur.WriteByte(c)
			default:
				cur.WriteByte(c)
			}
			i++
			continue
		}

		// Unquoted context.
		switch {
		case c == '\'':
			inSingle = true
			cur.WriteByte(c)
			i++
		case c == '"':
			inDouble = true
			cur.WriteByte(c)
			i++
		case c == '`':
			if backtickDepth > 0 {
				backtickDepth--
			} else {
				backtickDepth++
			}
			cur.WriteByte(c)
			i++
		case c == '$' && i+1 < n && command[i+1] == '(':
			parenDepth++
			cur.WriteByte(c)
			cur.WriteByte(command[i+1])
			i += 2
		case c == '(':
			parenDepth++
			cur.WriteByte(c)
			i++
		case c == ')':
			if parenDepth > 0 {
				parenDepth--
			}
			cur.WriteByte(c)
			i++
		case c == '<' && i+1 < n && command[i+1] == '<' && parenDepth == 0 && backtickDepth == 0:
			if delimEnd, bodyEnd, ok := scanHeredoc(command, i); ok {
				// Preserve <<DELIM in the segment text; drop the body so
				// content like markdown tables or prose mentions cannot
				// produce false splits or false program matches. Flush
				// after consumption so anything on subsequent lines (the
				// terminator is followed by its own newline) starts a
				// fresh segment, matching bash semantics where a newline
				// after a heredoc terminator separates commands.
				cur.WriteString(command[i:delimEnd])
				i = bodyEnd
				flush()
			} else {
				cur.WriteByte(c)
				i++
			}
		case (c == ';' || c == '|' || c == '&') && parenDepth == 0 && backtickDepth == 0:
			if (c == '|' || c == '&') && i+1 < n && command[i+1] == c {
				// Two-character operator: || or &&
				flush()
				i += 2
			} else if c == '&' {
				// Lone & (background): treat as literal rather than splitting.
				cur.WriteByte(c)
				i++
			} else {
				// ; or lone |: split.
				flush()
				i++
			}
		default:
			cur.WriteByte(c)
			i++
		}
	}
	flush()
	return segs
}

// scanHeredoc parses a heredoc operator at s[start] (which must be "<<")
// and returns:
//   - delimEnd: position just after the delimiter token (and any closing
//     quote). Bytes s[start:delimEnd] are the literal "<<DELIM" portion
//     that should be preserved in the segment text.
//   - bodyEnd: position just after the heredoc body terminator line
//     (including its trailing newline). Bytes between delimEnd and
//     bodyEnd are the body and must NOT be in the segment text — that
//     is the whole point of this function.
//   - ok: false if "<<" is not at start, or if the delimiter word is
//     empty. Caller should fall back to literal handling in that case.
//
// Recognises <<W, <<-W (strip-tabs), <<'W', <<"W", <<\W (escaped), and the
// space-before-delimiter forms (<< 'W', << "W", << W — valid bash per POSIX).
// Unterminated heredocs (no matching delimiter line before EOF) report
// ok=true with bodyEnd=len(s) so the caller cleanly stops scanning.
//
// Simplification: if other tokens follow <<DELIM on the same line
// (e.g. `cmd <<EOF arg2`), they are skipped along with the body. This
// is exotic in practice; for the false-positives we care about
// (markdown tables, prose, code blocks in `ash write` heredocs) the
// simpler version is sufficient.
func scanHeredoc(s string, start int) (delimEnd, bodyEnd int, ok bool) {
	n := len(s)
	if start+1 >= n || s[start] != '<' || s[start+1] != '<' {
		return 0, 0, false
	}
	j := start + 2
	stripTabs := false
	if j < n && s[j] == '-' {
		stripTabs = true
		j++
	}
	for j < n && (s[j] == ' ' || s[j] == '\t') {
		j++
	}
	quote := byte(0)
	if j < n && (s[j] == '\'' || s[j] == '"') {
		quote = s[j]
		j++
	} else if j < n && s[j] == '\\' {
		j++
	}
	delimStart := j
	for j < n {
		c := s[j]
		if quote != 0 {
			if c == quote {
				break
			}
			j++
			continue
		}
		if c == ' ' || c == '\t' || c == '\n' || c == ';' ||
			c == '|' || c == '&' || c == '<' || c == '>' ||
			c == '(' || c == ')' {
			break
		}
		j++
	}
	if j == delimStart {
		return 0, 0, false
	}
	delim := s[delimStart:j]
	if quote != 0 && j < n && s[j] == quote {
		j++
	}
	delimEnd = j
	nl := strings.IndexByte(s[j:], '\n')
	if nl < 0 {
		return delimEnd, n, true
	}
	pos := j + nl + 1
	for pos < n {
		lineEnd := strings.IndexByte(s[pos:], '\n')
		var line string
		if lineEnd < 0 {
			line = s[pos:]
		} else {
			line = s[pos : pos+lineEnd]
		}
		candidate := line
		if stripTabs {
			candidate = strings.TrimLeft(candidate, "\t")
		}
		if candidate == delim {
			if lineEnd < 0 {
				return delimEnd, n, true
			}
			return delimEnd, pos + lineEnd + 1, true
		}
		if lineEnd < 0 {
			break
		}
		pos += lineEnd + 1
	}
	return delimEnd, n, true
}

// firstToken returns the first program in a bash segment after stripping
// leading VAR=value assignments and env/command/exec/time/nice prefixes.
// Tokenization is whitespace-based (no quote awareness); this matches the
// python helper's behavior.
// unquoteToken strips surrounding single or double quotes, or a leading
// backslash, from a program token so that "grep", 'grep', and \grep all
// resolve to grep for deny-list lookup.
func unquoteToken(s string) string {
	n := len(s)
	if n >= 2 {
		if (s[0] == '"' && s[n-1] == '"') || (s[0] == '\'' && s[n-1] == '\'') {
			return s[1 : n-1]
		}
	}
	if n >= 1 && s[0] == '\\' {
		return s[1:]
	}
	return s
}

func firstToken(segment string) (prog string, args []string) {
	toks := tokenize(segment)
	for {
		if len(toks) == 0 {
			return "", nil
		}
		tok := unquoteToken(toks[0])
		// Skip leading VAR=value assignments.
		if isAssignment(tok) {
			toks = toks[1:]
			continue
		}
		// Skip env/command/exec/time/nice prefixes.
		if isPrefixWord(tok) {
			toks = toks[1:]
			// After env, more VAR=val may appear.
			for len(toks) > 0 && isAssignment(unquoteToken(toks[0])) {
				toks = toks[1:]
			}
			continue
		}
		break
	}
	if len(toks) == 0 {
		return "", nil
	}
	prog = filepath.Base(unquoteToken(toks[0])) // /usr/bin/grep -> grep, "grep" -> grep
	args = toks[1:]
	return prog, args
}

func tokenize(s string) []string {
	// Naïve whitespace split — good enough for the patterns the agent
	// actually emits. The python uses shlex.split with a fallback to
	// .split() on errors; we just always do whitespace.
	return strings.Fields(s)
}

func isAssignment(tok string) bool {
	eq := strings.IndexByte(tok, '=')
	if eq <= 0 {
		return false
	}
	for i, ch := range tok[:eq] {
		if ch == '_' {
			continue
		}
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') {
			continue
		}
		if i > 0 && ch >= '0' && ch <= '9' {
			continue
		}
		return false
	}
	return true
}

var prefixWords = map[string]bool{
	"env":     true,
	"command": true,
	"exec":    true,
	"time":    true,
	"nice":    true,
}

func isPrefixWord(tok string) bool { return prefixWords[tok] }

var grepLike = map[string]bool{"grep": true, "rg": true, "egrep": true, "fgrep": true}
var readLike = map[string]bool{"cat": true, "head": true, "tail": true}
var gitRedirect = map[string]bool{"status": true, "log": true, "diff": true, "show": true}

// writeRedirectProgs are bash programs whose canonical write idiom is an
// output redirection: `cat > FILE`, `echo "x" > FILE`, `printf "..." > FILE`,
// `tee FILE` (or `tee >> FILE`). When one of these runs with a `>`/`>>`/`&>`
// redirect we route to ash write rather than ash read (ASH-69).
var writeRedirectProgs = map[string]bool{"cat": true, "echo": true, "printf": true, "tee": true}

func decideBash(command string, excludeVerbs []string) *Result {
	if strings.TrimSpace(command) == "" {
		return &Result{Decision: "allow", ToolName: "Bash"}
	}
	deny := func(toolName, rule, reason string) *Result {
		if r := allowedByExclusion(toolName, rule, excludeVerbs); r != nil {
			return r
		}
		return denyResult(toolName, rule, reason)
	}
	for _, seg := range segments(command) {
		prog, args := firstToken(seg)
		if prog == "" {
			continue
		}
		if writeRedirectProgs[prog] {
			if target, ok := detectOutputRedirect(args); ok {
				reason := fmt.Sprintf("Use ash instead: `%s` (bash `%s ... > FILE` is redirected to ash write in this repo).",
					suggestWriteRedirect(target), prog)
				return deny("Bash", "Bash:redirect-write", reason)
			}
		}
		if grepLike[prog] {
			pos := positionalArgs(args)
			var pattern, path string
			if len(pos) >= 1 {
				pattern = pos[0]
			}
			if len(pos) >= 2 {
				path = pos[1]
			} else {
				path = "."
			}
			reason := fmt.Sprintf("Use ash instead: `%s` (bash `%s` is redirected to ash grep in this repo).",
				suggestGrep(pattern, path, ""), prog)
			return deny("Bash", "Bash:"+prog, reason)
		}
		if prog == "find" {
			pos := positionalArgs(args)
			path := "."
			if len(pos) >= 1 {
				path = pos[0]
			}
			glob := ""
			for i, a := range args {
				if (a == "-name" || a == "-iname") && i+1 < len(args) {
					glob = args[i+1]
					break
				}
			}
			reason := fmt.Sprintf("Use ash instead: `%s` (bash `find` is redirected to ash find in this repo).",
				suggestFind(path, glob, ""))
			return deny("Bash", "Bash:find", reason)
		}
		if readLike[prog] {
			pos := positionalArgs(args)
			path := ""
			if len(pos) >= 1 {
				path = pos[0]
			}
			reason := fmt.Sprintf("Use ash instead: `%s` (bash `%s` is redirected to ash read in this repo).",
				suggestRead(path), prog)
			return deny("Bash", "Bash:"+prog, reason)
		}
		if prog == "ls" && lsIsRecursive(args) {
			pos := positionalArgs(args)
			path := "."
			if len(pos) >= 1 {
				path = pos[0]
			}
			reason := fmt.Sprintf("Use ash instead: `%s` (recursive `ls -R` is redirected to ash find in this repo).",
				suggestFind(path, "", ""))
			return deny("Bash", "Bash:ls-R", reason)
		}
		if prog == "stat" {
			pos := positionalArgs(args)
			reason := fmt.Sprintf("Use ash instead: `%s` (bash `stat` is redirected to ash stat in this repo).",
				suggestStat(pos))
			return deny("Bash", "Bash:stat", reason)
		}
		if prog == "git" {
			sub := gitSubcommand(args)
			if gitRedirect[sub] {
				reason := fmt.Sprintf("Use ash instead: `ash git --op %s` (bash `git %s` is redirected to the ash git verb in this repo).",
					sub, sub)
				return deny("Bash", "Bash:git-"+sub, reason)
			}
		}
		if prog == "go" && len(args) > 0 && args[0] == "test" {
			pos := positionalArgs(args[1:])
			reason := fmt.Sprintf("Use ash instead: `%s` (bash `go test` is redirected to ash test in this repo).",
				suggestTest(pos))
			return deny("Bash", "Bash:go-test", reason)
		}
		// Other programs (gh, mv, rm, …) pass through.
	}
	return &Result{Decision: "allow", ToolName: "Bash"}
}

func positionalArgs(args []string) []string {
	args = stripRedirections(args)
	out := make([]string, 0, len(args))
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			out = append(out, a)
		}
	}
	return out
}

// classifyRedirectToken inspects a single token (after whitespace
// tokenization) and reports whether it is a shell redirection operator.
// consumesFilename is true for bare operators (`>`, `>>`, `<`, `<<`,
// `<<-`, `&>`, `&>>`, `2>`, etc.) where the next token is the redirect
// target; false for glued forms (`>foo`, `2>&1`, `<<EOF`) and
// fd-duplications that contain the target inline. Used by
// stripRedirections to keep redirection tokens out of suggestion paths
// (ASH-69).
func classifyRedirectToken(tok string) (isRedirect, consumesFilename bool) {
	n := len(tok)
	if n == 0 {
		return false, false
	}
	i := 0
	for i < n && tok[i] >= '0' && tok[i] <= '9' {
		i++
	}
	if i < n && tok[i] == '&' {
		if i+1 < n && tok[i+1] == '>' {
			i++
		} else {
			return false, false
		}
	}
	if i >= n || (tok[i] != '<' && tok[i] != '>') {
		return false, false
	}
	op := tok[i]
	i++
	if i < n && tok[i] == op {
		i++
	}
	if op == '<' && i < n && tok[i] == '-' {
		i++
	}
	return true, i == n
}

// stripRedirections returns args with shell redirection tokens removed.
// Bare operators consume the following token (the target filename); glued
// forms (`>foo`, `2>&1`, `<<EOF`) drop on their own.
func stripRedirections(args []string) []string {
	out := make([]string, 0, len(args))
	skipNext := false
	for _, a := range args {
		if skipNext {
			skipNext = false
			continue
		}
		isRed, consumes := classifyRedirectToken(a)
		if isRed {
			if consumes {
				skipNext = true
			}
			continue
		}
		out = append(out, a)
	}
	return out
}

// detectOutputRedirect scans args (still raw, before stripRedirections)
// for an output redirection (`>`, `>>`, `&>`, `&>>`) and returns the
// target path when present. Stderr-only (`2>...`), input (`<...`), and
// fd-duplication (`>&N`) redirections do not count as a write target.
func detectOutputRedirect(args []string) (string, bool) {
	for i, a := range args {
		if a == "" {
			continue
		}
		j := 0
		if a[j] == '&' {
			j++
		} else if a[j] >= '0' && a[j] <= '9' {
			continue
		}
		if j >= len(a) || a[j] != '>' {
			continue
		}
		for j < len(a) && a[j] == '>' {
			j++
		}
		target := a[j:]
		if target != "" {
			return target, true
		}
		if i+1 < len(args) {
			next := args[i+1]
			if isRed, _ := classifyRedirectToken(next); !isRed {
				return next, true
			}
		}
		return "", false
	}
	return "", false
}

func lsIsRecursive(args []string) bool {
	for _, a := range args {
		if a == "--recursive" {
			return true
		}
		if strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") {
			if strings.ContainsRune(a[1:], 'R') {
				return true
			}
		}
	}
	return false
}

func gitSubcommand(args []string) string {
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			continue
		}
		return a
	}
	return ""
}

// -- shell quoting (port of python shlex.quote) -----------------------------

var safeShellRe = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

func shellquote(s string) string {
	if s == "" {
		return "''"
	}
	if safeShellRe.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

// -- result decoding for pretty-rendering ----------------------------------
