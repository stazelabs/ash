package bench

import (
	"fmt"
	"strconv"
	"strings"
)

// BashFor returns the argv (program at index 0) the agent would have
// run had ash not been installed, given a bench Case. The mapping is
// the inverse of `.claude/hooks/prefer-ash.py` — when adding cases here
// keep the bash form the *agent would actually have written*, not the
// most charitable bash. Adding flags ash uses internally (e.g.
// gitignore filtering) is dishonest: the whole point is the comparison
// against what bash usage looks like in practice.
//
// Returns an error if the verb / case shape isn't covered.
func BashFor(c Case) ([]string, error) {
	switch c.Verb {
	case "find":
		return bashFind(c.AshArgs)
	case "grep":
		return bashGrep(c.AshArgs)
	case "read":
		return bashRead(c.AshArgs)
	case "git":
		return bashGit(c.AshArgs)
	case "stat":
		return bashStat(c.AshArgs)
	case "write":
		return bashWrite(c.AshArgs)
	case "edit":
		return bashEdit(c.AshArgs)
	case "diff":
		return bashDiff(c.AshArgs)
	default:
		return nil, fmt.Errorf("bench: no bash translation for verb %q", c.Verb)
	}
}

// bashWrite returns the sh -c form an agent would write: a heredoc
// piped to redirected stdout. The bash side writes to a sibling path
// so ash and bash don't race on the same file across iterations.
func bashWrite(a map[string]any) ([]string, error) {
	path, ok := a["path"].(string)
	if !ok || path == "" {
		return nil, fmt.Errorf("bench: write case missing path")
	}
	content, _ := a["content"].(string)
	bashPath := path + ".bash"
	script := fmt.Sprintf("cat > %s <<'BENCH_EOF'\n%sBENCH_EOF\n", bashPath, content)
	if !strings.HasSuffix(content, "\n") {
		script = fmt.Sprintf("cat > %s <<'BENCH_EOF'\n%s\nBENCH_EOF\n", bashPath, content)
	}
	return []string{"sh", "-c", script}, nil
}

// bashEdit maps to sed -i. macOS BSD sed needs a backup-suffix arg, so
// we use sed -i.bak; agents on linux + macos both encounter this. The
// .bak side-effect file is created in the same directory and is part
// of the bench/.ash/bench-tmp/ tree (cleaned up after the run).
func bashEdit(a map[string]any) ([]string, error) {
	path, ok := a["path"].(string)
	if !ok || path == "" {
		return nil, fmt.Errorf("bench: edit case missing path")
	}
	old, _ := a["old"].(string)
	new, _ := a["new"].(string)
	if old == "" {
		return nil, fmt.Errorf("bench: edit case missing old (only string-replace mode supported)")
	}
	// Use | as the sed delimiter to avoid escape collisions with /
	// in path-shaped strings; old/new must not contain |.
	expr := fmt.Sprintf("s|%s|%s|g", old, new)
	return []string{"sed", "-i.bak", expr, path}, nil
}

// bashDiff is `diff A B`. The --stat ash variant has no clean bash
// equivalent (diff -q gives \"differ\"/\"identical\", not counts), so
// we keep the same unified diff form — ash's win there is structural,
// and a misleading bash form would muddy the comparison.
func bashDiff(a map[string]any) ([]string, error) {
	path, ok := a["path"].(string)
	if !ok || path == "" {
		return nil, fmt.Errorf("bench: diff case missing path")
	}
	other, _ := a["other"].(string)
	if other == "" {
		return nil, fmt.Errorf("bench: diff case missing other (no honest bash translation for content-string diff)")
	}
	return []string{"diff", path, other}, nil
}

func bashFind(a map[string]any) ([]string, error) {
	path := strArg(a, "path", ".")
	glob := strArg(a, "glob", "")
	typ := strArg(a, "type", "")
	maxDepth := strArg(a, "depth", "")

	argv := []string{"find", path}
	if maxDepth != "" {
		if n, err := strconv.Atoi(maxDepth); err == nil && n > 0 {
			argv = append(argv, "-maxdepth", strconv.Itoa(n))
		}
	}
	switch typ {
	case "file":
		argv = append(argv, "-type", "f")
	case "dir":
		argv = append(argv, "-type", "d")
	case "symlink":
		argv = append(argv, "-type", "l")
	}
	if glob != "" {
		// Strip a leading **/ — bash find -name matches the leaf name and
		// recurses by default. `**/*.go` becomes `*.go`.
		leaf := strings.TrimPrefix(glob, "**/")
		argv = append(argv, "-name", leaf)
		// If the original glob was leaf-only (no **/) and we're using a
		// type filter, ensure -type is set to file (matches the agent's
		// usual expectation for `-name`).
	}
	return argv, nil
}

