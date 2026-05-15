// Package proto defines the wire envelope between ash clients and the ashd daemon.
//
// Frames are 4-byte big-endian length, followed by msgpack-encoded Request or
// Response. Versioning lives in the V field. Today only v1 exists; the field is
// the seam through which schema-dictionary optimization will arrive without
// breaking older clients.
package proto

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"

	"github.com/vmihailenco/msgpack/v5"
)

const (
	ProtocolVersion = 2
	MaxFrameSize    = 64 << 20 // 64 MiB hard cap on a single message

	// AshVersion is bumped manually per ship. Persisted into bench_runs
	// so historical bench rows can be attributed to a known surface.
	AshVersion = "0.1.0"
)

// Frame kind tags discriminate streaming response/control frames on the
// wire (ASH-106). Tags are written as a single byte immediately before the
// msgpack payload, INSIDE the length-prefixed frame. Legacy non-streaming
// traffic does not carry a kind byte at all — the daemon emits a plain
// Response frame iff Request.Stream is false (or absent), preserving v1
// byte-for-byte compatibility.
const (
	KindFinal  byte = 0x01 // daemon → client: final Response after streaming
	KindChunk  byte = 0x02 // daemon → client: intermediate batch
	KindCancel byte = 0x03 // client → daemon: cancel an in-flight streaming request
)

type Request struct {
	V    int            `msgpack:"v"    json:"v"`
	ID   uint64         `msgpack:"id"   json:"id"`
	Verb string         `msgpack:"verb" json:"verb"`
	Args map[string]any `msgpack:"args" json:"args"`
	// Argv is the literal client argv after the binary name (verb plus
	// every flag/value/positional the agent typed, before --format
	// stripping or stdin resolution). When present the daemon tokenizes
	// this as `tokens_in` so the meter reflects what the agent typed
	// rather than the post-parse canonical form. Optional for backward
	// compatibility: when absent the daemon falls back to PrettyRequest.
	Argv []string `msgpack:"argv,omitempty" json:"argv,omitempty"`
	// Stream opts the request into the streaming response shape (ASH-106).
	// When true, the daemon emits zero or more kind-tagged Chunk frames as
	// the verb produces intermediate results, then a kind-tagged Final
	// frame (the legacy Response shape). When false or absent, the daemon
	// returns a single legacy Response frame with no kind tag — v1 clients
	// continue to work unchanged.
	Stream bool `msgpack:"stream,omitempty" json:"stream,omitempty"`
}

// Chunk is one batch of intermediate results emitted during a streaming
// response. Data is the msgpack-encoded verb-specific chunk payload (e.g.
// []grep.Match, []find.Record, test.Package). Seq numbers chunks from 1
// in emission order — the client uses Seq for progress accounting and to
// detect dropped frames (none expected today; the wire is a UDS).
type Chunk struct {
	V    int                `msgpack:"v"    json:"v"`
	ID   uint64             `msgpack:"id"   json:"id"`
	Seq  uint32             `msgpack:"seq"  json:"seq"`
	Data msgpack.RawMessage `msgpack:"data" json:"data"`
}

// Cancel is a control frame sent from client to daemon to interrupt an
// in-flight streaming request. The daemon's per-request cancel watcher
// reads kind-tagged frames from the conn while the verb runs; receiving
// a Cancel with a matching ID triggers context cancellation, which the
// streaming verbs honor at their walker / event-loop checkpoints.
type Cancel struct {
	V  int    `msgpack:"v"  json:"v"`
	ID uint64 `msgpack:"id" json:"id"`
}

