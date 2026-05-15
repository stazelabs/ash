package proto

import (
	"encoding/json"
	"fmt"
)

// TransportMCP is the value `ashmcp` sets on Request.Transport so the
// daemon knows to compute the MCP-envelope tokens_out_emit accounting
// alongside the pretty-rendered tokens_out (ASH-123).
const TransportMCP = "mcp"

// MCPEnvelope builds the JSON bytes that represent the model-visible
// portion of one ashmcp tool call result (ASH-124). Both `ashmcp` (to
// shape the CallToolResult) and `ashd` (to tokenize what the harness
// will consume) call this so the wire shape lives in exactly one place.
//
// Shape on success: rsp.Data decoded into its native JSON form — no
// {ok, err, metrics, data} wrapper. The metrics envelope rides in MCP
// `_meta` (protocol-reserved metadata), and `ok`/`err` collapse into
// the CallToolResult.IsError flag. ashmcp re-emits these bytes as the
// TextContent fallback for harnesses that don't consume
// structuredContent, so the byte count here is exactly what those
// harnesses tokenize.
//
// Shape on failure: "<err.code>: <err.msg>" — the same short prose a
// harness shows when surfacing a failed tool call. No JSON wrapper.
//
// Returns the bytes ready for both ashmcp emission and ashd
// tokenization. Stable across both compute sites by construction.
func MCPEnvelope(rsp *Response) ([]byte, error) {
	if rsp == nil {
		return nil, fmt.Errorf("proto.MCPEnvelope: nil response")
	}
	if !rsp.OK {
		if rsp.Err != nil {
			return []byte(rsp.Err.Code + ": " + rsp.Err.Msg), nil
		}
		return []byte("error"), nil
	}
	if len(rsp.Data) == 0 {
		return []byte("{}"), nil
	}
	var data any
	if err := UnmarshalData(rsp, &data); err != nil {
		return nil, fmt.Errorf("proto.MCPEnvelope: decode data: %w", err)
	}
	out, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("proto.MCPEnvelope: marshal: %w", err)
	}
	return out, nil
}

// MCPStructuredData decodes rsp.Data into a generic Go value (map or
// scalar) suitable for use as CallToolResult.StructuredContent. Returns
// nil when rsp.Data is empty or the response is an error envelope; the
// caller should fall back to error-shaped emission in that case.
func MCPStructuredData(rsp *Response) (any, error) {
	if rsp == nil || !rsp.OK || len(rsp.Data) == 0 {
		return nil, nil
	}
	var data any
	if err := UnmarshalData(rsp, &data); err != nil {
		return nil, fmt.Errorf("proto.MCPStructuredData: decode data: %w", err)
	}
	return data, nil
}

// MCPTruncationHint extracts the structured truncation hint from a
// verb's Result without depending on the verb-specific Result type.
// Every truncating verb encodes the hint under msgpack tag
// "truncation_hint"; a partial decode picks it up. Returns nil when the
// response is empty, errored, or the verb didn't truncate. Lives in
// proto so ashmcp (emit) and ashd (tokens_out_emit accounting) agree
// on whether the sentinel is present (ASH-127).
func MCPTruncationHint(rsp *Response) *TruncInfo {
	if rsp == nil || !rsp.OK || len(rsp.Data) == 0 {
		return nil
	}
	var probe struct {
		Hint *TruncInfo `msgpack:"truncation_hint"`
	}
	if err := UnmarshalData(rsp, &probe); err != nil {
		return nil
	}
	return probe.Hint
}

// MCPTruncationSentinel renders the short, verb-agnostic truncation
// hint that ashmcp prepends to the CallToolResult content blocks when a
// response carries a truncation hint (ASH-127). Returns "" when the
// response was not truncated, so ashd can add `len(sentinel)` /
// `tokens(sentinel)` to its MCP-envelope accounting unconditionally
// and keep the ledger's tokens_out_emit honest with what the harness
// actually consumes.
//
// Limit==Max means the verb hit its hard cap — raising the limit
// cannot help, only narrowing the call will. The two phrasings keep
// agents from retrying with --max=higher on a call that already
// saturated the cap.
func MCPTruncationSentinel(rsp *Response) string {
	t := MCPTruncationHint(rsp)
	if t == nil {
		return ""
	}
	if t.Limit >= t.Max {
		return fmt.Sprintf("truncated: hit hard cap (max=%d) — narrow the call; raising the limit will not help", t.Max)
	}
	return fmt.Sprintf("truncated: hit limit=%d (max=%d) — narrow the call or raise the verb's limit flag", t.Limit, t.Max)
}
