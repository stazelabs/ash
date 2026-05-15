package proto

import (
	"encoding/json"
	"fmt"
)

// TransportMCP is the value `ashmcp` sets on Request.Transport so the
// daemon knows to compute the MCP-envelope tokens_out_emit accounting
// alongside the pretty-rendered tokens_out (ASH-123).
const TransportMCP = "mcp"

// MCPEnvelope builds the JSON envelope that `ashmcp` ships back to the
// MCP harness as TextContent.text. Both `ashmcp` (to actually emit) and
// `ashd` (to tokenize the bytes the harness will consume) call this so
// the wire shape lives in exactly one place.
//
// Shape: {"ok": bool, "err"?: Error, "metrics"?: Metrics, "data"?: ...}
// rsp.Data (which is msgpack.RawMessage on the daemon side) is decoded
// into a generic value before re-encoding so the result is valid JSON
// rather than a base64-wrapped binary blob.
//
// Returns the JSON bytes ready for TextContent.text. The envelope is
// stable across both compute sites by construction: changing the shape
// here changes both ashmcp emission and ashd accounting in lockstep.
func MCPEnvelope(rsp *Response) ([]byte, error) {
	if rsp == nil {
		return nil, fmt.Errorf("proto.MCPEnvelope: nil response")
	}
	body := map[string]any{
		"ok": rsp.OK,
	}
	if rsp.Err != nil {
		body["err"] = rsp.Err
	}
	if rsp.Metrics != nil {
		body["metrics"] = rsp.Metrics
	}
	if len(rsp.Data) > 0 {
		var data any
		if err := UnmarshalData(rsp, &data); err != nil {
			return nil, fmt.Errorf("proto.MCPEnvelope: decode data: %w", err)
		}
		body["data"] = data
	}
	out, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("proto.MCPEnvelope: marshal: %w", err)
	}
	return out, nil
}