// Response carries the verb result back to the client. Data is
// msgpack.RawMessage rather than `any` so the verb's typed Result is
// encoded once on the daemon side and decoded straight into the typed
// struct on the client side via msgpack.Unmarshal — no per-verb
// hand-rolled `map[string]any` walker. Use MustData / UnmarshalData to
// move between typed values and RawMessage.
type Response struct {
	V       int                `msgpack:"v"                 json:"v"`
	ID      uint64             `msgpack:"id"                json:"id"`
	OK      bool               `msgpack:"ok"                json:"ok"`
	Data    msgpack.RawMessage `msgpack:"data,omitempty"    json:"data,omitempty"`
	Err     *Error             `msgpack:"err,omitempty"     json:"err,omitempty"`
	Metrics *Metrics           `msgpack:"metrics,omitempty" json:"metrics,omitempty"`
}

type Error struct {
	Code string `msgpack:"code"           json:"code"`
	Msg  string `msgpack:"msg"            json:"msg"`
	Hint string `msgpack:"hint,omitempty" json:"-"`
}

// TruncInfo carries structured truncation metadata in place of the prose
// truncation_hint string, saving 20-30 tokens per truncated call (ASH-76).
// Limit is the cap that triggered truncation; Max is the hard cap (if
// Limit==Max, the only recourse is narrowing — raising is not possible).
type TruncInfo struct {
	Trunc int `msgpack:"trunc"`
	Limit int `msgpack:"limit"`
	Max   int `msgpack:"max"`
}

type Metrics struct {
	LatencyParseUs     int64  `msgpack:"lp,omitempty" json:"lp,omitempty"`
	LatencyExecUs      int64  `msgpack:"le,omitempty" json:"le,omitempty"`
	LatencySerializeUs int64  `msgpack:"ls,omitempty" json:"ls,omitempty"`
	LatencyDispatchUs  int64  `msgpack:"ld,omitempty" json:"ld,omitempty"`
	TokensIn           int    `msgpack:"ti,omitempty" json:"ti,omitempty"`
	TokensOut          int    `msgpack:"to,omitempty" json:"to,omitempty"`
	TokensMethod       string `msgpack:"tm,omitempty" json:"tm,omitempty"`
	BytesIn            int    `msgpack:"bi,omitempty" json:"bi,omitempty"`
	BytesOut           int    `msgpack:"bo,omitempty" json:"bo,omitempty"`
	Truncated          bool   `msgpack:"tr,omitempty" json:"tr,omitempty"`
	// LedgerError is set by the daemon when persisting the call to the
	// instrumentation ledger failed. The verb itself may have succeeded; the
	// ledger row did not. Empty in the normal case (omitempty drops it from
	// the wire). This is the project's main "loud failure" signal: a quiet
	// ledger failure undermines every claim ash makes about itself.
	LedgerError string `msgpack:"ledger_error,omitempty" json:"ledger_error,omitempty"`
	// Phases is the sub-execution latency breakdown reported by the verb's
	// Tracer. Optional; a verb that doesn't instrument anything leaves it
	// nil and the field omits from the wire entirely.
	Phases *Phases `msgpack:"ph,omitempty" json:"ph,omitempty"`
}

// Phases breaks LatencyExecUs into named subsystems. Fields are
// microseconds. Phases overlap by design: WalkUs is the wall time spent
// inside walker.Walk (which itself contains the visitor's IO/regex), so
// IOUs and RegexUs are typically subsets of WalkUs. The point is to tell
// the agent "of the exec time, here's how much was each subsystem" not
// to provide a strict tree decomposition.
type Phases struct {
	WalkUs         int64 `msgpack:"w,omitempty"  json:"w,omitempty"`
	IOUs           int64 `msgpack:"io,omitempty" json:"io,omitempty"`
	RegexUs        int64 `msgpack:"r,omitempty"  json:"r,omitempty"`
	RegexCompileUs int64 `msgpack:"rc,omitempty" json:"rc,omitempty"`
}

// IsZero reports whether all phases are zero — used by the daemon to
// decide whether to attach a Phases pointer at all.
func (p Phases) IsZero() bool {
	return p.WalkUs == 0 && p.IOUs == 0 && p.RegexUs == 0 && p.RegexCompileUs == 0
}

