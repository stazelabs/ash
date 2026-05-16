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
	res, err := toolResult("grep", rsp, "")
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
	// Post-ASH-156: json-mode success carries the truncation sentinel
	// as the sole TextContent block — the JSON body fallback is gone.
	if len(res.Content) != 1 {
		t.Fatalf("expected sentinel-only TextContent, got %d content blocks", len(res.Content))
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
	res, err := toolResult("grep", rsp, "")
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
	res, err := toolResult("grep", rsp, "")
	if err != nil {
		t.Fatalf("toolResult: %v", err)
	}
	if ash, ok := res.Meta["ash"].(map[string]any); ok {
		if _, ok := ash["truncated"]; ok {
			t.Errorf("_meta.ash.truncated set on a non-truncated response")
		}
	}
	// Post-ASH-156: json-mode success with no truncation carries zero
	// TextContent blocks — the JSON body lives in StructuredContent.
	if len(res.Content) != 0 {
		t.Errorf("Content count = %d; want 0 (json-mode success is single-emit StructuredContent)", len(res.Content))
	}
	if res.StructuredContent == nil {
		t.Errorf("StructuredContent must be populated for json-mode success")
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
	res, err := toolResult("grep", rsp, "")
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

// TestDecodeArgsStripsFormat exercises ASH-146: ashmcp must peel the
// MCP-only `format` knob off the args map before the request reaches
// the daemon. Verb-level ParseArgs would reject the unknown key under
// the additionalProperties:false guard added in ASH-116, so a leak
// would surface as a runtime regression that schema-level validation
// cannot catch.
func TestDecodeArgsStripsFormat(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		wantArgs   map[string]any
		wantFormat string
	}{
		{
			name:       "format=pretty stripped, opts into pretty",
			body:       `{"path":"README.md","format":"pretty"}`,
			wantArgs:   map[string]any{"path": "README.md"},
			wantFormat: "pretty",
		},
		{
			name:       "format=json stripped, stays json",
			body:       `{"path":"README.md","format":"json"}`,
			wantArgs:   map[string]any{"path": "README.md"},
			wantFormat: "json",
		},
		{
			name:       "no format key, defaults to json",
			body:       `{"path":"README.md"}`,
			wantArgs:   map[string]any{"path": "README.md"},
			wantFormat: "json",
		},
		{
			name:       "unknown format coerced to json",
			body:       `{"path":"README.md","format":"yaml"}`,
			wantArgs:   map[string]any{"path": "README.md"},
			wantFormat: "json",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args, format, err := decodeArgs(json.RawMessage(tc.body))
			if err != nil {
				t.Fatalf("decodeArgs: %v", err)
			}
			if format != tc.wantFormat {
				t.Errorf("format = %q; want %q", format, tc.wantFormat)
			}
			if _, leaked := args["format"]; leaked {
				t.Errorf("args still carry format key after decodeArgs: %+v", args)
			}
			if len(args) != len(tc.wantArgs) {
				t.Errorf("args = %+v; want %+v", args, tc.wantArgs)
			}
			for k, v := range tc.wantArgs {
				if args[k] != v {
					t.Errorf("args[%q] = %v; want %v", k, args[k], v)
				}
			}
		})
	}
}

