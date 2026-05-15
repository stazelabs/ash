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
