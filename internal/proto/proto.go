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
	V    int            `msgpack:"v"`
	ID   uint64         `msgpack:"id"`
	Verb string         `msgpack:"verb"`
	Args map[string]any `msgpack:"args"`
}

type Response struct {
	V       int      `msgpack:"v"`
	ID      uint64   `msgpack:"id"`
	OK      bool     `msgpack:"ok"`
	Data    any      `msgpack:"data,omitempty"`
	Err     *Error   `msgpack:"err,omitempty"`
	Metrics *Metrics `msgpack:"metrics,omitempty"`
}

type Error struct {
	Code string `msgpack:"code"`
	Msg  string `msgpack:"msg"`
}

type Metrics struct {
	LatencyParseUs     int64  `msgpack:"latency_parse_us"`
	LatencyExecUs      int64  `msgpack:"latency_exec_us"`
	LatencySerializeUs int64  `msgpack:"latency_serialize_us"`
	TokensIn           int    `msgpack:"tokens_in"`
	TokensOut          int    `msgpack:"tokens_out"`
	TokensMethod       string `msgpack:"tokens_method"`
	BytesIn            int    `msgpack:"bytes_in"`
	BytesOut           int    `msgpack:"bytes_out"`
	Truncated          bool   `msgpack:"truncated,omitempty"`
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
