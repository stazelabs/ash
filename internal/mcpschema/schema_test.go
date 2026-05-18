package mcpschema

import (
	"encoding/json"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stazelabs/ash/internal/verbs/help"
)

// testRepoRoot resolves the repo root from this test file's path so
// Generate can find verb source packages regardless of where 'go test'
// is invoked from. internal/mcpschema/schema_test.go → ../..
func testRepoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(file), "..", "..")
}

// testCompactVerbs mirrors verbs.CompactHandlers() but is hand-listed
// here to keep internal/mcpschema decoupled from internal/verbs (which
// would be an import cycle via internal/verbs/help). The list is
// dual-validated by TestGenerateFormatKnob_RowShapeDefaultsCompact
// against the live ashschema generator.
func testCompactVerbs() map[string]bool {
	return map[string]bool{
		"metrics": true,
		"report":  true,
		"find":    true,
		"grep":    true,
		"stat":    true,
		"git":     true,
		"test":    true,
	}
}

// TestGenerateLiveRegistry exercises Generate against the real help
// registry and asserts the basic invariants — every verb maps to a tool,
// names are namespaced, and each tool carries a valid object schema with
// MCP's required dialect URI.
func TestGenerateLiveRegistry(t *testing.T) {
	tl, err := Generate(testRepoRoot(t), help.Registry(), testCompactVerbs())
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	reg := help.Registry()
	if len(tl.Tools) != len(reg) {
		t.Fatalf("tools=%d, want %d", len(tl.Tools), len(reg))
	}

	seenNames := map[string]bool{}
	for i, tool := range tl.Tools {
		want := ToolNamePrefix + reg[i].Verb
		if tool.Name != want {
			t.Errorf("tool[%d].Name = %q, want %q", i, tool.Name, want)
		}
		if seenNames[tool.Name] {
			t.Errorf("duplicate tool name %q", tool.Name)
		}
		seenNames[tool.Name] = true
		if tool.Description == "" {
			t.Errorf("tool %q: empty description", tool.Name)
		}
		if tool.InputSchema.Schema != Dialect {
			t.Errorf("tool %q: $schema = %q, want %q", tool.Name, tool.InputSchema.Schema, Dialect)
		}
		if tool.InputSchema.Type != "object" {
			t.Errorf("tool %q: type = %q, want object", tool.Name, tool.InputSchema.Type)
		}
		if tool.InputSchema.AdditionalProperties {
			t.Errorf("tool %q: additionalProperties must be false", tool.Name)
		}
	}
}

// TestGenerateRequiredAndDefaults verifies that required:true on the
// registry produces required[] entries on the JSON Schema, and that
// stringly-typed defaults coerce to proper JSON types.
func TestGenerateRequiredAndDefaults(t *testing.T) {
	tl, err := Generate(testRepoRoot(t), help.Registry(), testCompactVerbs())
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	byName := map[string]Tool{}
	for _, tool := range tl.Tools {
		byName[tool.Name] = tool
	}

	// `ash read` has required path, default unit="lines", default
	// bytes=262144, default meta=false. Spot-check all three types.
	read, ok := byName["ash_read"]
	if !ok {
		t.Fatalf("ash_read missing")
	}
	if !contains(read.InputSchema.Required, "path") {
		t.Errorf("ash_read: path not required (got %v)", read.InputSchema.Required)
	}
	if got := read.InputSchema.Properties["unit"].Default; got != "lines" {
		t.Errorf("ash_read.unit default = %v (%T), want string lines", got, got)
	}
	if got := read.InputSchema.Properties["bytes"].Default; got != 262144 {
		t.Errorf("ash_read.bytes default = %v (%T), want int 262144", got, got)
	}
	if got := read.InputSchema.Properties["meta"].Default; got != false {
		t.Errorf("ash_read.meta default = %v (%T), want bool false", got, got)
	}
}

// TestGenerateEditCoalescesNew checks that edit's `new` arg (which appears
// twice in the registry, once per mode) collapses to a single JSON Schema
// property whose description preserves both senses.
func TestGenerateEditCoalescesNew(t *testing.T) {
	tl, err := Generate(testRepoRoot(t), help.Registry(), testCompactVerbs())
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	var edit *Tool
	for i := range tl.Tools {
		if tl.Tools[i].Name == "ash_edit" {
			edit = &tl.Tools[i]
			break
		}
	}
	if edit == nil {
		t.Fatalf("ash_edit missing")
	}
	newProp, ok := edit.InputSchema.Properties["new"]
	if !ok {
		t.Fatalf("ash_edit.new property missing — coalescing dropped it")
	}
	if newProp.Type != "string" {
		t.Errorf("ash_edit.new type = %q, want string", newProp.Type)
	}
	// Description should mention both senses (string-mode replacement
	// and range-mode replacement) joined by " / ".
	if !strings.Contains(newProp.Description, " / ") {
		t.Errorf("ash_edit.new description = %q, expected coalesced ' / ' separator", newProp.Description)
	}
}

