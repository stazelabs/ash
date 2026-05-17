package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/stazelabs/ash/internal/mcpschema"
)

// TestEmbeddedToolsParse confirms the //go:embed bytes round-trip through
// loadEmbeddedTools, gating an empty / corrupt schema artifact at test
// time before it ships in a binary.
func TestEmbeddedToolsParse(t *testing.T) {
	tools, err := loadEmbeddedTools(toolsJSON)
	if err != nil {
		t.Fatalf("loadEmbeddedTools: %v", err)
	}
	if len(tools) == 0 {
		t.Fatal("embedded tools.json yielded zero tools")
	}
	for _, tt := range tools {
		if !strings.HasPrefix(tt.Name, mcpschema.ToolNamePrefix) {
			t.Errorf("tool %q: missing %q prefix", tt.Name, mcpschema.ToolNamePrefix)
		}
		if len(tt.InputSchema) == 0 {
			t.Errorf("tool %q: empty input schema", tt.Name)
		}
		var probe map[string]any
		if err := json.Unmarshal(tt.InputSchema, &probe); err != nil {
			t.Errorf("tool %q: schema not valid JSON: %v", tt.Name, err)
		} else if probe["type"] != "object" {
			t.Errorf("tool %q: schema type=%v, want \"object\"", tt.Name, probe["type"])
		}
	}
}

// TestExposedVerbsPresent ensures every verb ashmcp claims to expose
// actually has a matching definition in the embedded schema. Catches a
// future schema regeneration that renames or drops one of the entries.
func TestExposedVerbsPresent(t *testing.T) {
	tools, err := loadEmbeddedTools(toolsJSON)
	if err != nil {
		t.Fatalf("loadEmbeddedTools: %v", err)
	}
	have := map[string]bool{}
	for _, tt := range tools {
		v, ok := stripToolPrefix(tt.Name)
		if !ok {
			continue
		}
		have[v] = true
	}
	for v := range exposedVerbs {
		if !have[v] {
			t.Errorf("exposed verb %q missing from embedded tools.json", v)
		}
	}
}

// TestStripToolPrefix covers the prefix-trim helper at the boundary —
// the prefix string itself, an empty input, and a non-namespaced tool
// from a hypothetical second MCP server sharing the bus.
func TestStripToolPrefix(t *testing.T) {
	cases := []struct {
		in       string
		wantOk   bool
		wantVerb string
	}{
		{"ash_read", true, "read"},
		{"ash_", false, ""},
		{"", false, ""},
		{"read", false, ""},
		{"other_read", false, ""},
	}
	for _, c := range cases {
		v, ok := stripToolPrefix(c.in)
		if ok != c.wantOk || v != c.wantVerb {
			t.Errorf("stripToolPrefix(%q) = (%q, %v); want (%q, %v)",
				c.in, v, ok, c.wantVerb, c.wantOk)
		}
	}
}

// TestServerRegistersTools wires the real schema into a real mcp.Server
// against an in-memory transport, completes the MCP handshake, then calls
// tools/list to confirm the server advertises exactly the exposed
// verbs ashmcp claims. This is the closest we can get to "does ashmcp
// actually serve" without spawning a real ashd.
//
// Tool dispatch (ash_grep -> ashd roundtrip) is exercised by a separate
// integration test that requires the daemon; here we only verify the
// advertised surface.
func TestServerRegistersTools(t *testing.T) {
	tools, err := loadEmbeddedTools(toolsJSON)
	if err != nil {
		t.Fatalf("loadEmbeddedTools: %v", err)
	}

	srv := mcp.NewServer(&mcp.Implementation{Name: "ashmcp-test", Version: "test"}, nil)
	for _, tt := range tools {
		verb, ok := stripToolPrefix(tt.Name)
		if !ok || !exposedVerbs[verb] {
			continue
		}
		srv.AddTool(&mcp.Tool{
			Name:        tt.Name,
			Description: tt.Description,
			InputSchema: json.RawMessage(tt.InputSchema),
		}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: "ok"}},
			}, nil
		})
	}

	clientT, serverT := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go func() { _ = srv.Run(ctx, serverT) }()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "test"}, nil)
	sess, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	defer sess.Close()

	list, err := sess.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	got := map[string]bool{}
	for _, tt := range list.Tools {
		got[tt.Name] = true
	}
	for v := range exposedVerbs {
		want := mcpschema.ToolNamePrefix + v
		if !got[want] {
			t.Errorf("server did not advertise %q", want)
		}
	}
	if len(list.Tools) != len(exposedVerbs) {
		t.Errorf("server advertised %d tools, want exactly %d (orchestration verbs stay CLI-only)",
			len(list.Tools), len(exposedVerbs))
	}
}

// TestEmbedMatchesCanonical guards the build-time invariant that
// cmd/ashmcp/tools.json (the embed source) is byte-identical to
// docs/mcp/tools.json (the documented canonical artifact). `make schema`
// regenerates both; `make schema-check` enforces freshness vs the
// generator. This test enforces the cross-path equality at unit-test
// time so a stray manual edit to one copy fails CI immediately.
func TestEmbedMatchesCanonical(t *testing.T) {
	// Resolve the repo root from this test's source file so the test
	// works regardless of where `go test` is invoked from.
	canonical, err := os.ReadFile(filepath.Join("..", "..", "docs", "mcp", "tools.json"))
	if err != nil {
		t.Fatalf("read canonical artifact: %v", err)
	}
	if string(canonical) != string(toolsJSON) {
		t.Fatal("docs/mcp/tools.json and cmd/ashmcp/tools.json disagree; run `make schema`")
	}
}
