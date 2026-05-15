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
	"github.com/vmihailenco/msgpack/v5"

	"github.com/stazelabs/ash/internal/jail"
	"github.com/stazelabs/ash/internal/proto"
	"github.com/stazelabs/ash/internal/session"
)

// streamingVerbs is the set of MCP-exposed verbs that opt into the ASH-106
// streaming response shape when the MCP client supplies a progressToken.
// Adding a verb here is sufficient to wire it up — daemon-side support
// follows from the same Request.Stream flag.
//
// Note: 'test' is NOT in v1 readSideVerbs so this entry is presently
// dormant. It's listed here so when test joins the MCP surface (deferred
// per ASH-104) streaming comes online with no extra wiring.
var streamingVerbs = map[string]bool{
	"grep": true,
	"find": true,
	"test": true,
}

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

		var rsp *proto.Response
		token := req.Params.GetProgressToken()
		if streamingVerbs[verb] && token != nil {
			rsp, err = streamingRoundtrip(ctx, conn, req.Session, token, verb, args)
		} else {
			rsp, err = roundtrip(conn, verb, args)
		}
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
		V:         proto.ProtocolVersion,
		ID:        newID(),
		Verb:      verb,
		Args:      args,
		Transport: proto.TransportMCP,
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

// streamingRoundtrip writes a Stream=true Request, reads kind-tagged
// Chunk and Final frames from the daemon, and forwards each Chunk to the
// MCP client as a progressNotification (ASH-106). The cumulative Final
// frame is returned to the caller untouched, so the tool result shape is
// identical to the non-streaming path — clients that ignore progress
// notifications see today's behavior, clients that subscribe see the
// streaming preview.
//
// ctx cancellation (the agent abandoning the tool call) propagates by
// closing the conn; the daemon's per-request watcher reads EOF, cancels
// its own ctx, and the streaming verb aborts at its next checkpoint.
func streamingRoundtrip(ctx context.Context, conn net.Conn, ss *mcp.ServerSession, progressToken any, verb string, args map[string]any) (*proto.Response, error) {
	req := &proto.Request{
		V:         proto.ProtocolVersion,
		ID:        newID(),
		Verb:      verb,
		Args:      args,
		Stream:    true,
		Transport: proto.TransportMCP,
	}
	encoded, err := proto.EncodeRequest(req)
	if err != nil {
		return nil, fmt.Errorf("encode: %w", err)
	}
	if err := proto.WriteFrame(conn, encoded); err != nil {
		return nil, fmt.Errorf("write: %w", err)
	}

	// Close the conn on ctx cancel so an in-flight ReadKinded returns
	// promptly and the daemon's watcher sees EOF == cancel.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-done:
		}
	}()

	totalBytes := 0
	for {
		kind, payload, rerr := proto.ReadKinded(conn)
		if rerr != nil {
			return nil, fmt.Errorf("read: %w", rerr)
		}
		totalBytes += 1 + len(payload)
		switch kind {
		case proto.KindChunk:
			chunk, cerr := proto.DecodeChunk(payload)
			if cerr != nil {
				return nil, fmt.Errorf("decode chunk: %w", cerr)
			}
			// Decode the verb-typed batch back into a generic []any so we
			// can JSON-encode it for the progress message. ashmcp is verb-
			// agnostic at this layer; the harness sees the same record
			// shape as in the final result.
			var batch []any
			if uerr := msgpack.Unmarshal(chunk.Data, &batch); uerr != nil {
				return nil, fmt.Errorf("decode chunk data: %w", uerr)
			}
			msg, jerr := json.Marshal(batch)
			if jerr != nil {
				return nil, fmt.Errorf("marshal chunk: %w", jerr)
			}
			// Best-effort: NotifyProgress can fail if the MCP transport
			// is broken, but the daemon is still producing real work, so
			// we keep draining the stream.
			_ = ss.NotifyProgress(ctx, &mcp.ProgressNotificationParams{
				ProgressToken: progressToken,
				Progress:      float64(chunk.Seq),
				Message:       string(msg),
			})
		case proto.KindFinal:
			rsp, derr := proto.DecodeResponse(payload)
			if derr != nil {
				return nil, fmt.Errorf("decode final: %w", derr)
			}
			if rsp.Metrics != nil {
				rsp.Metrics.BytesOut = totalBytes
			}
			return rsp, nil
		default:
			return nil, fmt.Errorf("unexpected frame kind %#x", kind)
		}
	}
}

// toolResult shapes the ashd response as an MCP tool result. Verb-level
// failures (rsp.OK == false) become IsError=true tool results carrying the
// proto.Error payload as JSON, not protocol errors — the harness should
// see them as "the tool ran and reported an error", not "the transport
// blew up". The success path emits the decoded Data as one TextContent
// block of JSON. The Metrics envelope rides along on both paths so the
// agent can see token / latency cost without a separate report call.
func toolResult(rsp *proto.Response) (*mcp.CallToolResult, error) {
	// Envelope shape lives in proto.MCPEnvelope so the daemon can
	// compute it identically for ledger accounting (ASH-123). Changes
	// to the wire shape — adding _meta, StructuredContent, etc. —
	// happen there, not here.
	out, err := proto.MCPEnvelope(rsp)
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