func WriteFrame(w io.Writer, payload []byte) error {
	if len(payload) > MaxFrameSize {
		return fmt.Errorf("proto: frame size %d exceeds max %d", len(payload), MaxFrameSize)
	}
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(payload)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

func ReadFrame(r io.Reader) ([]byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n > MaxFrameSize {
		return nil, fmt.Errorf("proto: incoming frame size %d exceeds max %d", n, MaxFrameSize)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

// WriteKinded writes a kind-tagged frame: a single byte kind followed by
// the msgpack payload, wrapped in the standard 4-byte length prefix. Used
// for streaming traffic (ASH-106) — Chunk and Final frames from the
// daemon, and Cancel frames from the client. Legacy non-streaming frames
// continue to use WriteFrame with no kind byte.
func WriteKinded(w io.Writer, kind byte, payload []byte) error {
	if len(payload)+1 > MaxFrameSize {
		return fmt.Errorf("proto: kinded frame size %d exceeds max %d", len(payload)+1, MaxFrameSize)
	}
	buf := make([]byte, 0, 1+len(payload))
	buf = append(buf, kind)
	buf = append(buf, payload...)
	return WriteFrame(w, buf)
}

// ReadKinded reads one kind-tagged frame and returns its kind byte and
// payload (the bytes after the kind byte, ready to be msgpack-decoded into
// the matching envelope type). The caller is responsible for dispatching
// on kind.
func ReadKinded(r io.Reader) (byte, []byte, error) {
	buf, err := ReadFrame(r)
	if err != nil {
		return 0, nil, err
	}
	if len(buf) < 1 {
		return 0, nil, fmt.Errorf("proto: kinded frame is empty")
	}
	return buf[0], buf[1:], nil
}

func EncodeChunk(c *Chunk) ([]byte, error) { return msgpack.Marshal(c) }
func EncodeCancel(c *Cancel) ([]byte, error) { return msgpack.Marshal(c) }

func DecodeChunk(buf []byte) (*Chunk, error) {
	var c Chunk
	if err := msgpack.Unmarshal(buf, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

func DecodeCancel(buf []byte) (*Cancel, error) {
	var c Cancel
	if err := msgpack.Unmarshal(buf, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

func EncodeRequest(req *Request) ([]byte, error)   { return msgpack.Marshal(req) }
func EncodeResponse(rsp *Response) ([]byte, error) { return msgpack.Marshal(rsp) }

func DecodeRequest(buf []byte) (*Request, error) {
	var r Request
	if err := msgpack.Unmarshal(buf, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// DecodeResponse decodes a wire-frame Response. Data lands as
// msgpack.RawMessage; verbs decode it into their typed Result via
// UnmarshalData rather than re-walking a map[string]any.
func DecodeResponse(buf []byte) (*Response, error) {
	var r Response
	dec := msgpack.NewDecoder(bytes.NewReader(buf))
	if err := dec.Decode(&r); err != nil {
		return nil, err
	}
	return &r, nil
}

// MustData encodes a typed verb Result into the RawMessage shape that
// Response.Data expects. The daemon uses this after dispatching a verb;
// bench uses it for in-process dispatch; tests use it to construct
// Response values that mirror the wire shape. Panics on encode error,
// which only happens for unencodable types (channels, funcs, etc.) — a
// programming bug, not a runtime condition.
func MustData(v any) msgpack.RawMessage {
	if v == nil {
		return nil
	}
	b, err := msgpack.Marshal(v)
	if err != nil {
		panic(fmt.Errorf("proto.MustData: %w", err))
	}
	return msgpack.RawMessage(b)
}

// UnmarshalData decodes Response.Data into dst, which must be a non-nil
// pointer. Returns an error if Data is empty or the underlying msgpack
// decode fails. Verbs call this from PrettyResponse instead of the
// hand-rolled per-verb decodeResult walkers that this replaces.
func UnmarshalData(rsp *Response, dst any) error {
	if rsp == nil || len(rsp.Data) == 0 {
		return fmt.Errorf("proto: response has no data")
	}
	return msgpack.Unmarshal(rsp.Data, dst)
}
