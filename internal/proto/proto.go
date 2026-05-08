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
	ProtocolVersion = 1
	MaxFrameSize    = 64 << 20 // 64 MiB hard cap on a single message
)

type Request struct {
	V    int            `msgpack:"v"    json:"v"`
	ID   uint64         `msgpack:"id"   json:"id"`
	Verb string         `msgpack:"verb" json:"verb"`
	Args map[string]any `msgpack:"args" json:"args"`
}

type Response struct {
	V       int      `msgpack:"v"                json:"v"`
	ID      uint64   `msgpack:"id"               json:"id"`
	OK      bool     `msgpack:"ok"               json:"ok"`
	Data    any      `msgpack:"data,omitempty"    json:"data,omitempty"`
	Err     *Error   `msgpack:"err,omitempty"     json:"err,omitempty"`
	Metrics *Metrics `msgpack:"metrics,omitempty" json:"metrics,omitempty"`
}

type Error struct {
	Code string `msgpack:"code" json:"code"`
	Msg  string `msgpack:"msg"  json:"msg"`
}

type Metrics struct {
	LatencyParseUs     int64  `msgpack:"latency_parse_us"                json:"latency_parse_us"`
	LatencyExecUs      int64  `msgpack:"latency_exec_us"                 json:"latency_exec_us"`
	LatencySerializeUs int64  `msgpack:"latency_serialize_us"            json:"latency_serialize_us"`
	LatencyDispatchUs  int64  `msgpack:"latency_dispatch_us,omitempty"   json:"latency_dispatch_us,omitempty"`
	TokensIn           int    `msgpack:"tokens_in"            json:"tokens_in"`
	TokensOut          int    `msgpack:"tokens_out"           json:"tokens_out"`
	TokensMethod       string `msgpack:"tokens_method"        json:"tokens_method"`
	BytesIn            int    `msgpack:"bytes_in"             json:"bytes_in"`
	BytesOut           int    `msgpack:"bytes_out"            json:"bytes_out"`
	Truncated          bool   `msgpack:"truncated,omitempty"  json:"truncated,omitempty"`
	// LedgerError is set by the daemon when persisting the call to the
	// instrumentation ledger failed. The verb itself may have succeeded; the
	// ledger row did not. Empty in the normal case (omitempty drops it from
	// the wire). This is the project's main "loud failure" signal: a quiet
	// ledger failure undermines every claim ash makes about itself.
	LedgerError string `msgpack:"ledger_error,omitempty" json:"ledger_error,omitempty"`
	// Phases is the sub-execution latency breakdown reported by the verb's
	// Tracer. Optional; a verb that doesn't instrument anything leaves it
	// nil and the field omits from the wire entirely.
	Phases *Phases `msgpack:"phases,omitempty" json:"phases,omitempty"`
}

// Phases breaks LatencyExecUs into named subsystems. Fields are
// microseconds. Phases overlap by design: WalkUs is the wall time spent
// inside walker.Walk (which itself contains the visitor's IO/regex), so
// IOUs and RegexUs are typically subsets of WalkUs. The point is to tell
// the agent "of the exec time, here's how much was each subsystem" not
// to provide a strict tree decomposition.
type Phases struct {
	WalkUs         int64 `msgpack:"walk_us,omitempty"          json:"walk_us,omitempty"`
	IOUs           int64 `msgpack:"io_us,omitempty"            json:"io_us,omitempty"`
	RegexUs        int64 `msgpack:"regex_us,omitempty"         json:"regex_us,omitempty"`
	RegexCompileUs int64 `msgpack:"regex_compile_us,omitempty" json:"regex_compile_us,omitempty"`
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

func EncodeRequest(req *Request) ([]byte, error)   { return msgpack.Marshal(req) }
func EncodeResponse(rsp *Response) ([]byte, error) { return msgpack.Marshal(rsp) }

func DecodeRequest(buf []byte) (*Request, error) {
	var r Request
	if err := msgpack.Unmarshal(buf, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

func DecodeResponse(buf []byte) (*Response, error) {
	var r Response
	dec := msgpack.NewDecoder(bytes.NewReader(buf))
	dec.UseLooseInterfaceDecoding(true)
	if err := dec.Decode(&r); err != nil {
		return nil, err
	}
	return &r, nil
}
