package vocab

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// literalRoots is the set of directories scanned for string literals
// inside pretty / footer / header renderers. Includes cmd/ash because
// the metrics footer (`[ash bi=... bo=... ...]`) is rendered client-side
// in cmd/ash/main.go, not in any verb package.
var literalRoots = []string{
	"internal/verbs",
	"cmd/ash",
}

// extractLiterals walks every non-test .go file under literalRoots,
// classifies each string-literal as header / footer / label / glue,
// and returns headers, footers, and labels. Footers are folded into
// headers (the headers category in the output) since they share
// rendering intent — a frame around the response body.
func extractLiterals(repoRoot string, counter Counter) (headers, labels []Entry, err error) {
	type rawLit struct {
		Value string
		Func  string
		File  string
		Line  int
	}
	var raws []rawLit
	fset := token.NewFileSet()
	for _, sub := range literalRoots {
		root := filepath.Join(repoRoot, sub)
		werr := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			f, perr := parser.ParseFile(fset, path, nil, 0)
			if perr != nil {
				return fmt.Errorf("parse %s: %w", path, perr)
			}
			for _, decl := range f.Decls {
				fd, ok := decl.(*ast.FuncDecl)
				if !ok {
					continue
				}
				ast.Inspect(fd, func(n ast.Node) bool {
					lit, ok := n.(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						return true
					}
					s := unquoteString(lit.Value)
					if s == "" {
						return true
					}
					rel, rerr := filepath.Rel(repoRoot, path)
					if rerr != nil {
						rel = path
					}
					raws = append(raws, rawLit{
						Value: s,
						Func:  fd.Name.Name,
						File:  rel,
						Line:  fset.Position(lit.Pos()).Line,
					})
					return true
				})
			}
			return nil
		})
		if werr != nil {
			return nil, nil, werr
		}
	}

	// Bucket each literal.
	type bucket map[string]*aggLit
	headerMap := bucket{}
	labelMap := bucket{}
	for _, r := range raws {
		cat := classifyLiteral(r.Value, r.Func)
		switch cat {
		case "header":
			addLit(headerMap, normalizeHeader(r.Value), r)
		case "footer":
			addLit(headerMap, r.Value, r)
		case "label":
			addLit(labelMap, canonicalLabel(r.Value), r)
		}
	}

	headers = entriesFromBucket(headerMap, counter)
	labels = entriesFromBucket(labelMap, counter)
	return headers, labels, nil
}

type aggLit struct {
	literal string
	sites   []Site
}

func addLit(m map[string]*aggLit, key string, r struct {
	Value string
	Func  string
	File  string
	Line  int
}) {
	a, ok := m[key]
	if !ok {
		a = &aggLit{literal: key}
		m[key] = a
	}
	a.sites = append(a.sites, Site{File: r.File, Line: r.Line})
}

func entriesFromBucket(m map[string]*aggLit, counter Counter) []Entry {
	out := make([]Entry, 0, len(m))
	for _, a := range m {
		sort.Slice(a.sites, func(i, j int) bool {
			if a.sites[i].File != a.sites[j].File {
				return a.sites[i].File < a.sites[j].File
			}
			return a.sites[i].Line < a.sites[j].Line
		})
		out = append(out, Entry{
			Literal: a.literal,
			Tokens:  counter.Count(a.literal),
			Sites:   a.sites,
		})
	}
	sort.Slice(out, func(i, j int) bool { return entryLess(out[i], out[j]) })
	return out
}

// classifyLiteral assigns one of: header / footer / label / glue.
// Glue entries are dropped from the inventory.
func classifyLiteral(s, fn string) string {
	switch {
	case strings.HasPrefix(s, "§"):
		return "header"
	case strings.HasPrefix(s, "[ash ") || strings.HasPrefix(s, "[ash bi"):
		return "footer"
	case strings.HasPrefix(s, "[truncation"):
		return "footer"
	}
	// Labels live inside pretty / format functions, never in ParseArgs.
	if !looksLikeRenderFunc(fn) {
		return "glue"
	}
	if !looksLikeLabel(s) {
		return "glue"
	}
	return "label"
}

// looksLikeRenderFunc picks function names that contribute to pretty
// rendering. The naming conventions in this repo are stable enough that
// a few prefixes cover the surface:
//   - PrettyResponse / prettyXxx (every verb)
//   - format / Format / write... helpers in those files
//   - main / runX in cmd/ash (footer / hook denials etc.)
func looksLikeRenderFunc(fn string) bool {
	if fn == "" {
		return false
	}
	low := strings.ToLower(fn)
	for _, pre := range []string{"pretty", "format", "render", "write", "footer", "header"} {
		if strings.HasPrefix(low, pre) {
			return true
		}
	}
	// cmd/ash main + helpers: the metrics footer lives in main.go.
	if fn == "main" || fn == "run" || strings.HasPrefix(fn, "print") {
		return true
	}
	return false
}

// fmtDirectiveRE matches Go format-string directives (%d, %s, %5d, %-10s,
// %v, etc.). Stripped before classification so a format string like
// " w=%d" reduces to " w=" and is recognized as a label.
var fmtDirectiveRE = regexp.MustCompile(`%[-+# 0]*\d*\.?\d*[a-zA-Z%]`)

// labelLikeRE matches the canonical label shape: an identifier-ish run
// (letters / digits / underscore / single inner spaces) followed by `:`
// or `=`. Case-insensitive.
var labelLikeRE = regexp.MustCompile(`(?i)^[a-z][a-z0-9_]*([ ][a-z0-9_]+)*[:=]$`)

// looksLikeLabel returns true for short strings that look like a column
// or key label — e.g. "verb:", "tokens=", "tok/KiB", " w=" (after
// format-directive stripping). Heuristic:
//
//   - length ≤ 40 (after stripping)
//   - starts with a letter, ends in `:` or `=`
//   - contains only ASCII letters/digits/`_`/space
//
// Glue ("  ", "\n", ", ", ": ", etc.) and pure format directives ("%d")
// are dropped.
func looksLikeLabel(s string) bool {
	stripped := strings.TrimSpace(fmtDirectiveRE.ReplaceAllString(s, ""))
	if stripped == "" {
		return false
	}
	if len(stripped) > 40 {
		return false
	}
	return labelLikeRE.MatchString(stripped)
}

// canonicalLabel is the form of a label literal that the inventory
// indexes by — directive-stripped and whitespace-trimmed. Matches what
// the agent actually sees on the wire (the format directive is
// substituted server-side before tokenization).
func canonicalLabel(s string) string {
	return strings.TrimSpace(fmtDirectiveRE.ReplaceAllString(s, ""))
}

// normalizeHeader collapses the formatting variability in `§…`
// headers down to a vocabulary signature. Stripping any trailing
// format-directive tail (e.g. "%d verb(s)") leaves the per-verb header
// *shape* — e.g. `§find:`, `§grep:`. We keep the trailing `:` because
// it is tokenized.
func normalizeHeader(s string) string {
	t := strings.TrimSpace(s)
	if i := strings.IndexAny(t, "%"); i >= 0 {
		t = strings.TrimSpace(t[:i])
	}
	return t
}
