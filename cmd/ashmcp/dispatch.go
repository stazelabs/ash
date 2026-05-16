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
	"github.com/stazelabs/ash/internal/mcpschema"
	"github.com/stazelabs/ash/internal/proto"
	"github.com/stazelabs/ash/internal/session"
	"github.com/stazelabs/ash/internal/verbs"
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
		args, format, err := decodeArgs(req.Params.Arguments)
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
			rsp, err = streamingRoundtrip(ctx, conn, req.Session, token, verb, args, format)
		} else {
			rsp, err = roundtrip(conn, verb, args, format)
		}
		if err != nil {
			return nil, fmt.Errorf("ashd roundtrip: %w", err)
		}
		return toolResult(verb, rsp, format)
	}
}

// decodeArgs unmarshals the JSON args object and peels off the MCP-only
// `format` knob (ASH-146) so it does not reach the daemon's verb-level
// arg parser. Empty / missing / `json` are equivalent — the JSON envelope
// is the default emit shape. `pretty` opts into daemon-pretty rendering.
// Unknown values are silently coerced to `json` so a typo can't break a
// session; MCP schema validation already rejects them client-side.
func decodeArgs(raw json.RawMessage) (map[string]any, string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return map[string]any{}, mcpschema.FormatJSON, nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, "", err
	}
	if m == nil {
		m = map[string]any{}
	}
	format := mcpschema.FormatJSON
	if v, ok := m[mcpschema.FormatArg]; ok {
		if s, ok := v.(string); ok && s == mcpschema.FormatPretty {
			format = mcpschema.FormatPretty
		}
		delete(m, mcpschema.FormatArg)
	}
	return m, format, nil
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
//
// emitFormat is set on the wire as Request.EmitFormat (ASH-146) so the
// daemon's tokens_out_emit accounting tokenizes the right rendering —
// pretty when the harness opted in via the `format` tool arg, JSON
// envelope (legacy / default) otherwise.
func roundtrip(conn net.Conn, verb string, args map[string]any, emitFormat string) (*proto.Response, error) {
	req := &proto.Request{
		V:          proto.ProtocolVersion,
		ID:         newID(),
		Verb:       verb,
		Args:       args,
		Transport:  proto.TransportMCP,
		EmitFormat: wireEmitFormat(emitFormat),
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
func streamingRoundtrip(ctx context.Context, conn net.Conn, ss *mcp.ServerSession, progressToken any, verb string, args map[string]any, emitFormat string) (*proto.Response, error) {
	req := &proto.Request{
		V:          proto.ProtocolVersion,
		ID:         newID(),
		Verb:       verb,
		Args:       args,
		Stream:     true,
		Transport:  proto.TransportMCP,
		EmitFormat: wireEmitFormat(emitFormat),
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
// proto.Error payload as TextContent — the harness sees "the tool ran
// and reported an error", not "the transport blew up".
//
// On success in JSON-emit mode (ASH-124) the response is dual-emitted:
// the decoded data rides as both StructuredContent (for harnesses that
// validate against the tool's outputSchema and skip the parse) and a
// TextContent fallback carrying the same JSON bytes (for harnesses that
// ignore structuredContent). The bytes in both places are byte-identical
// to what proto.MCPEnvelope produces, so ashd's tokens_out_emit accounting
// models exactly what the harness consumes.
//
// On success in pretty-emit mode (ASH-146) the structured fallback is
// dropped: TextContent carries the daemon-pretty render — the same text
// the ash CLI emits — so the harness pays CLI-equivalent token cost.
// StructuredContent is omitted; harnesses that need programmatic access
// to fields must use the default JSON mode.
//
// Metrics move out of the body into MCP _meta — it's protocol-reserved
// metadata, not part of the tool's output contract, so harnesses don't
// pay for it in tokens.
//
// When the verb truncated its output (ASH-127), the structured
// TruncInfo rides alongside metrics in _meta.ash.truncated so harnesses
// can detect "this response is partial" programmatically without
// parsing the envelope. A short sentinel TextContent is also prepended
// so harnesses that ignore _meta still see the signal in the model-
// visible content. IsError stays false — truncation is a partial
// success, not a failure.
func toolResult(verb string, rsp *proto.Response, emitFormat string) (*mcp.CallToolResult, error) {
	body, err := mcpBody(verb, rsp, emitFormat)
	if err != nil {
		return nil, fmt.Errorf("marshal tool result: %w", err)
	}
	out := &mcp.CallToolResult{
		IsError: !rsp.OK,
		Content: []mcp.Content{&mcp.TextContent{Text: body}},
	}
	var trunc *proto.TruncInfo
	if rsp.OK {
		// StructuredContent is paired with the JSON envelope: it shares
		// the same field shape as the TextContent in JSON mode. In
		// pretty mode the TextContent diverges from the structured
		// payload, so we drop StructuredContent rather than serve two
		// inconsistent views; harnesses that need structured access can
		// re-call with format=json.
		if emitFormat != mcpschema.FormatPretty {
			data, derr := proto.MCPStructuredData(rsp)
			if derr == nil {
				// MCP requires StructuredContent to marshal to a JSON
				// object. Every verb Result type in ash decodes to a
				// top-level map; non-object payloads are dropped from
				// structured emission and the TextContent fallback
				// continues to carry them.
				if m, ok := data.(map[string]any); ok {
					out.StructuredContent = m
				}
			}
		}
		trunc = proto.MCPTruncationHint(rsp)
	}
	ashMeta := map[string]any{}
	if rsp.Metrics != nil {
		ashMeta["metrics"] = rsp.Metrics
	}
	if trunc != nil {
		ashMeta["truncated"] = trunc
		// Prepend the sentinel so the truncation signal is the first
		// thing the harness renders. The text comes from proto so
		// ashd's tokens_out_emit accounting (ASH-123) sees the same
		// bytes the harness consumes.
		sentinel := proto.MCPTruncationSentinel(rsp)
		out.Content = append(
			[]mcp.Content{&mcp.TextContent{Text: sentinel}},
			out.Content...,
		)
	}
	if len(ashMeta) > 0 {
		out.Meta = mcp.Meta{"ash": ashMeta}
	}
	return out, nil
}

// mcpBody returns the model-visible text for one tool call. JSON mode
// (default) builds the proto.MCPEnvelope used since ASH-124. Pretty mode
// (ASH-146) renders the daemon-pretty form a CLI client would print, so
// the harness pays the same token cost it would have paid had it shelled
// out to ash directly. On error responses both modes return the legacy
// "<code>: <msg>" prose envelope — the failure shape is small enough
// that the JSON/pretty distinction doesn't matter.
func mcpBody(verb string, rsp *proto.Response, emitFormat string) (string, error) {
	if rsp != nil && !rsp.OK {
		b, err := proto.MCPEnvelope(rsp)
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
	if emitFormat == mcpschema.FormatPretty {
		if p, ok := prettyHandlers[verb]; ok {
			// PrettyHandlers expect the rendering Request that
			// produced the response so they can quote the request
			// path in headers. ashmcp does not carry the original
			// Request all the way to toolResult, so synthesize a
			// minimal one — verb is the only field every pretty
			// renderer consults today (path is read off the
			// response's typed Data, not the Request).
			return p(&proto.Request{Verb: verb}, rsp), nil
		}
		// Verb has no pretty handler registered — fall through to the
		// JSON envelope so we never silently emit empty content.
	}
	b, err := proto.MCPEnvelope(rsp)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// prettyHandlers is built once at process start; ashmcp uses these to
// render daemon-pretty output for format=pretty calls (ASH-146).
var prettyHandlers = verbs.PrettyHandlers()

// wireEmitFormat maps the local format value to the proto.Request wire
// representation. Empty / json both serialize as empty (the omitempty tag
// keeps two-call cache prefixes stable for the common case); pretty rides
// as "pretty" so the daemon can route emit accounting through the pretty
// renderer instead of the JSON envelope.
func wireEmitFormat(format string) string {
	if format == mcpschema.FormatPretty {
		return mcpschema.FormatPretty
	}
	return ""
}

func newID() uint64 {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return uint64(time.Now().UnixNano())
	}
	return binary.BigEndian.Uint64(b[:])
}
