package mcpschema

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// OutputSchema is the JSON Schema for a tool's structured output. Per
// the MCP 2025-06-18 spec it describes the shape of CallToolResult's
// structuredContent. ashmcp emits a decoded rsp.Data as
// structuredContent on success; harnesses that validate against this
// schema can avoid re-parsing the TextContent fallback.
type OutputSchema struct {
	Schema               string                 `json:"$schema"`
	Type                 string                 `json:"type"`
	Properties           map[string]*SchemaNode `json:"properties"`
	Required             []string               `json:"required,omitempty"`
	AdditionalProperties bool                   `json:"additionalProperties"`
}

// SchemaNode is a recursive JSON Schema node used inside output schemas.
// Fields are emitted only when non-empty so leaf primitives don't carry
// blank Items/Properties slots that confuse strict validators.
type SchemaNode struct {
	Type                 string                 `json:"type,omitempty"`
	Description          string                 `json:"description,omitempty"`
	Items                *SchemaNode            `json:"items,omitempty"`
	Properties           map[string]*SchemaNode `json:"properties,omitempty"`
	Required             []string               `json:"required,omitempty"`
	AdditionalProperties *bool                  `json:"additionalProperties,omitempty"`
}

// verbPackageDir maps a wire-verb name to its source package directory
// relative to the repo root. Most verbs follow internal/verbs/<verb>;
// only `init` deviates (the package is `initverb` because `init` is
// reserved for Go's package-init functions).
var verbPackageDir = map[string]string{
	"init": "internal/verbs/initverb",
}

// externalSchemas pre-bakes JSON Schemas for types referenced by Result
// structs that live outside the verb package. The AST walker can't
// follow cross-package references without expanding scope dramatically;
// the universe of external types in the wire surface is small enough
// that hardcoding them is cheaper than building a multi-package
// resolver.
var externalSchemas = map[string]*SchemaNode{
	"proto.TruncInfo": {
		Type:        "object",
		Description: "Truncation hint: how many records were dropped and the caps that triggered the drop.",
		Properties: map[string]*SchemaNode{
			"trunc": {Type: "integer", Description: "Number of records dropped."},
			"limit": {Type: "integer", Description: "Cap that triggered truncation."},
			"max":   {Type: "integer", Description: "Hard cap (max possible value of limit)."},
		},
		Required:             []string{"limit", "max", "trunc"},
		AdditionalProperties: ptrBool(false),
	},
}

func ptrBool(b bool) *bool { return &b }

// packageTypes is the set of struct declarations discovered in one verb
// package, keyed by type name.
type packageTypes struct {
	structs map[string]*ast.StructType
	// fieldComments captures the trailing // comment for each field, by
	// (struct name, field name). Used as the description when the
	// struct tag itself doesn't carry one.
	fieldComments map[string]map[string]string
}

// loadPackageTypes parses every non-test .go file in dir and collects
// struct declarations plus per-field trailing comments. Returns a
// non-nil packageTypes even when the dir contains no struct decls so
// the caller can rely on map presence.
func loadPackageTypes(dir string) (*packageTypes, error) {
	out := &packageTypes{
		structs:       map[string]*ast.StructType{},
		fieldComments: map[string]map[string]string{},
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		f, perr := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if perr != nil {
			return nil, fmt.Errorf("parse %s: %w", path, perr)
		}
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, spec := range gd.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				st, ok := ts.Type.(*ast.StructType)
				if !ok {
					continue
				}
				out.structs[ts.Name.Name] = st
				out.fieldComments[ts.Name.Name] = collectFieldComments(st)
			}
		}
	}
	return out, nil
}

// collectFieldComments harvests the trailing // comment for each field
// in a struct. We prefer line comments (e.g. `Foo string // explains foo`)
// because that is the convention the verb Result structs use; the doc
// comment above the field is captured as a fallback.
func collectFieldComments(st *ast.StructType) map[string]string {
	out := map[string]string{}
	if st.Fields == nil {
		return out
	}
	for _, f := range st.Fields.List {
		var comment string
		if f.Comment != nil {
			comment = strings.TrimSpace(f.Comment.Text())
		} else if f.Doc != nil {
			comment = strings.TrimSpace(f.Doc.Text())
		}
		comment = strings.TrimSpace(comment)
		for _, name := range f.Names {
			out[name.Name] = comment
		}
	}
	return out
}

