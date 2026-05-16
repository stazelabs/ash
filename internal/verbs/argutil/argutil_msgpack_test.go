package argutil

import (
	"testing"

	"github.com/stazelabs/ash/internal/proto"
)

// TestToInt_MsgpackRoundTrip is the load-bearing regression for
// ASH-149: feed a proto.Request with Go-native int args through the
// msgpack encode/decode pair (the actual wire path), then verify the
// Layer-2 validators accept the decoded values.
//
// msgpack-go normalizes integers on encode based on width — small
// positive ints (0–127) round-trip as uint8, slightly larger as uint16
// / uint32 — and the daemon sees those types when Args is
// map[string]any. ASH-148 worked around this in wirecmp by passing
// strings; the real fix lives here in argutil.
func TestToInt_MsgpackRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want int
	}{
		// Native Go integer literals on either side of msgpack's
		// width-specific encoding boundaries.
		{"int_20", int(20), 20},
		{"int8_20", int8(20), 20},
		{"int16_300", int16(300), 300},
		{"int32_70000", int32(70000), 70000},
		{"int64_5e9", int64(5_000_000_000), 5_000_000_000},
		{"uint_20", uint(20), 20},
		{"uint8_20", uint8(20), 20},
		{"uint16_300", uint16(300), 300},
		{"uint32_70000", uint32(70000), 70000},
		{"uint64_5e9", uint64(5_000_000_000), 5_000_000_000},
		{"float32_20", float32(20), 20},
		{"float64_20", float64(20), 20},
		{"string_20", "20", 20},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := &proto.Request{
				V:    proto.ProtocolVersion,
				ID:   1,
				Verb: "find",
				Args: map[string]any{"limit": c.in},
			}
			buf, err := proto.EncodeRequest(req)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			got, err := proto.DecodeRequest(buf)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			n, perr := OptionalPosInt(got.Args, "limit", 0, 0)
			if perr != nil {
				t.Fatalf("OptionalPosInt rejected decoded %T(%v): %+v",
					got.Args["limit"], got.Args["limit"], perr)
			}
			if n != c.want {
				t.Errorf("OptionalPosInt=%d, want %d (decoded type %T)",
					n, c.want, got.Args["limit"])
			}
			// OptionalNonNegInt walks the same coercer; make sure it
			// agrees on the round-tripped value.
			if n2, perr := OptionalNonNegInt(got.Args, "limit", 0, 0); perr != nil || n2 != c.want {
				t.Errorf("OptionalNonNegInt=%d perr=%+v, want %d",
					n2, perr, c.want)
			}
		})
	}
}

// TestToInt64_MsgpackRoundTrip mirrors the above for the 64-bit coercer.
// Both ToInt and ToInt64 are on every verb's hot path; pinning both
// keeps a future refactor from silently dropping a case arm.
func TestToInt64_MsgpackRoundTrip(t *testing.T) {
	cases := []struct {
		in   any
		want int64
	}{
		{int(7), 7},
		{int8(7), 7},
		{int16(7), 7},
		{int32(7), 7},
		{int64(7), 7},
		{uint(7), 7},
		{uint8(7), 7},
		{uint16(7), 7},
		{uint32(7), 7},
		{uint64(7), 7},
		{float32(7), 7},
		{float64(7), 7},
		{"7", 7},
	}
	for _, c := range cases {
		req := &proto.Request{
			V: proto.ProtocolVersion, ID: 1, Verb: "find",
			Args: map[string]any{"n": c.in},
		}
		buf, err := proto.EncodeRequest(req)
		if err != nil {
			t.Fatalf("encode %T: %v", c.in, err)
		}
		got, err := proto.DecodeRequest(buf)
		if err != nil {
			t.Fatalf("decode %T: %v", c.in, err)
		}
		n, ok := ToInt64(got.Args["n"])
		if !ok || n != c.want {
			t.Errorf("ToInt64(%T)=%d ok=%v, want %d true (decoded type %T)",
				c.in, n, ok, c.want, got.Args["n"])
		}
	}
}
