// Stop-hook fast path for `ash hook --event stop`.
//
// Claude Code's Stop hook fires once per assistant turn after the model
// emits its final message. We scrape the just-written transcript JSONL
// for the most recent assistant entry, pull out the Anthropic `usage`
// block, and fire the `turn` verb against the local daemon so the
// cache hit/miss numbers land in the ledger.
//
// Like the PreToolUse fast path, everything is best-effort: parse
// errors, missing transcripts, mid-rotation files, and a down daemon
// all fall through to silent exit 0. The Stop hook must never break
// the harness.
//
// Background: ASH-188 / ASH-185 Option A. See docs/cache-telemetry.md
// for the design.
package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"github.com/stazelabs/ash/internal/proto"
	"github.com/stazelabs/ash/internal/session"
)

// stopTailBytes is how far back we read from the end of the transcript
// when scanning for the last assistant entry. Each turn's serialized
// assistant entry is usually tens of KB; 256 KiB covers typical sizes
// and keeps the read cheap. If we can't find an assistant entry in the
// tail, we soft-fail rather than escalate to a full file read.
const stopTailBytes = 256 * 1024

// stopHookPayload mirrors the Claude Code Stop hook input. Only the
// fields we consume are typed; extra fields are tolerated by
// json.Unmarshal.
type stopHookPayload struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	CWD            string `json:"cwd"`
	HookEventName  string `json:"hook_event_name"`
}

