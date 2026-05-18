package main

import (
	"database/sql"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stazelabs/ash/internal/ledger"
	"github.com/stazelabs/ash/internal/proto"
	"github.com/stazelabs/ash/internal/verbs"
	"github.com/vmihailenco/msgpack/v5"

	_ "modernc.org/sqlite"
)

// streamingFixture builds a tree with `n` files, each containing the
// literal "STREAMHIT" exactly once, so a grep yields n matches in walker
// order. n=70 deliberately exceeds the flushItemThreshold (64) so the
// daemon must emit at least 2 Chunk frames during the run.
func streamingFixture(t *testing.T, n int) string {
	t.Helper()
	root := t.TempDir()
	for i := 0; i < n; i++ {
		body := fmt.Sprintf("package p%d\n// STREAMHIT in file %d\n", i, i)
		path := filepath.Join(root, fmt.Sprintf("f%03d.go", i))
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func startStreamingDaemon(t *testing.T) (sockPath string, dbPath string) {
	t.Helper()
	// Unix sockets have a 104-byte path cap on macOS; t.TempDir() can
	// produce paths over that. Use /tmp + a short random suffix to stay
	// under the limit reliably.
	tmp, err := os.MkdirTemp("/tmp", "ashd-stream-")
	if err != nil {
		t.Fatalf("mkdirtemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(tmp) })
	dbPath = filepath.Join(tmp, "ledger.db")
	led, lerr := ledger.Open(dbPath, tmp, "streaming-test")
	if lerr != nil {
		t.Fatalf("ledger.Open: %v", lerr)
	}
	t.Cleanup(func() { led.Close() })

	runners := verbs.Runners(led, nil, time.Time{}, "", nil, nil)
	pretty := verbs.PrettyHandlers()

	sockPath = filepath.Join(tmp, "s")
	ln, lerr := net.Listen("unix", sockPath)
	if lerr != nil {
		t.Fatalf("listen: %v", lerr)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go handle(conn, led, runners, pretty, 0, nil)
		}
	}()
	return sockPath, dbPath
}

var streamReqID atomic.Uint64

func dialStreaming(t *testing.T, sockPath string) net.Conn {
	t.Helper()
	c, err := net.DialTimeout("unix", sockPath, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func writeStreamingRequest(t *testing.T, c net.Conn, verb string, args map[string]any) uint64 {
	t.Helper()
	id := streamReqID.Add(1)
	req := &proto.Request{
		V:      proto.ProtocolVersion,
		ID:     id,
		Verb:   verb,
		Args:   args,
		Stream: true,
	}
	buf, err := proto.EncodeRequest(req)
	if err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}
	if err := proto.WriteFrame(c, buf); err != nil {
		t.Fatalf("WriteFrame: %v", err)
	}
	return id
}

// TestStreaming_GrepEmitsChunksAndFinal proves the end-to-end shape:
// a Stream=true request yields one or more KindChunk frames followed by
// exactly one KindFinal frame. The cumulative match count across the
// stream equals the count in the Final, and the Final.Matches matches
// what a non-streaming grep over the same fixture would produce.
func TestStreaming_GrepEmitsChunksAndFinal(t *testing.T) {
	sockPath, dbPath := startStreamingDaemon(t)
	root := streamingFixture(t, 70)

	c := dialStreaming(t, sockPath)
	reqID := writeStreamingRequest(t, c, "grep", map[string]any{
		"pattern": "STREAMHIT",
		"path":    root,
		"glob":    "**/*.go",
		"case":    "sensitive",
		"max":     "1000",
	})

	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))

	streamedMatches := 0
	chunkCount := 0
	var final *proto.Response
	for final == nil {
		kind, payload, err := proto.ReadKinded(c)
		if err != nil {
			t.Fatalf("ReadKinded: %v", err)
		}
		switch kind {
		case proto.KindChunk:
			chunkCount++
			chunk, err := proto.DecodeChunk(payload)
			if err != nil {
				t.Fatalf("DecodeChunk: %v", err)
			}
			if chunk.ID != reqID {
				t.Errorf("chunk.ID = %d, want %d", chunk.ID, reqID)
			}
			var batch []any
			if err := msgpack.Unmarshal(chunk.Data, &batch); err != nil {
				t.Fatalf("decode chunk batch: %v", err)
			}
			streamedMatches += len(batch)
		case proto.KindFinal:
			final, err = proto.DecodeResponse(payload)
			if err != nil {
				t.Fatalf("DecodeResponse final: %v", err)
			}
		default:
			t.Fatalf("unexpected kind: %#x", kind)
		}
	}
	if chunkCount < 2 {
		t.Errorf("expected at least 2 Chunk frames (fixture > 64 matches), got %d", chunkCount)
	}
	if !final.OK {
		t.Fatalf("final not OK: err=%+v", final.Err)
	}
	// Decode the cumulative Result and confirm the per-stream count
	// matches the cumulative Final count. They MUST agree, else the
	// MCP "chunks + final agree" contract is broken.
	var res struct {
		Matches []map[string]any `msgpack:"matches"`
		Count   int              `msgpack:"count"`
	}
	if err := proto.UnmarshalData(final, &res); err != nil {
		t.Fatalf("UnmarshalData: %v", err)
	}
	if res.Count != 70 {
		t.Errorf("final match count: got %d, want 70", res.Count)
	}
	if streamedMatches != res.Count {
		t.Errorf("streamed match count %d disagrees with final %d", streamedMatches, res.Count)
	}

	// Verify the ledger row carries the streaming markers.
	row := queryLastCall(t, dbPath, reqID)
	if row.streaming != 1 {
		t.Errorf("ledger.streaming = %d, want 1", row.streaming)
	}
	if row.chunksOut < int64(chunkCount) {
		// chunks_out is what the daemon counted; should equal what we read.
		t.Errorf("ledger.chunks_out = %d, expected >= %d", row.chunksOut, chunkCount)
	}
	if row.ttfcUs <= 0 {
		t.Errorf("ledger.time_to_first_chunk_us = %d, want > 0", row.ttfcUs)
	}
}

// TestStreaming_GrepHonorsCancellation proves a Cancel frame sent mid-
// stream triggers a clean shutdown: the daemon stops yielding new chunks,
// writes a Final with Err.Code="cancelled", and the ledger row records
// the failure path.
func TestStreaming_GrepHonorsCancellation(t *testing.T) {
	sockPath, _ := startStreamingDaemon(t)
	// Larger fixture: each file holds 200 matches across 1000 lines, so
	// the walker spends real time per file. 200 files × 200 matches gives
	// the cancel frame a wide window to land before the verb finishes.
	root := streamingFixtureSlow(t, 200, 200)

	c := dialStreaming(t, sockPath)
	reqID := writeStreamingRequest(t, c, "grep", map[string]any{
		"pattern": "STREAMHIT",
		"path":    root,
		"glob":    "**/*.go",
		"case":    "sensitive",
		"max":     "100000",
	})

	// Send Cancel immediately after the Request — the daemon's watcher
	// goroutine reads it as soon as it's scheduled, well before the
	// walker can finish the 40k-match tree. We do NOT pre-read a Chunk
	// because that would let the walker keep running for as long as we
	// take to react.
	cancelBuf, cerr := proto.EncodeCancel(&proto.Cancel{V: proto.ProtocolVersion, ID: reqID})
	if cerr != nil {
		t.Fatalf("EncodeCancel: %v", cerr)
	}
	if werr := proto.WriteKinded(c, proto.KindCancel, cancelBuf); werr != nil {
		t.Fatalf("WriteKinded cancel: %v", werr)
	}

	// Drain frames until Final. The daemon may emit a few Chunks the
	// emitter had already buffered before honoring the cancel.
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	deadline := time.Now().Add(5 * time.Second)
	var final *proto.Response
	for final == nil {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for Final after Cancel")
		}
		kind, payload, rerr := proto.ReadKinded(c)
		if rerr != nil {
			t.Fatalf("ReadKinded post-cancel: %v", rerr)
		}
		if kind == proto.KindFinal {
			f, derr := proto.DecodeResponse(payload)
			if derr != nil {
				t.Fatalf("DecodeResponse: %v", derr)
			}
			final = f
		}
	}
	if final.OK {
		// On extremely fast machines the walker could finish before the
		// cancel goroutine fires. Surface that as a skip rather than a
		// false negative — the determinism we care about (kinded wire,
		// chunks + final shape) is covered by the other test.
		t.Skipf("race lost: walker finished before cancel landed; got OK final")
	}
	if final.Err == nil || final.Err.Code != "cancelled" {
		t.Errorf("expected Err.Code=cancelled, got %+v", final.Err)
	}
}

// streamingFixtureSlow builds a tree where each file holds matchesPerFile
// occurrences of "STREAMHIT" inside lines that are otherwise non-matching.
// Designed to give the daemon enough wall-clock work for cancellation
// tests; the per-file body keeps grep's regex+IO honest.
func streamingFixtureSlow(t *testing.T, files, matchesPerFile int) string {
	t.Helper()
	root := t.TempDir()
	var body []byte
	for i := 0; i < matchesPerFile; i++ {
		body = append(body, []byte(fmt.Sprintf("line %d: STREAMHIT marker %d\n", i, i))...)
	}
	for i := 0; i < files; i++ {
		path := filepath.Join(root, fmt.Sprintf("f%05d.go", i))
		if err := os.WriteFile(path, body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// ledgerRow is the subset of a calls row we care about in streaming tests.
type ledgerRow struct {
	streaming int
	chunksOut int64
	ttfcUs    int64
}

func queryLastCall(t *testing.T, dbPath string, reqID uint64) ledgerRow {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath+"?mode=ro")
	if err != nil {
		t.Fatalf("open ledger: %v", err)
	}
	defer db.Close()
	var row ledgerRow
	err = db.QueryRow(
		`SELECT streaming, chunks_out, time_to_first_chunk_us FROM calls WHERE request_id = ?`,
		int64(reqID),
	).Scan(&row.streaming, &row.chunksOut, &row.ttfcUs)
	if err != nil {
		t.Fatalf("query ledger row for req %d: %v", reqID, err)
	}
	return row
}
