package proto

import (
	"bytes"
	"strings"
	"testing"
)

func TestFrameRoundTrip(t *testing.T) {
	cases := [][]byte{
		[]byte(""),
		[]byte("hi"),
		bytes.Repeat([]byte{0xAB}, 1<<10),
		bytes.Repeat([]byte{0xCD}, 1<<20),
	}
	for _, payload := range cases {
		var buf bytes.Buffer
		if err := WriteFrame(&buf, payload); err != nil {
			t.Fatalf("WriteFrame(%dB): %v", len(payload), err)
		}
		got, err := ReadFrame(&buf)
		if err != nil {
			t.Fatalf("ReadFrame(%dB): %v", len(payload), err)
		}
		if !bytes.Equal(got, payload) {
			t.Errorf("payload mismatch (%dB)", len(payload))
		}
	}
}

func TestFrameTooLargeRejectedOnWrite(t *testing.T) {
	var buf bytes.Buffer
	huge := make([]byte, MaxFrameSize+1)
	if err := WriteFrame(&buf, huge); err == nil {
		t.Fatal("expected error for oversized frame, got nil")
	}
}

func TestRequestRoundTrip(t *testing.T) {
	want := &Request{
		V:    ProtocolVersion,
		ID:   0xCAFEBABEDEADBEEF,
		Verb: "read",
		Args: map[string]any{
			"path":  "src/foo.go",
			"range": "10:50",
		},
	}
	encoded, err := EncodeRequest(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeRequest(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if got.V != want.V || got.ID != want.ID || got.Verb != want.Verb {
		t.Errorf("envelope mismatch: got=%+v want=%+v", got, want)
	}
	if got.Args["path"] != "src/foo.go" {
		t.Errorf("args.path: got %v want src/foo.go", got.Args["path"])
	}
	if got.Args["range"] != "10:50" {
		t.Errorf("args.range: got %v want 10:50", got.Args["range"])
	}
}

func TestResponseRoundTrip_OK(t *testing.T) {
	want := &Response{
		V:  ProtocolVersion,
		ID: 7,
		OK: true,
		Data: map[string]any{
			"content": "hello world",
			"size":    int64(11),
		},
		Metrics: &Metrics{
			LatencyParseUs:     12,
			LatencyExecUs:      34,
			LatencySerializeUs: 5,
			TokensIn:           1,
			TokensOut:          2,
			TokensMethod:       "real:cl100k_base",
			BytesIn:            42,
			BytesOut:           99,
			Truncated:          false,
		},
	}
	encoded, err := EncodeResponse(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeResponse(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if got.V != want.V || got.ID != want.ID || !got.OK {
		t.Errorf("envelope mismatch: got=%+v", got)
	}
	if got.Metrics == nil {
		t.Fatal("metrics missing after decode")
	}
	if got.Metrics.TokensOut != 2 || got.Metrics.TokensMethod != "real:cl100k_base" {
		t.Errorf("metrics: got=%+v", got.Metrics)
	}
	dm, ok := got.Data.(map[string]any)
	if !ok {
		t.Fatalf("data: expected map[string]any, got %T", got.Data)
	}
	if dm["content"] != "hello world" {
		t.Errorf("data.content: got %v", dm["content"])
	}
}

func TestResponseRoundTrip_LedgerError(t *testing.T) {
	// The "loud failure" path: the verb succeeded but the ledger row didn't
	// land. The wire response carries that fact so the client can scream.
	want := &Response{
		V:    ProtocolVersion,
		ID:   42,
		OK:   true,
		Data: map[string]any{"content": "x"},
		Metrics: &Metrics{
			LatencyParseUs:     1,
			LatencyExecUs:      1,
			LatencySerializeUs: 1,
			TokensIn:           1,
			TokensOut:          1,
			TokensMethod:       "real:cl100k_base",
			BytesIn:            1,
			BytesOut:           1,
			LedgerError:        "sql: database is closed",
		},
	}
	encoded, err := EncodeResponse(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeResponse(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if got.Metrics == nil || got.Metrics.LedgerError != "sql: database is closed" {
		t.Errorf("ledger_error did not survive round-trip: %+v", got.Metrics)
	}
}

func TestResponseRoundTrip_NoLedgerErrorByDefault(t *testing.T) {
	// omitempty: in the normal case, the field must not bloat every response.
	want := &Response{
		V:       ProtocolVersion,
		ID:      1,
		OK:      true,
		Metrics: &Metrics{TokensOut: 100, TokensMethod: "real:cl100k_base"},
	}
	encoded, err := EncodeResponse(want)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("ledger_error")) {
		t.Errorf("ledger_error key should be omitted when empty; encoded: %x", encoded)
	}
}

func TestResponseRoundTrip_Err(t *testing.T) {
	want := &Response{
		V:   ProtocolVersion,
		ID:  9,
		OK:  false,
		Err: &Error{Code: "not_found", Msg: "foo: no such file"},
	}
	encoded, err := EncodeResponse(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeResponse(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if got.OK || got.Err == nil {
		t.Fatalf("expected ok=false with err set, got %+v", got)
	}
	if got.Err.Code != "not_found" || got.Err.Msg != "foo: no such file" {
		t.Errorf("err mismatch: %+v", got.Err)
	}
}

func TestPrettyRequest_Deterministic(t *testing.T) {
	// Map iteration is randomized; PrettyRequest must sort keys so the
	// daemon and client produce identical bytes for token measurement.
	r1 := &Request{Verb: "read", Args: map[string]any{"path": "x", "range": "1:2", "limit_bytes": 100}}
	r2 := &Request{Verb: "read", Args: map[string]any{"limit_bytes": 100, "path": "x", "range": "1:2"}}
	a, b := PrettyRequest(r1), PrettyRequest(r2)
	if a != b {
		t.Errorf("non-deterministic pretty render:\n a=%q\n b=%q", a, b)
	}
	if !strings.HasPrefix(a, "ash read ") {
		t.Errorf("expected ash read prefix, got %q", a)
	}
}