// transcriptAssistant is the subset of a Claude Code transcript JSONL
// assistant entry we consume. Field names match the on-disk schema;
// the structure has been stable across recent Claude Code releases
// but is not an Anthropic-supported contract — see
// docs/cache-telemetry.md §Mechanism A for the risk discussion.
type transcriptAssistant struct {
	Type      string `json:"type"`
	SessionID string `json:"sessionId"`
	Timestamp string `json:"timestamp"`
	Message   struct {
		ID    string `json:"id"`
		Model string `json:"model"`
		Usage struct {
			InputTokens              int `json:"input_tokens"`
			OutputTokens             int `json:"output_tokens"`
			CacheReadInputTokens     int `json:"cache_read_input_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

// runHookStop is the entry point for `ash hook --event stop`. It reads
// the Stop payload from stdin, locates the last assistant entry in the
// transcript, and fires a `turn` request to the daemon. All failures
// are silently swallowed.
func runHookStop() {
	runHookStopFromReader(os.Stdin)
}

func runHookStopFromReader(r io.Reader) {
	payloadBytes, err := io.ReadAll(r)
	if err != nil || len(payloadBytes) == 0 {
		return
	}
	var p stopHookPayload
	if err := json.Unmarshal(payloadBytes, &p); err != nil {
		return
	}
	if p.TranscriptPath == "" {
		return
	}
	entry, ok := lastAssistantEntry(p.TranscriptPath)
	if !ok || entry.Message.ID == "" {
		return
	}
	// Resolve the project root the daemon owns. The Stop hook's `cwd`
	// is whichever directory Claude Code was running in; we prefer it
	// over os.Getwd() because the hook process inherits cwd from the
	// harness but session.Root may differ.
	root := resolveStopRoot(p.CWD)
	if root == "" {
		return
	}
	args := map[string]any{
		"turn_id":               entry.Message.ID,
		"harness_session_id":    entry.SessionID,
		"model":                 entry.Message.Model,
		"input_tokens":          int64(entry.Message.Usage.InputTokens),
		"output_tokens":         int64(entry.Message.Usage.OutputTokens),
		"cache_read_tokens":     int64(entry.Message.Usage.CacheReadInputTokens),
		"cache_creation_tokens": int64(entry.Message.Usage.CacheCreationInputTokens),
	}
	if ns := parseTranscriptTimestamp(entry.Timestamp); ns > 0 {
		args["timestamp_nanos"] = ns
	}
	fireTurn(root, args)
}

func resolveStopRoot(cwd string) string {
	candidates := []string{cwd}
	if w, err := os.Getwd(); err == nil {
		candidates = append(candidates, w)
	}
	for _, c := range candidates {
		if c == "" {
			continue
		}
		if r, err := session.Root(c); err == nil {
			return r
		}
	}
	return ""
}

// lastAssistantEntry tails up to stopTailBytes from the transcript and
// returns the most recent line whose `type` is `assistant`. Returns
// (zero, false) if no such line exists in the tail window.
func lastAssistantEntry(path string) (transcriptAssistant, bool) {
	var zero transcriptAssistant
	f, err := os.Open(path)
	if err != nil {
		return zero, false
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return zero, false
	}
	size := stat.Size()
	if size == 0 {
		return zero, false
	}
	var offset int64
	if size > stopTailBytes {
		offset = size - stopTailBytes
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return zero, false
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return zero, false
	}
	// If we seeked into the middle of a line, drop the leading partial
	// line so we don't try to parse half a JSON object.
	if offset > 0 {
		if nl := bytes.IndexByte(data, '\n'); nl >= 0 {
			data = data[nl+1:]
		} else {
			return zero, false
		}
	}
	return findLastAssistant(data)
}

// findLastAssistant scans data backward (newline-delimited) and returns
// the first line that decodes as an assistant entry with a populated
// message.id. Exposed for tests.
func findLastAssistant(data []byte) (transcriptAssistant, bool) {
	var zero transcriptAssistant
	// Drop trailing newline so the split doesn't yield an empty tail.
	data = bytes.TrimRight(data, "\n")
	// Walk lines back-to-front. Bufio default token cap is 64 KiB which
	// would reject long tool-call results, so we slice manually.
	for end := len(data); end > 0; {
		start := bytes.LastIndexByte(data[:end], '\n') + 1
		line := data[start:end]
		end = start - 1
		if len(line) == 0 {
			continue
		}
		// Cheap reject: only the assistant type is interesting.
		if !bytes.Contains(line, []byte(`"type":"assistant"`)) {
			continue
		}
		var entry transcriptAssistant
		if err := json.Unmarshal(line, &entry); err != nil {
			continue
		}
		if entry.Type == "assistant" && entry.Message.ID != "" {
			return entry, true
		}
	}
	return zero, false
}

// parseTranscriptTimestamp parses Claude Code's ISO-8601 timestamps
// (e.g. "2026-05-18T04:05:59.253Z") to unix nanos. Returns 0 on any
// parse failure, in which case the verb falls back to time.Now().
func parseTranscriptTimestamp(s string) int64 {
	if s == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return 0
	}
	return t.UnixNano()
}

// fireTurn dials the daemon and sends a fire-and-forget `turn` request.
// Reuses the same soft-fail discipline as fireHookLedger: pre-stat the
// socket so a down daemon costs ~1µs, cap the write deadline so a hung
// daemon can't stall the hook.
func fireTurn(root string, args map[string]any) {
	sock := session.SocketPath(root)
	if _, err := os.Stat(sock); err != nil {
		return
	}
	conn, err := net.DialTimeout("unix", sock, hookDaemonDialTimeout)
	if err != nil {
		return
	}
	defer conn.Close()
	_ = conn.SetWriteDeadline(time.Now().Add(20 * time.Millisecond))
	req := &proto.Request{
		V:    proto.ProtocolVersion,
		ID:   newID(),
		Verb: "turn",
		Args: args,
	}
	encoded, err := proto.EncodeRequest(req)
	if err != nil {
		return
	}
	_ = proto.WriteFrame(conn, encoded)
}

// stopArgvHasEvent reports whether argv (positional args after `hook`)
// contains `--event stop` (in either `--event stop` or `--event=stop`
// form). Argv parsing here is intentionally minimal — the Stop hook's
// command line is fixed by us in .claude/settings.json.
func stopArgvHasEvent(argv []string) bool {
	for i, a := range argv {
		if a == "--event" && i+1 < len(argv) && argv[i+1] == "stop" {
			return true
		}
		if strings.HasPrefix(a, "--event=") && a == "--event=stop" {
			return true
		}
	}
	return false
}