func bashGrep(a map[string]any) ([]string, error) {
	pattern, ok := a["pattern"].(string)
	if !ok || pattern == "" {
		return nil, fmt.Errorf("bench: grep case missing pattern")
	}
	path := strArg(a, "path", ".")
	filesOnly := boolArg(a, "fo", false)
	fixed := boolArg(a, "lit", false)

	flags := "-rn"
	if filesOnly {
		flags = "-rln"
	}
	if fixed {
		flags += "F"
	}
	return []string{"grep", flags, pattern, path}, nil
}

func bashRead(a map[string]any) ([]string, error) {
	path, ok := a["path"].(string)
	if !ok || path == "" {
		return nil, fmt.Errorf("bench: read case missing path")
	}
	rng := strArg(a, "range", "")
	kind := strArg(a, "unit", "lines")
	if rng == "" {
		return []string{"cat", path}, nil
	}
	// Range form. Lines → sed -n 'start,endp'; bytes → dd-shaped, but
	// agents reach for sed/head/tail combos rather than dd, so for byte
	// ranges fall back to head -c which is the closest single-call form.
	parts := strings.SplitN(rng, ":", 2)
	if len(parts) != 2 {
		return []string{"cat", path}, nil
	}
	start, end := parts[0], parts[1]
	if kind == "bytes" {
		// `head -c N` returns the first N bytes; range start>1 isn't
		// expressible in one bash call. Use tail+head when start>1.
		s, _ := strconv.Atoi(start)
		e, _ := strconv.Atoi(end)
		if s <= 1 {
			return []string{"head", "-c", strconv.Itoa(e), path}, nil
		}
		// tail -c +S | head -c (E-S+1) — bash typically pipes; we don't
		// run pipelines here, so approximate with sed on bytes is wrong.
		// Use head -c E (closest single-call); document the limitation.
		return []string{"head", "-c", strconv.Itoa(e), path}, nil
	}
	// lines (default). Agents typically use sed -n.
	return []string{"sed", "-n", fmt.Sprintf("%s,%sp", start, end), path}, nil
}

func bashGit(a map[string]any) ([]string, error) {
	op, ok := a["op"].(string)
	if !ok || op == "" {
		return nil, fmt.Errorf("bench: git case missing op")
	}
	switch op {
	case "status":
		return []string{"git", "status"}, nil
	case "log":
		argv := []string{"git", "log"}
		if lim := strArg(a, "limit", ""); lim != "" {
			argv = append(argv, "-n", lim)
		}
		if r := strArg(a, "range", ""); r != "" {
			argv = append(argv, r)
		}
		if au := strArg(a, "author", ""); au != "" {
			argv = append(argv, "--author", au)
		}
		if since := strArg(a, "since", ""); since != "" {
			argv = append(argv, "--since", since)
		}
		if until := strArg(a, "until", ""); until != "" {
			argv = append(argv, "--until", until)
		}
		if ps := strArg(a, "pathspec", ""); ps != "" {
			argv = append(argv, "--", ps)
		}
		return argv, nil
	default:
		return nil, fmt.Errorf("bench: no bash translation for git op %q", op)
	}
}

func bashStat(a map[string]any) ([]string, error) {
	paths := strArg(a, "paths", "")
	if paths == "" {
		paths = strArg(a, "path", "")
	}
	if paths == "" {
		return nil, fmt.Errorf("bench: stat case missing paths")
	}
	parts := strings.Split(paths, ",")
	argv := []string{"stat"}
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			argv = append(argv, p)
		}
	}
	return argv, nil
}

func strArg(a map[string]any, key, def string) string {
	v, ok := a[key]
	if !ok {
		return def
	}
	if s, ok := v.(string); ok {
		return s
	}
	return def
}

func boolArg(a map[string]any, key string, def bool) bool {
	v, ok := a[key]
	if !ok {
		return def
	}
	if b, ok := v.(bool); ok {
		return b
	}
	if s, ok := v.(string); ok {
		switch strings.ToLower(s) {
		case "true", "1", "yes":
			return true
		case "false", "0", "no":
			return false
		}
	}
	return def
}
