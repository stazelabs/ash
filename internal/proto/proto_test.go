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
	type sample struct {
		Content string `msgpack:"content"`
		Size    int64  `msgpack:"size"`
	}
	want := &Response{
		V:    ProtocolVersion,
		ID:   7,
		OK:   true,
		Data: MustData(sample{Content: "hello world", Size: 11}),
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
	var decoded sample
	if err := UnmarshalData(got, &decoded); err != nil {
		t.Fatalf("UnmarshalData: %v", err)
	}
	if decoded.Content != "hello world" || decoded.Size != 11 {
		t.Errorf("decoded data wrong: %+v", decoded)
	}
}

func TestResponseRoundTrip_LedgerError(t *testing.T) {
	// The "loud failure" path: the verb succeeded but the ledger row didn't
	// land. The wire response carries that fact so the client can scream.
	want := &Response{
		V:    ProtocolVersion,
		ID:   42,
		OK:   true,
		Data: MustData(map[string]string{"content": "x"}),
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

func TestMetricsOmitempty(t *testing.T) {
	// Zero-value numeric fields and empty TokensMethod must be absent from
	// the wire — the daemon omits TokensMethod when using the default tokenizer.
	want := &Response{
		V:       ProtocolVersion,
		ID:      2,
		OK:      true,
		Metrics: &Metrics{TokensOut: 42},
	}
	encoded, err := EncodeResponse(want)
	if err != nil {
		t.Fatal(err)
	}
	for _, absent := range []string{"tm", "ti", "bi", "bo", "lp", "ls"} {
		if bytes.Contains(encoded, []byte(absent)) {
			t.Errorf("field %q should be omitted when zero/empty; encoded: %x", absent, encoded)
		}
	}
	// Non-zero field must survive round-trip.
	got, err := DecodeResponse(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if got.Metrics == nil || got.Metrics.TokensOut != 42 {
		t.Errorf("tokens_out not preserved: %+v", got.Metrics)
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

func TestPrettyRequestArgv(t *testing.T) {
	cases := []struct {
		name string
		argv []string
		want string
	}{
		{"empty", nil, "ash"},
		{"verb only", []string{"help"}, "ash help"},
		{"flag form", []string{"read", "--path", "foo.go"}, "ash read --path foo.go"},
		{"positional", []string{"read", "foo.go"}, "ash read foo.go"},
		{"two positionals", []string{"grep", "TODO", "."}, "ash grep TODO ."},
		{"format flag preserved", []string{"read", "--format", "json", "f.go"}, "ash read --format json f.go"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := PrettyRequestArgv(tc.argv)
			if got != tc.want {
				t.Errorf("PrettyRequestArgv(%q) = %q, want %q", tc.argv, got, tc.want)
			}
		})
	}
}

func TestRequest_ArgvRoundTrip(t *testing.T) {
	// Argv must survive msgpack encode/decode so the daemon can tokenize
	// the literal client argv.
	in := &Request{
		V:    ProtocolVersion,
		ID:   42,
		Verb: "grep",
		Args: map[string]any{"pattern": "TODO", "path": "."},
		Argv: []string{"grep", "TODO", "."},
	}
	buf, err := EncodeRequest(in)
	if err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}
	out, err := DecodeRequest(buf)
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}
	if len(out.Argv) != 3 {
		t.Fatalf("Argv length: got %d, want 3 (%v)", len(out.Argv), out.Argv)
	}
	for i, want := range in.Argv {
		if out.Argv[i] != want {
			t.Errorf("Argv[%d] = %q, want %q", i, out.Argv[i], want)
		}
	}
}

func TestRequest_ArgvOmittedWhenEmpty(t *testing.T) {
	// Backward compat: clients that don't ship Argv must still produce
	// a well-formed request the daemon can decode.
	in := &Request{V: ProtocolVersion, ID: 1, Verb: "help", Args: map[string]any{}}
	buf, err := EncodeRequest(in)
	if err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}
	out, err := DecodeRequest(buf)
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}
	if len(out.Argv) != 0 {
		t.Errorf("expected empty Argv on decode of legacy request, got %v", out.Argv)
	}
}