// TestToolResultPrettyMode covers the ASH-146 pretty-emit shape: the
// daemon-pretty render rides as the sole TextContent, structuredContent
// is omitted (the JSON/pretty divergence would be a footgun), and the
// IsError flag still mirrors rsp.OK.
func TestToolResultPrettyMode(t *testing.T) {
	rsp := &proto.Response{
		V:  proto.ProtocolVersion,
		ID: 42,
		OK: true,
		Data: proto.MustData(map[string]any{
			"path":  "README.md",
			"size":  int64(123),
			"mtime": int64(0),
			"type":  "file",
		}),
	}
	resJSON, err := toolResult("stat", rsp, "")
	if err != nil {
		t.Fatalf("toolResult json: %v", err)
	}
	// Post-ASH-156: json-mode success carries no TextContent — the
	// verb's typed payload rides as StructuredContent only.
	if len(resJSON.Content) != 0 {
		t.Fatalf("json mode: Content count = %d, want 0 (single-emit StructuredContent)", len(resJSON.Content))
	}
	if resJSON.StructuredContent == nil {
		t.Error("json mode: StructuredContent must be populated (clients rely on outputSchema)")
	}

	resPretty, err := toolResult("stat", rsp, "pretty")
	if err != nil {
		t.Fatalf("toolResult pretty: %v", err)
	}
	if resPretty.StructuredContent != nil {
		t.Error("pretty mode: StructuredContent must be omitted (the JSON/pretty divergence makes it inconsistent)")
	}
	if len(resPretty.Content) != 1 {
		t.Fatalf("pretty mode: Content count = %d, want 1", len(resPretty.Content))
	}
	tcPretty := resPretty.Content[0].(*mcp.TextContent)
	if tcPretty.Text == "" {
		t.Errorf("pretty mode: empty TextContent — pretty renderer was not used")
	}
	// Pretty render must still be the daemon-pretty text, not the JSON
	// envelope. Recomputing the JSON form here keeps the assertion
	// honest without leaning on the (now-absent) json-mode TextContent.
	envBody, err := proto.MCPEnvelope(rsp)
	if err != nil {
		t.Fatalf("envelope: %v", err)
	}
	if tcPretty.Text == string(envBody) {
		t.Errorf("pretty mode text == JSON envelope; pretty renderer was not used")
	}
	if len(tcPretty.Text) >= len(envBody) {
		t.Errorf("pretty (%d bytes) not shorter than JSON envelope (%d bytes) — ASH-146 only buys a win if pretty is cheaper for stat", len(tcPretty.Text), len(envBody))
	}
}

// TestToolResultPrettyModeFallback guards the safety net: if a verb has
// no pretty renderer (unlikely today, but the map could ship missing an
// entry), pretty mode must not silently emit empty content. Falling
// back to the JSON envelope keeps the harness from receiving a black
// hole when the human hits a bug.
func TestToolResultPrettyModeFallback(t *testing.T) {
	rsp := &proto.Response{
		V:    proto.ProtocolVersion,
		ID:   1,
		OK:   true,
		Data: proto.MustData(map[string]any{"k": "v"}),
	}
	res, err := toolResult("nonexistent-verb", rsp, "pretty")
	if err != nil {
		t.Fatalf("toolResult: %v", err)
	}
	if len(res.Content) != 1 {
		t.Fatalf("fallback: Content count = %d, want 1", len(res.Content))
	}
	if tc, ok := res.Content[0].(*mcp.TextContent); !ok || tc.Text == "" {
		t.Errorf("fallback: expected non-empty TextContent, got %+v", res.Content[0])
	}
}

// TestToolResultPrettyModeError keeps error envelopes consistent across
// formats — failure shape is small enough that the JSON/pretty distinction
// would only add complexity. Both paths produce "<code>: <msg>".
func TestToolResultPrettyModeError(t *testing.T) {
	rsp := &proto.Response{
		V:   proto.ProtocolVersion,
		ID:  1,
		OK:  false,
		Err: &proto.Error{Code: "path_denied", Msg: "outside jail"},
	}
	for _, mode := range []string{"", "pretty"} {
		res, err := toolResult("read", rsp, mode)
		if err != nil {
			t.Fatalf("toolResult format=%q: %v", mode, err)
		}
		if !res.IsError {
			t.Errorf("format=%q: IsError must be true on error responses", mode)
		}
		tc := res.Content[0].(*mcp.TextContent)
		if tc.Text != "path_denied: outside jail" {
			t.Errorf("format=%q: error text = %q; want \"path_denied: outside jail\"", mode, tc.Text)
		}
	}
}