// generateOutputSchema builds the JSON Schema for one verb's Result
// type by parsing the verb's package directory. Returns nil (no error)
// when the package has no "Result" struct — some side-effecting verbs
// may legitimately have no structured output, though every live verb
// currently does. Returns an error only on parse or unresolvable-type
// failures, which are real bugs in the source.
func generateOutputSchema(repoRoot, verb string) (*OutputSchema, error) {
	dir, ok := verbPackageDir[verb]
	if !ok {
		dir = filepath.Join("internal", "verbs", verb)
	}
	pkg, err := loadPackageTypes(filepath.Join(repoRoot, dir))
	if err != nil {
		return nil, fmt.Errorf("verb %s: %w", verb, err)
	}
	result, ok := pkg.structs["Result"]
	if !ok {
		return nil, nil
	}
	props, required, err := schemaFromStruct(pkg, "Result", result)
	if err != nil {
		return nil, fmt.Errorf("verb %s: %w", verb, err)
	}
	return &OutputSchema{
		Schema:               Dialect,
		Type:                 "object",
		Properties:           props,
		Required:             required,
		AdditionalProperties: false,
	}, nil
}

// schemaFromStruct walks one struct's fields and returns the JSON
// Schema properties map plus the required[] slice (fields lacking an
// `omitempty` msgpack tag). Fields with a `-` msgpack tag are skipped.
// Fields with no msgpack tag are skipped — only wire fields belong in
// the schema.
func schemaFromStruct(pkg *packageTypes, structName string, st *ast.StructType) (map[string]*SchemaNode, []string, error) {
	props := map[string]*SchemaNode{}
	requiredSet := map[string]struct{}{}
	if st.Fields == nil {
		return props, nil, nil
	}
	comments := pkg.fieldComments[structName]
	for _, f := range st.Fields.List {
		tag := parseMsgpackTag(f.Tag)
		if tag.skip {
			continue
		}
		if tag.name == "" {
			continue
		}
		node, err := schemaFromType(pkg, f.Type)
		if err != nil {
			return nil, nil, fmt.Errorf("field %q: %w", tag.name, err)
		}
		if node == nil {
			continue
		}
		desc := fieldDescription(f, comments)
		if desc != "" && node.Description == "" {
			node.Description = desc
		}
		props[tag.name] = node
		if !tag.omitempty {
			requiredSet[tag.name] = struct{}{}
		}
	}
	required := make([]string, 0, len(requiredSet))
	for k := range requiredSet {
		required = append(required, k)
	}
	sort.Strings(required)
	return props, required, nil
}

// schemaFromType resolves one Go type expression to a SchemaNode.
// Handles primitives, slices, pointers, maps, same-package struct
// references, and the small set of cross-package references we
// pre-baked in externalSchemas.
func schemaFromType(pkg *packageTypes, expr ast.Expr) (*SchemaNode, error) {
	switch t := expr.(type) {
	case *ast.Ident:
		if prim := primitiveSchema(t.Name); prim != nil {
			return prim, nil
		}
		if st, ok := pkg.structs[t.Name]; ok {
			return schemaFromNamedStruct(pkg, t.Name, st)
		}
		return nil, fmt.Errorf("unresolved type %q", t.Name)
	case *ast.SelectorExpr:
		pkgIdent, ok := t.X.(*ast.Ident)
		if !ok {
			return nil, fmt.Errorf("unsupported selector expression")
		}
		key := pkgIdent.Name + "." + t.Sel.Name
		if ext, ok := externalSchemas[key]; ok {
			cp := *ext
			return &cp, nil
		}
		return nil, fmt.Errorf("unresolved external type %q", key)
	case *ast.ArrayType:
		if ident, ok := t.Elt.(*ast.Ident); ok && ident.Name == "byte" {
			return &SchemaNode{Type: "string"}, nil
		}
		inner, err := schemaFromType(pkg, t.Elt)
		if err != nil {
			return nil, err
		}
		return &SchemaNode{Type: "array", Items: inner}, nil
	case *ast.StarExpr:
		return schemaFromType(pkg, t.X)
	case *ast.MapType:
		// JSON Schema only models maps as objects with
		// additionalProperties typing. Keys are always strings in JSON.
		_, err := schemaFromType(pkg, t.Value)
		if err != nil {
			return nil, err
		}
		return &SchemaNode{Type: "object", AdditionalProperties: ptrBool(true)}, nil
	case *ast.InterfaceType:
		return &SchemaNode{}, nil
	}
	return nil, fmt.Errorf("unsupported type expression %T", expr)
}