func TestRequest_StreamOmittedWhenFalse(t *testing.T) {
	// Backward compat (ASH-106): v1 clients did not have Stream. The
	// omitempty tag must keep stream=false off the wire so v1 daemons
	// decoding a v2 request still parse it as today's plain request.
	in := &Request{V: ProtocolVersion, ID: 1, Verb: "grep", Args: map[string]any{}}
	buf, err := EncodeRequest(in)
	if err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}
	if bytes.Contains(buf, []byte("stream")) {
		t.Errorf("stream key must be omitted when false; encoded: %x", buf)
	}
	out, err := DecodeRequest(buf)
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}
	if out.Stream {
		t.Errorf("expected Stream=false on decode, got true")
	}
}

func TestRequest_StreamRoundTrip(t *testing.T) {
	in := &Request{V: ProtocolVersion, ID: 7, Verb: "grep", Args: map[string]any{"pattern": "TODO"}, Stream: true}
	buf, err := EncodeRequest(in)
	if err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}
	out, err := DecodeRequest(buf)
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}
	if !out.Stream {
		t.Errorf("Stream=true did not survive round-trip")
	}
}

func TestKindedFrameRoundTrip(t *testing.T) {
	// WriteKinded / ReadKinded must preserve the kind byte AND the
	// payload bytes exactly. Used for streaming Chunk and control Cancel
	// frames (ASH-106).
	cases := []struct {
		kind    byte
		payload []byte
	}{
		{KindFinal, []byte("final")},
		{KindChunk, []byte("a batch of matches")},
		{KindCancel, []byte{}},
		{KindChunk, bytes.Repeat([]byte{0x42}, 4096)},
	}
	for _, tc := range cases {
		var buf bytes.Buffer
		if err := WriteKinded(&buf, tc.kind, tc.payload); err != nil {
			t.Fatalf("WriteKinded(kind=%#x, %dB): %v", tc.kind, len(tc.payload), err)
		}
		kind, body, err := ReadKinded(&buf)
		if err != nil {
			t.Fatalf("ReadKinded(kind=%#x): %v", tc.kind, err)
		}
		if kind != tc.kind {
			t.Errorf("kind: got %#x want %#x", kind, tc.kind)
		}
		if !bytes.Equal(body, tc.payload) {
			t.Errorf("payload mismatch for kind=%#x (%dB vs %dB)", tc.kind, len(body), len(tc.payload))
		}
	}
}

func TestChunkRoundTrip(t *testing.T) {
	// Chunk carries verb-typed batches as RawMessage so the verb's
	// concrete chunk type (e.g. []grep.Match) round-trips without an
	// any-walk. Mirror the Response/MustData pattern.
	type item struct {
		Path string `msgpack:"p"`
		Line int    `msgpack:"l"`
	}
	batch := []item{{"foo.go", 10}, {"bar.go", 42}}
	in := &Chunk{V: ProtocolVersion, ID: 100, Seq: 1, Data: MustData(batch)}
	buf, err := EncodeChunk(in)
	if err != nil {
		t.Fatalf("EncodeChunk: %v", err)
	}
	out, err := DecodeChunk(buf)
	if err != nil {
		t.Fatalf("DecodeChunk: %v", err)
	}
	if out.ID != in.ID || out.Seq != in.Seq {
		t.Errorf("envelope mismatch: got %+v want %+v", out, in)
	}
	var got []item
	if err := UnmarshalData(&Response{Data: out.Data}, &got); err != nil {
		t.Fatalf("UnmarshalData: %v", err)
	}
	if len(got) != 2 || got[0].Path != "foo.go" || got[1].Line != 42 {
		t.Errorf("data decoded wrong: %+v", got)
	}
}

func TestCancelRoundTrip(t *testing.T) {
	in := &Cancel{V: ProtocolVersion, ID: 999}
	buf, err := EncodeCancel(in)
	if err != nil {
		t.Fatalf("EncodeCancel: %v", err)
	}
	out, err := DecodeCancel(buf)
	if err != nil {
		t.Fatalf("DecodeCancel: %v", err)
	}
	if out.V != in.V || out.ID != in.ID {
		t.Errorf("got %+v want %+v", out, in)
	}
}

func TestReadKindedEmptyFrame(t *testing.T) {
	// A zero-length frame (no kind byte) is malformed; the reader must
	// return an error rather than dereference an empty slice.
	var buf bytes.Buffer
	if err := WriteFrame(&buf, nil); err != nil {
		t.Fatalf("WriteFrame: %v", err)
	}
	_, _, err := ReadKinded(&buf)
	if err == nil {
		t.Fatal("expected error on empty kinded frame, got nil")
	}
}
