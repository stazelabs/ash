// Package mcpschema generates MCP (Model Context Protocol) tool definitions
// from the canonical verb registry in internal/verbs/help. This is the
// third artifact derived from the same source — alongside `ash help` text
// and the vocab inventory in docs/vocab/ — so the three never drift.
//
// The output is JSON Schema draft 2020-12 (MCP's required dialect) wrapped
// in MCP's tools/list response shape. The artifact is checked in at
// docs/mcp/tools.json; ASH-104 (ashmcp) embeds it via //go:embed so the
// MCP server has zero startup roundtrip to ashd to discover tools.
//
// Drift is a build failure: `make schema` regenerates,
// `make schema-check` diffs against the checked-in artifact.
package mcpschema

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/stazelabs/ash/internal/verbs/help"
)

// Dialect is the JSON Schema dialect URI required by MCP.
const Dialect = "https://json-schema.org/draft/2020-12/schema"

// ToolNamePrefix is the namespace prefix for ash MCP tool names. Per
// docs/revolutionary-directions.md Part 2: tools surface as `ash_read`,
// `ash_grep`, etc., so they coexist with harness built-ins without
// colliding.
const ToolNamePrefix = "ash_"

// GeneratedBy identifies the producer for traceability. Surfaces in the
// JSON artifact so a stranger reading docs/mcp/tools.json can find the
// generator.
const GeneratedBy = "cmd/ashschema (ASH-105)"

// Tool is one MCP tool definition. JSON shape matches MCP's tools/list
// response entries exactly.
type Tool struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema InputSchema `json:"inputSchema"`
}

// InputSchema is the JSON Schema for a tool's input arguments. The
// $schema field declares the dialect; additionalProperties:false makes
// unknown args a schema-validation error rather than a daemon-side surprise
// (ASH-116 added the same guard at the verb level).
type InputSchema struct {
	Schema               string              `json:"$schema"`
	Type                 string              `json:"type"`
	Properties           map[string]Property `json:"properties"`
	Required             []string            `json:"required,omitempty"`
	AdditionalProperties bool                `json:"additionalProperties"`
}

// Property is one argument's JSON Schema. Default uses any so booleans
// and integers serialize unquoted (the registry stores them as strings
// like "true" / "256"; we parse to typed JSON before emitting).
type Property struct {
	Type        string   `json:"type"`
	Description string   `json:"description"`
	Enum        []string `json:"enum,omitempty"`
	Default     any      `json:"default,omitempty"`
}

// ToolList is the wrapper artifact written to docs/mcp/tools.json. The
// outer GeneratedBy / Dialect fields are not part of the MCP wire format
// but are useful provenance for the static artifact. An MCP server reads
// the Tools slice and serves it verbatim.
type ToolList struct {
	GeneratedBy string `json:"generated_by"`
	Dialect     string `json:"dialect"`
	Tools       []Tool `json:"tools"`
}

// Generate builds the MCP tool list from the help registry. Verbs are
// emitted in registry order so the artifact is stable across runs (the
// registry itself is a hand-ordered slice).
func Generate(reg []help.VerbSchema) (*ToolList, error) {
	out := &ToolList{
		GeneratedBy: GeneratedBy,
		Dialect:     Dialect,
		Tools:       make([]Tool, 0, len(reg)),
	}
	for _, vs := range reg {
		t, err := toolForVerb(vs)
		if err != nil {
			return nil, fmt.Errorf("verb %s: %w", vs.Verb, err)
		}
		out.Tools = append(out.Tools, t)
	}
	return out, nil
}