// schemaFromNamedStruct turns a same-package struct reference into a
// SchemaNode (type=object). Recursion through pkg.structs is bounded
// by the type graph being a DAG in practice — none of the verbs use
// self-referential Result types — so we don't track visited names.
func schemaFromNamedStruct(pkg *packageTypes, name string, st *ast.StructType) (*SchemaNode, error) {
	props, required, err := schemaFromStruct(pkg, name, st)
	if err != nil {
		return nil, err
	}
	return &SchemaNode{
		Type:                 "object",
		Properties:           props,
		Required:             required,
		AdditionalProperties: ptrBool(false),
	}, nil
}

// primitiveSchema maps Go primitive type names to JSON Schema nodes.
// Returns nil for non-primitive idents so the caller can fall back to
// named-struct lookup.
func primitiveSchema(name string) *SchemaNode {
	switch name {
	case "string":
		return &SchemaNode{Type: "string"}
	case "bool":
		return &SchemaNode{Type: "boolean"}
	case "int", "int8", "int16", "int32", "int64",
		"uint", "uint8", "uint16", "uint32", "uint64":
		return &SchemaNode{Type: "integer"}
	case "float32", "float64":
		return &SchemaNode{Type: "number"}
	}
	return nil
}

// msgpackTag is the parsed shape of one struct field's msgpack tag.
// skip is true for the `-` form; name is empty when the tag is absent.
type msgpackTag struct {
	name      string
	omitempty bool
	skip      bool
}

// parseMsgpackTag pulls the msgpack:"..." segment out of a struct tag
// literal and splits it on commas. Returns a zero-value tag when no
// msgpack key is present.
func parseMsgpackTag(tag *ast.BasicLit) msgpackTag {
	out := msgpackTag{}
	if tag == nil {
		return out
	}
	raw := strings.Trim(tag.Value, "`")
	for raw != "" {
		i := 0
		for i < len(raw) && raw[i] == ' ' {
			i++
		}
		raw = raw[i:]
		if raw == "" {
			break
		}
		colon := strings.IndexByte(raw, ':')
		if colon == -1 {
			break
		}
		key := raw[:colon]
		if colon+1 >= len(raw) || raw[colon+1] != '"' {
			break
		}
		end := strings.IndexByte(raw[colon+2:], '"')
		if end == -1 {
			break
		}
		val := raw[colon+2 : colon+2+end]
		raw = raw[colon+3+end:]
		if key == "msgpack" {
			parts := strings.Split(val, ",")
			if parts[0] == "-" {
				out.skip = true
				return out
			}
			out.name = parts[0]
			for _, p := range parts[1:] {
				if p == "omitempty" {
					out.omitempty = true
				}
			}
			return out
		}
	}
	return out
}

// fieldDescription pulls a human-readable description for one field.
// Prefers the trailing line comment; otherwise the doc comment above
// the field. Both are captured by collectFieldComments.
func fieldDescription(f *ast.Field, comments map[string]string) string {
	if len(f.Names) == 0 {
		return ""
	}
	return comments[f.Names[0].Name]
}