// TestGenerateFormatKnob (ASH-146) checks the MCP-only `format` property
// is injected into every tool's InputSchema with the right enum, default,
// and that it does not appear in the help registry (which would leak it
// to the CLI surface where it has no meaning — `ash` ships its own
// out-of-band --format flag).
func TestGenerateFormatKnob(t *testing.T) {
	tl, err := Generate(testRepoRoot(t), help.Registry(), testCompactVerbs())
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(tl.Tools) == 0 {
		t.Fatal("no tools generated")
	}
	for _, tool := range tl.Tools {
		f, ok := tool.InputSchema.Properties[FormatArg]
		if !ok {
			t.Errorf("%s: missing %q property", tool.Name, FormatArg)
			continue
		}
		if f.Type != "string" {
			t.Errorf("%s.%s type = %q, want string", tool.Name, FormatArg, f.Type)
		}
		// ASH-186: row-shape verbs default to FormatCompact; others
		// default to FormatJSON. Strip the prefix to match against
		// testCompactVerbs.
		verb := strings.TrimPrefix(tool.Name, ToolNamePrefix)
		wantDefault := FormatJSON
		if testCompactVerbs()[verb] {
			wantDefault = FormatCompact
		}
		if f.Default != wantDefault {
			t.Errorf("%s.%s default = %v, want %q", tool.Name, FormatArg, f.Default, wantDefault)
		}
		if !equalStringSet(f.Enum, []string{FormatJSON, FormatPretty, FormatCompact}) {
			t.Errorf("%s.%s enum = %v, want [%s %s %s]", tool.Name, FormatArg, f.Enum, FormatJSON, FormatPretty, FormatCompact)
		}
	}
	// `format` must not collide with any registered help arg — otherwise
	// the duplicate-coalescing path in propertiesForVerb would silently
	// overwrite the verb's own value.
	for _, vs := range help.Registry() {
		for _, a := range vs.Args {
			if a.Name == FormatArg {
				t.Errorf("verb %s registers an arg named %q, which collides with the MCP-only knob", vs.Verb, FormatArg)
			}
		}
	}
}

// TestGenerateEnums verifies enum lists make the round trip — picking
// read's --unit (lines|bytes) and git's --op as the witnesses.
func TestGenerateEnums(t *testing.T) {
	tl, err := Generate(testRepoRoot(t), help.Registry(), testCompactVerbs())
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	byName := map[string]Tool{}
	for _, tool := range tl.Tools {
		byName[tool.Name] = tool
	}
	read := byName["ash_read"].InputSchema.Properties["unit"]
	if !equalStringSet(read.Enum, []string{"lines", "bytes"}) {
		t.Errorf("ash_read.unit enum = %v, want [lines bytes]", read.Enum)
	}
	git := byName["ash_git"].InputSchema.Properties["op"]
	if !equalStringSet(git.Enum, []string{"status", "log", "diff", "show"}) {
		t.Errorf("ash_git.op enum = %v, want [status log diff show]", git.Enum)
	}
}

// TestMarshalRoundtrip ensures the artifact is valid JSON, idempotent
// across two consecutive Marshal calls, and parsable back into the same
// shape. CI lint diffs the bytes byte-for-byte so determinism matters.
func TestMarshalRoundtrip(t *testing.T) {
	tl, err := Generate(testRepoRoot(t), help.Registry(), testCompactVerbs())
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	b1, err := tl.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	b2, err := tl.Marshal()
	if err != nil {
		t.Fatalf("Marshal #2: %v", err)
	}
	if string(b1) != string(b2) {
		t.Fatalf("Marshal not deterministic across runs")
	}
	if !strings.HasSuffix(string(b1), "\n") {
		t.Errorf("artifact must end with a newline")
	}
	var got ToolList
	if err := json.Unmarshal(b1, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Dialect != Dialect {
		t.Errorf("roundtrip dialect = %q, want %q", got.Dialect, Dialect)
	}
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

func equalStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]int{}
	for _, s := range a {
		seen[s]++
	}
	for _, s := range b {
		seen[s]--
	}
	for _, n := range seen {
		if n != 0 {
			return false
		}
	}
	return true
}
