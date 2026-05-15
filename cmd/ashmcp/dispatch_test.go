package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/stazelabs/ash/internal/proto"
)

// resultWithTrunc is a minimal stand-in for a real verb Result. The
// msgpack tag on TruncInfo matches the convention every truncating verb
// uses, so the partial decode in decodeTruncInfo behaves identically
// against this shape and against the real grep/find/read/log/diff Result
// types.
type resultWithTrunc struct {
	Count     int              `msgpack:"count"`
	Truncated bool             `msgpack:"truncated,omitempty"`
	Hint      *proto.TruncInfo `msgpack:"truncation_hint,omitempty"`
}

// TestToolResultTruncationMeta exercises the ASH-127 surface: a verb
// response that carries TruncInfo must show up under _meta.ash.truncated
// on the MCP CallToolResult, and a short sentinel TextContent must be
// prepended so harnesses ignoring _meta still see the signal.
func TestToolResultTruncationMeta(t *testing.T) {
	rsp := &proto.Response{
		V:  proto.ProtocolVersion,
		ID: 1,
		OK: true,
		Data: proto.MustData(resultWithTrunc{
			Count:     256,
			Truncated: true,
			Hint:      &proto.TruncInfo{Trunc: 1, Limit: 256, Max: 4096},
		}),
	}
	res, err := toolResult(rsp)
	if err != nil {
		t.Fatalf("toolResult: %v", err)
	}
	if res.IsError {
		t.Fatal("IsError=true; truncation is a partial success, not a failure")
	}
	ash, ok := res.Meta["ash"].(map[string]any)
	if !ok {
		t.Fatalf("_meta.ash missing or wrong type: %T", res.Meta["ash"])
	}
	trunc, ok := ash["truncated"].(*proto.TruncInfo)
	if !ok {
		t.Fatalf("_meta.ash.truncated missing or wrong type: %T", ash["truncated"])
	}
	if trunc.Limit != 256 || trunc.Max != 4096 || trunc.Trunc != 1 {
		t.Errorf("TruncInfo = %+v; want {Trunc:1 Limit:256 Max:4096}", trunc)
	}
	// JSON round-trip confirms the wire shape uses the lowercase keys
	// documented in the outputSchema (ASH-124 baked these into the
	// proto.TruncInfo external-schema entry; we need json tags on the
	// type for Meta marshal to agree).
	b, err := json.Marshal(ash["truncated"])
	if err != nil {
		t.Fatalf("json.Marshal trunc: %v", err)
	}
	if got := string(b); got != `{"trunc":1,"limit":256,"max":4096}` {
		t.Errorf("Meta TruncInfo JSON = %s; want lowercase keys matching outputSchema", got)
	}
	if len(res.Content) < 2 {
		t.Fatalf("expected sentinel + body, got %d content blocks", len(res.Content))
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("first content block = %T; want *mcp.TextContent sentinel", res.Content[0])
	}
	if !strings.HasPrefix(tc.Text, "truncated:") {
		t.Errorf("sentinel = %q; want prefix \"truncated:\"", tc.Text)
	}
	if !strings.Contains(tc.Text, "256") || !strings.Contains(tc.Text, "4096") {
		t.Errorf("sentinel = %q; want both limit (256) and max (4096) inline", tc.Text)
	}
}

// TestToolResultHardCapSentinel covers the Limit==Max branch — the only
// remedy is narrowing, raising the limit cannot help. The message must
// reflect that so a harness reading just the sentinel doesn't retry
// with the same args plus --max=higher.
func TestToolResultHardCapSentinel(t *testing.T) {
	rsp := &proto.Response{
		V:  proto.ProtocolVersion,
		ID: 2,
		OK: true,
		Data: proto.MustData(resultWithTrunc{
			Count:     4096,
			Truncated: true,
			Hint:      &proto.TruncInfo{Trunc: 1, Limit: 4096, Max: 4096},
		}),
	}
	res, err := toolResult(rsp)
	if err != nil {
		t.Fatalf("toolResult: %v", err)
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("sentinel block = %T; want *mcp.TextContent", res.Content[0])
	}
	if !strings.Contains(tc.Text, "hard cap") {
		t.Errorf("sentinel = %q; want \"hard cap\" wording for Limit==Max", tc.Text)
	}
	if strings.Contains(tc.Text, "raise") {
		t.Errorf("sentinel = %q; must not suggest raising the limit when Limit==Max", tc.Text)
	}
}

// TestToolResultNoTruncation guards the common case: a successful verb
// response without truncation must not leak a truncated key into _meta
// and must not prepend a sentinel. Otherwise every successful call
// would carry a spurious "you may want to narrow" signal.
func TestToolResultNoTruncation(t *testing.T) {
	rsp := &proto.Response{
		V:    proto.ProtocolVersion,
		ID:   3,
		OK:   true,
		Data: proto.MustData(resultWithTrunc{Count: 42}),
	}
	res, err := toolResult(rsp)
	if err != nil {
		t.Fatalf("toolResult: %v", err)
	}
	if ash, ok := res.Meta["ash"].(map[string]any); ok {
		if _, ok := ash["truncated"]; ok {
			t.Errorf("_meta.ash.truncated set on a non-truncated response")
		}
	}
	if len(res.Content) != 1 {
		t.Errorf("Content count = %d; want 1 (no sentinel for non-truncated)", len(res.Content))
	}
}

// TestTruncationSentinelMatchesProto pins ashmcp's sentinel emission to
// the proto-level helper. Drift between the two would mean ashd
// accounts for one string in tokens_out_emit while the harness
// receives another — exactly the fidelity gap ASH-123 set out to
// eliminate.
func TestTruncationSentinelMatchesProto(t *testing.T) {
	rsp := &proto.Response{
		V:  proto.ProtocolVersion,
		ID: 4,
		OK: true,
		Data: proto.MustData(resultWithTrunc{
			Count:     100,
			Truncated: true,
			Hint:      &proto.TruncInfo{Trunc: 1, Limit: 100, Max: 4096},
		}),
	}
	res, err := toolResult(rsp)
	if err != nil {
		t.Fatalf("toolResult: %v", err)
	}
	want := proto.MCPTruncationSentinel(rsp)
	if want == "" {
		t.Fatal("proto.MCPTruncationSentinel returned empty on truncated rsp")
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok || tc.Text != want {
		t.Errorf("first content block = %q; want exact match with proto.MCPTruncationSentinel = %q", tc.Text, want)
	}
}
