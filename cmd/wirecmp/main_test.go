package main

import (
	"testing"

	"github.com/stazelabs/ash/internal/proto"
	"github.com/stazelabs/ash/internal/verbs/find"
	"github.com/stazelabs/ash/internal/verbs/grep"
)

// TestFixtures_ParseArgsAfterWireRoundTrip is the regression for
// ASH-148 (worked around with strings) and ASH-149 (fixed in argutil).
// Before ASH-149 the find/grep fixtures rode strings through
// EncodeRequest because msgpack decoded their `limit: 20` /
// `max: 20` into uint8 and `argutil.ToInt` did not accept uint8 —
// every measured wirecmp row for those fixtures was actually the
// `args: limit must be a positive integer` error envelope, not real
// verb output.
//
// We don't need a live daemon to catch this: the failure mode is in
// the encode → decode → ParseArgs path, which is what this test
// exercises. If a future change re-narrows argutil.ToInt, this fails
// loudly instead of corrupting the next round of wire-cost numbers.
func TestFixtures_ParseArgsAfterWireRoundTrip(t *testing.T) {
	for _, f := range fixtures {
		f := f
		if f.Verb != "find" && f.Verb != "grep" {
			continue
		}
		t.Run(f.Name, func(t *testing.T) {
			req := &proto.Request{
				V: proto.ProtocolVersion, ID: 1,
				Verb: f.Verb, Args: f.Args,
			}
			buf, err := proto.EncodeRequest(req)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			got, err := proto.DecodeRequest(buf)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			switch f.Verb {
			case "find":
				if _, perr := find.ParseArgs(got.Args); perr != nil {
					t.Fatalf("find.ParseArgs rejected round-tripped args %#v: %+v",
						got.Args, perr)
				}
			case "grep":
				if _, perr := grep.ParseArgs(got.Args); perr != nil {
					t.Fatalf("grep.ParseArgs rejected round-tripped args %#v: %+v",
						got.Args, perr)
				}
			}
		})
	}
}
