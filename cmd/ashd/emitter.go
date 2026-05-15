package main

import (
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/stazelabs/ash/internal/proto"
	"github.com/vmihailenco/msgpack/v5"
)

// frameEmitter buffers chunks from a streaming verb and flushes them as
// kind-tagged Chunk frames to the conn. It implements proto.Emitter, so
// it plugs into the daemon's tracer at the start of a streaming request
// and the verb is unaware of the conn underneath.
//
// Flushes happen on three triggers:
//
//   - buffer reaches flushItemThreshold items (a "full" batch),
//   - flushIntervalMax has elapsed since the last flush (a "stale" batch),
//   - the daemon calls Flush explicitly at end-of-stream.
//
// Writes to the conn are serialized by writeMu, which is shared with the
// daemon handler so the trailing Final frame cannot interleave with an
// in-flight Chunk flush. The first flush records firstFlushAt, which the
// daemon reads for the ledger's time_to_first_chunk_us column. After a
// write error, every subsequent Emit/Flush is a no-op — the streaming
// transport is broken and the verb's ctx cancellation will take it from
// there.
type frameEmitter struct {
	conn    net.Conn
	writeMu *sync.Mutex
	reqID   uint64
	start   time.Time // request decode time; t0 for time_to_first_chunk_us

	mu       sync.Mutex // protects buffer + sequence
	buffer   []any
	seq      uint32
	lastFlush time.Time

	firstFlushAt atomic.Int64 // unix nanos; 0 == no flush yet
	chunkCount   atomic.Int64
	broken       atomic.Bool
}

// flushItemThreshold and flushIntervalMax are the batching knobs. 64 +
// 50ms is a measured-by-eye starting point — small enough that even a
// fast walker emits batches every few ms, large enough that grep over a
// dense tree doesn't write a frame per match. ASH-106 leaves these as
// consts; revisit after the bench fixture lands.
const (
	flushItemThreshold = 64
	flushIntervalMax   = 50 * time.Millisecond
)

// newFrameEmitter binds an emitter to a conn for one streaming request.
// writeMu is shared with the daemon handler so the Final frame cannot
// interleave with a Chunk flush. start is the request decode time, used
// as t0 for the time_to_first_chunk_us metric.
func newFrameEmitter(conn net.Conn, writeMu *sync.Mutex, reqID uint64, start time.Time) *frameEmitter {
	return &frameEmitter{
		conn:      conn,
		writeMu:   writeMu,
		reqID:     reqID,
		start:     start,
		lastFlush: start,
	}
}

// Emit adds one chunk item to the buffer and flushes if the item or time
// threshold has been crossed. Safe for concurrent callers; today's verbs
// emit from one goroutine but the contract is worry-free.
func (e *frameEmitter) Emit(chunk any) error {
	if e.broken.Load() {
		return nil
	}
	e.mu.Lock()
	e.buffer = append(e.buffer, chunk)
	full := len(e.buffer) >= flushItemThreshold
	stale := time.Since(e.lastFlush) >= flushIntervalMax
	e.mu.Unlock()
	if full || stale {
		return e.Flush()
	}
	return nil
}

// Flush forces the buffered items out as one Chunk frame. Called by the
// daemon at end-of-stream to drain any trailing items before Final.
// No-op on an empty buffer.
func (e *frameEmitter) Flush() error {
	if e.broken.Load() {
		return nil
	}
	e.mu.Lock()
	if len(e.buffer) == 0 {
		e.mu.Unlock()
		return nil
	}
	batch := e.buffer
	e.buffer = nil
	e.seq++
	seq := e.seq
	e.lastFlush = time.Now()
	e.mu.Unlock()

	data, err := msgpack.Marshal(batch)
	if err != nil {
		e.broken.Store(true)
		return err
	}
	chunk := &proto.Chunk{
		V:    proto.ProtocolVersion,
		ID:   e.reqID,
		Seq:  seq,
		Data: data,
	}
	encoded, err := proto.EncodeChunk(chunk)
	if err != nil {
		e.broken.Store(true)
		return err
	}
	e.writeMu.Lock()
	defer e.writeMu.Unlock()
	if err := proto.WriteKinded(e.conn, proto.KindChunk, encoded); err != nil {
		e.broken.Store(true)
		return err
	}
	if e.firstFlushAt.Load() == 0 {
		e.firstFlushAt.Store(time.Now().UnixNano())
	}
	e.chunkCount.Add(1)
	return nil
}

// FirstChunkLatency reports the wall time between start and the first
// successful Chunk flush, or zero if nothing was flushed. The daemon
// reads this for the ledger's time_to_first_chunk_us column.
func (e *frameEmitter) FirstChunkLatency() time.Duration {
	first := e.firstFlushAt.Load()
	if first == 0 {
		return 0
	}
	return time.Duration(first - e.start.UnixNano())
}

// ChunkCount reports the total number of Chunk frames written. Read by
// the daemon for the ledger's chunks_out column.
func (e *frameEmitter) ChunkCount() int { return int(e.chunkCount.Load()) }
