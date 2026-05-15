package main

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/stazelabs/ash/internal/jail"
	"github.com/stazelabs/ash/internal/proto"
	"github.com/stazelabs/ash/internal/session"
)

// dialDeadline caps the time a single tool call may spend establishing a
// daemon connection (including auto-start). The daemon itself enforces
// per-verb timeouts on top of this.
const dialDeadline = 5 * time.Second

// makeHandler returns an mcp.ToolHandler that proxies one MCP tool call
// to ashd over the per-project UDS. The handler resolves the project root
// from the current working directory on every call so the binary works
// correctly when the harness invokes it from inside a project tree, and
// uses dial-or-start to recover transparently if the daemon has crashed
// or been stopped between calls.
func makeHandler(verb string) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args, err := decodeArgs(req.Params.Arguments)
		if err != nil {
			return nil, fmt.Errorf("decode arguments: %w", err)
		}

		root, err := resolveRoot()
		if err != nil {
			return nil, fmt.Errorf("project root: %w", err)
		}
		sock := session.SocketPath(root)
		jail.SetPolicy(jail.FromConfig(false, root, nil, nil))

		dialCtx, cancel := context.WithTimeout(ctx, dialDeadline)
		defer cancel()
		conn, err := dialOrStart(dialCtx, root, sock)
		if err != nil {
			return nil, fmt.Errorf("dial ashd: %w", err)
		}
		defer conn.Close()

		rsp, err := roundtrip(conn, verb, args)
		if err != nil {
			return nil, fmt.Errorf("ashd roundtrip: %w", err)
		}
		return toolResult(rsp)
	}
}

func decodeArgs(raw json.RawMessage) (map[string]any, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return map[string]any{}, nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	if m == nil {
		m = map[string]any{}
	}
	return m, nil
}

func resolveRoot() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return session.Root(cwd)
}

// roundtrip encodes the request, writes a single frame, reads the
// response frame, and decodes it. Connection is single-use, matching the
// pattern in cmd/ash.
func roundtrip(conn net.Conn, verb string, args map[string]any) (*proto.Response, error) {
	req := &proto.Request{
		V:    proto.ProtocolVersion,
		ID:   newID(),
		Verb: verb,
		Args: args,
	}
	encoded, err := proto.EncodeRequest(req)
	if err != nil {
		return nil, fmt.Errorf("encode: %w", err)
	}
	if err := proto.WriteFrame(conn, encoded); err != nil {
		return nil, fmt.Errorf("write: %w", err)
	}
	buf, err := proto.ReadFrame(conn)
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}
	rsp, err := proto.DecodeResponse(buf)
	if err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if rsp.Metrics != nil {
		rsp.Metrics.BytesOut = len(buf)
	}
	return rsp, nil
}

// toolResult shapes the ashd response as an MCP tool result. Verb-level
// failures (rsp.OK == false) become IsError=true tool results carrying the
// proto.Error payload as JSON, not protocol errors — the harness should
// see them as "the tool ran and reported an error", not "the transport
// blew up". The success path emits the decoded Data as one TextContent
// block of JSON. The Metrics envelope rides along on both paths so the
// agent can see token / latency cost without a separate report call.
func toolResult(rsp *proto.Response) (*mcp.CallToolResult, error) {
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
		if err := proto.UnmarshalData(rsp, &data); err != nil {
			return nil, fmt.Errorf("decode response data: %w", err)
		}
		body["data"] = data
	}
	out, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal tool result: %w", err)
	}
	return &mcp.CallToolResult{
		IsError: !rsp.OK,
		Content: []mcp.Content{&mcp.TextContent{Text: string(out)}},
	}, nil
}

func newID() uint64 {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return uint64(time.Now().UnixNano())
	}
	return binary.BigEndian.Uint64(b[:])
}
