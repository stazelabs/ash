// Command ashmcp is the MCP server adapter for ash. It speaks Model Context
// Protocol over stdio to an MCP-aware harness (Claude Code, Claude Desktop,
// Cursor) and dispatches typed tool calls to the existing ashd daemon over
// the per-project UDS — same wire that `ash` uses.
//
//	[harness] <--MCP stdio--> [ashmcp] <--UDS msgpack--> [ashd] <--SQLite--> ledger
//
// Tool definitions come from the ASH-105 schema artifact (generated from
// help.Registry). The canonical artifact is checked in at docs/mcp/tools.json;
// an identical copy lives at cmd/ashmcp/tools.json because //go:embed cannot
// reach outside the package directory. `make schema` regenerates both at once
// and `make schema-check` gates either drifting.
//
// Per ASH-104 scope, only read-side verbs are exposed in v1 (writes phase 2).
// Phase 1 verbs exposed (8): read, find, grep, stat, git, report, metrics, help.
// Phase 2 (deferred): write, edit, diff once stdio MCP behavior is understood
// in production sessions.
package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/stazelabs/ash/internal/mcpschema"
	"github.com/stazelabs/ash/internal/proto"
)

//go:embed tools.json
var toolsJSON []byte

// readSideVerbs lists the verbs ashmcp exposes in v1. Keys are wire-verb
// names (the proto.Request.Verb the daemon dispatches on); MCP tool names
// are derived by prefixing with mcpschema.ToolNamePrefix (e.g. read ->
// ash_read). Per ASH-104 the writing verbs (write, edit, diff) and
// side-effecting verbs (bench, hook, init, uninit, stop, test) are
// deliberately omitted from v1 — they roll out after stdio MCP behavior
// is observed on real sessions.
var readSideVerbs = map[string]bool{
	"read":    true,
	"find":    true,
	"grep":    true,
	"stat":    true,
	"git":     true,
	"report":  true,
	"metrics": true,
	"help":    true,
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "ashmcp:", err)
		os.Exit(1)
	}
}

func run() error {
	tools, err := loadEmbeddedTools(toolsJSON)
	if err != nil {
		return fmt.Errorf("load embedded tools.json: %w", err)
	}

	srv := mcp.NewServer(&mcp.Implementation{
		Name:    "ashmcp",
		Title:   "ash",
		Version: proto.AshVersion,
	}, nil)

	registered := 0
	for _, t := range tools {
		verb, ok := stripToolPrefix(t.Name)
		if !ok || !readSideVerbs[verb] {
			continue
		}
		srv.AddTool(&mcp.Tool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: json.RawMessage(t.InputSchema),
		}, makeHandler(verb))
		registered++
	}
	if registered == 0 {
		return fmt.Errorf("no tools registered; tools.json may be stale or empty")
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	return srv.Run(ctx, &mcp.StdioTransport{})
}

// embeddedTool is the on-disk shape of one entry in tools.json. InputSchema
// is captured as a raw JSON message so we hand the bytes straight to
// mcp.Tool.InputSchema without re-marshaling.
type embeddedTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

type embeddedToolList struct {
	GeneratedBy string         `json:"generated_by"`
	Dialect     string         `json:"dialect"`
	Tools       []embeddedTool `json:"tools"`
}

func loadEmbeddedTools(body []byte) ([]embeddedTool, error) {
	var tl embeddedToolList
	if err := json.Unmarshal(body, &tl); err != nil {
		return nil, err
	}
	if tl.Dialect != mcpschema.Dialect {
		return nil, fmt.Errorf("unexpected dialect %q (want %q)", tl.Dialect, mcpschema.Dialect)
	}
	return tl.Tools, nil
}

func stripToolPrefix(toolName string) (string, bool) {
	p := mcpschema.ToolNamePrefix
	if len(toolName) <= len(p) || toolName[:len(p)] != p {
		return "", false
	}
	return toolName[len(p):], true
}
