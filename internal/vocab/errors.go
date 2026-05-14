package vocab

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
)

// errorRoots is the list of directories scanned for proto.Error{Code: ...}
// composite literals. Kept narrow on purpose: error construction is a
// daemon-side concern; client-only helpers don't carry error codes.
var errorRoots = []string{
	"internal/verbs",
	"internal/runner",
	"internal/jail",
}

// runnerExpansionProgs are the known caller values for the runner's
// `prog + "_failed"` / `prog + "_not_found"` non-literal pattern. The
// runner accepts any string but the live callers in the codebase pass
// these two. ASH-102's inventory expands the computed form to concrete
// codes here so the inventory records the strings the agent actually
// sees on the wire.
var runnerExpansionProgs = []string{"go", "git"}

// errorSite captures the per-call metadata we keep alongside each error
// code occurrence.
type errorSite struct {
	Code     string
	Hint     string
	File     string
	Line     int
	Computed bool
}

// extractErrors walks repoRoot's errorRoots and returns one Entry per
// distinct error code. The runner's non-literal computed form is kept
// as its own entry with Computed=true; concrete expansions for known
// callers appear as sibling entries.
func extractErrors(repoRoot string, counter Counter) ([]Entry, error) {
	var sites []errorSite
	for _, sub := range errorRoots {
		s, err := walkErrorRoot(filepath.Join(repoRoot, sub))
		if err != nil {
			return nil, err
		}
		// Repo-relative paths in Sites — the markdown links assume it.
		for i := range s {
			rel, err := filepath.Rel(repoRoot, s[i].File)
			if err == nil {
				s[i].File = rel
			}
		}
		sites = append(sites, s...)
	}

	sites = expandComputed(sites, runnerExpansionProgs)

	// Aggregate by code.
	type agg struct {
		sites []errorSite
		hints []string
	}
	by := make(map[string]*agg)
	for _, s := range sites {
		a, ok := by[s.Code]
		if !ok {
			a = &agg{}
			by[s.Code] = a
		}
		a.sites = append(a.sites, s)
		if s.Hint != "" && !contains(a.hints, s.Hint) {
			a.hints = append(a.hints, s.Hint)
		}
	}

	entries := make([]Entry, 0, len(by))
	for code, a := range by {
		// Sort sites by file/line for stable output.
		sort.Slice(a.sites, func(i, j int) bool {
			if a.sites[i].File != a.sites[j].File {
				return a.sites[i].File < a.sites[j].File
			}
			return a.sites[i].Line < a.sites[j].Line
		})
		siteList := make([]Site, 0, len(a.sites))
		for _, s := range a.sites {
			siteList = append(siteList, Site{File: s.File, Line: s.Line})
		}
		hints := make([]Hint, 0, len(a.hints))
		for _, h := range a.hints {
			hints = append(hints, Hint{Text: h, Tokens: counter.Count(h)})
		}
		entries = append(entries, Entry{
			Literal:  code,
			Tokens:   counter.Count(code),
			Sites:    siteList,
			Hints:    hints,
			Computed: a.sites[0].Computed,
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entryLess(entries[i], entries[j]) })
	return entries, nil
}

func walkErrorRoot(root string) ([]errorSite, error) {
	var sites []errorSite
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			cl, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			if !isProtoErrorType(cl.Type) {
				return true
			}
			site := errorSite{File: path, Line: fset.Position(cl.Pos()).Line}
			gotCode := false
			for _, elt := range cl.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				key, ok := kv.Key.(*ast.Ident)
				if !ok {
					continue
				}
				switch key.Name {
				case "Code":
					if lit, ok := kv.Value.(*ast.BasicLit); ok && lit.Kind == token.STRING {
						site.Code = unquoteString(lit.Value)
						gotCode = true
					} else {
						site.Code = renderComputedCode(kv.Value)
						site.Computed = true
						gotCode = true
					}
				case "Hint":
					if lit, ok := kv.Value.(*ast.BasicLit); ok && lit.Kind == token.STRING {
						site.Hint = unquoteString(lit.Value)
					}
				}
			}
			if gotCode {
				sites = append(sites, site)
			}
			return true
		})
		return nil
	})
	return sites, err
}

func isProtoErrorType(t ast.Expr) bool {
	switch tt := t.(type) {
	case *ast.SelectorExpr:
		if id, ok := tt.X.(*ast.Ident); ok && id.Name == "proto" && tt.Sel.Name == "Error" {
			return true
		}
	case *ast.StarExpr:
		return isProtoErrorType(tt.X)
	}
	return false
}

func renderComputedCode(e ast.Expr) string {
	be, ok := e.(*ast.BinaryExpr)
	if !ok || be.Op != token.ADD {
		return "<computed>"
	}
	return exprToken(be.X) + "+" + exprToken(be.Y)
}

func exprToken(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.Ident:
		return "<" + x.Name + ">"
	case *ast.BasicLit:
		return x.Value
	default:
		return "?"
	}
}

// expandComputed turns runner.go-style sites (Code: <prog>+"_failed") into
// concrete codes for each known caller value. The original computed site
// is retained.
func expandComputed(sites []errorSite, progs []string) []errorSite {
	out := make([]errorSite, 0, len(sites)+len(sites)*len(progs))
	for _, s := range sites {
		out = append(out, s)
		if !s.Computed {
			continue
		}
		i := strings.Index(s.Code, `+"`)
		if i < 0 || !strings.HasSuffix(s.Code, `"`) {
			continue
		}
		suffix := s.Code[i+2 : len(s.Code)-1]
		for _, prog := range progs {
			out = append(out, errorSite{
				Code:     prog + suffix,
				Hint:     s.Hint,
				File:     s.File,
				Line:     s.Line,
				Computed: false,
			})
		}
	}
	return out
}

func unquoteString(lit string) string {
	if len(lit) >= 2 && lit[0] == '"' && lit[len(lit)-1] == '"' {
		s := lit[1 : len(lit)-1]
		s = strings.ReplaceAll(s, `\"`, `"`)
		s = strings.ReplaceAll(s, `\n`, "\n")
		s = strings.ReplaceAll(s, `\t`, "\t")
		s = strings.ReplaceAll(s, `\\`, `\`)
		return s
	}
	return lit
}

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}