// Marshal emits the canonical pretty-printed JSON. Two-space indent and a
// trailing newline match the conventions used by docs/vocab/inventory.json
// so CI diff output stays readable.
func (tl *ToolList) Marshal() ([]byte, error) {
	b, err := json.MarshalIndent(tl, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

func toolForVerb(vs help.VerbSchema) (Tool, error) {
	props, required, err := propertiesForVerb(vs)
	if err != nil {
		return Tool{}, err
	}
	return Tool{
		Name:        ToolNamePrefix + vs.Verb,
		Description: vs.Description,
		InputSchema: InputSchema{
			Schema:               Dialect,
			Type:                 "object",
			Properties:           props,
			Required:             required,
			AdditionalProperties: false,
		},
	}, nil
}

// propertiesForVerb collapses help.ArgSchema entries into JSON Schema
// properties. Duplicate Name entries (edit's `new` appears once per mode)
// are coalesced — same wire key means one property. Mode/op constraints
// land in the description so the agent sees them inline.
func propertiesForVerb(vs help.VerbSchema) (map[string]Property, []string, error) {
	props := make(map[string]Property)
	requiredSet := map[string]struct{}{}

	for _, a := range vs.Args {
		jsonType, err := jsonTypeFor(a.Type)
		if err != nil {
			return nil, nil, fmt.Errorf("arg %q: %w", a.Name, err)
		}
		def, err := parseDefault(a.Default, a.Type)
		if err != nil {
			return nil, nil, fmt.Errorf("arg %q: %w", a.Name, err)
		}
		desc := decorate(a.Description, a)

		existing, dup := props[a.Name]
		if !dup {
			props[a.Name] = Property{
				Type:        jsonType,
				Description: desc,
				Enum:        a.Values,
				Default:     def,
			}
		} else {
			// Same wire key, different mode/op. Sanity-check that the
			// type matches; coalesce descriptions; keep first non-nil
			// default; union the enum sets.
			if existing.Type != jsonType {
				return nil, nil, fmt.Errorf("arg %q: type mismatch across duplicates (%s vs %s)", a.Name, existing.Type, jsonType)
			}
			merged := existing
			if existing.Description == "" {
				merged.Description = desc
			} else if desc != "" && !strings.Contains(existing.Description, desc) {
				merged.Description = existing.Description + " / " + desc
			}
			if merged.Default == nil && def != nil {
				merged.Default = def
			}
			merged.Enum = unionStrings(existing.Enum, a.Values)
			props[a.Name] = merged
		}

		if a.Required {
			requiredSet[a.Name] = struct{}{}
		}
	}

	required := make([]string, 0, len(requiredSet))
	for k := range requiredSet {
		required = append(required, k)
	}
	sort.Strings(required)
	return props, required, nil
}

// decorate prepends [op=...] or [mode=...] tags to the description when
// the arg is op- or mode-restricted. The base registry uses square-bracket
// tags inline (e.g. "[diff] return per-file..."); the prefix is only
// added when the registry didn't already include it, so we don't end up
// double-tagging.
func decorate(desc string, a help.ArgSchema) string {
	var prefix string
	if a.Mode != "" {
		prefix = "[mode=" + a.Mode + "] "
	} else if len(a.Ops) > 0 {
		prefix = "[op=" + strings.Join(a.Ops, "|") + "] "
	}
	if prefix == "" {
		return desc
	}
	// Avoid double-prefixing when the description already opens with a
	// square-bracket tag in the registry (e.g. "[diff/show] ...").
	if strings.HasPrefix(desc, "[") {
		return desc
	}
	return prefix + desc
}

func jsonTypeFor(t string) (string, error) {
	switch t {
	case "string":
		return "string", nil
	case "int":
		return "integer", nil
	case "bool":
		return "boolean", nil
	default:
		return "", fmt.Errorf("unsupported registry type %q", t)
	}
}

// parseDefault converts the registry's stringly-typed default into a
// JSON-typed value so booleans / integers serialize unquoted in the
// schema. An empty string is treated as "no default" — for string args
// that's the same shape; for bool/int the registry always sets a real
// default when one applies.
func parseDefault(def, t string) (any, error) {
	if def == "" {
		return nil, nil
	}
	switch t {
	case "string":
		return def, nil
	case "int":
		var n int
		if _, err := fmt.Sscanf(def, "%d", &n); err != nil {
			return nil, fmt.Errorf("default %q not an int: %w", def, err)
		}
		return n, nil
	case "bool":
		switch strings.ToLower(def) {
		case "true":
			return true, nil
		case "false":
			return false, nil
		default:
			return nil, fmt.Errorf("default %q not a bool", def)
		}
	}
	return nil, fmt.Errorf("unsupported type %q for default", t)
}

func unionStrings(a, b []string) []string {
	if len(a) == 0 {
		return b
	}
	if len(b) == 0 {
		return a
	}
	seen := make(map[string]struct{}, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, s := range a {
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	for _, s := range b {
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}
