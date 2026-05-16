package replay

import (
	"testing"

	"github.com/stazelabs/ash/internal/proto"
)

func TestCommonPrefixLen(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"hello", "help", 3},
		{"abc", "xyz", 0},
		{"abc", "abc", 3},
		{"abcdef", "abc", 3},
		{"abc", "abcdef", 3},
	}
	for _, c := range cases {
		got := commonPrefixLen([]byte(c.a), []byte(c.b))
		if got != c.want {
			t.Errorf("commonPrefixLen(%q,%q): got %d want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestEncodeLegacy_PutsVolatileEarly(t *testing.T) {
	// The whole point of the A/B is that legacy encoding interleaves
	// volatile fields before Data. Two responses sharing Data but
	// differing in ID should have a SHORTER matching prefix under the
	// legacy struct than under proto.Response.
	type sample struct {
		Path string `msgpack:"path"`
		Body string `msgpack:"body"`
	}
	data := proto.MustData(sample{Path: "internal/proto/proto.go", Body: "// hello, world"})

	a := &proto.Response{
		V: proto.ProtocolVersion, OK: true, Data: data,
		ID: 0x1111111111111111,
		Metrics: &proto.Metrics{
			LatencyExecUs: 100, TokensOut: 200,
		},
	}
	b := &proto.Response{
		V: proto.ProtocolVersion, OK: true, Data: data,
		ID: 0x9999999999999999,
		Metrics: &proto.Metrics{
			LatencyExecUs: 222, TokensOut: 200,
		},
	}

	newA, err := proto.EncodeResponse(a)
	if err != nil {
		t.Fatalf("encode new a: %v", err)
	}
	newB, err := proto.EncodeResponse(b)
	if err != nil {
		t.Fatalf("encode new b: %v", err)
	}
	legA, err := encodeLegacy(a)
	if err != nil {
		t.Fatalf("encode legacy a: %v", err)
	}
	legB, err := encodeLegacy(b)
	if err != nil {
		t.Fatalf("encode legacy b: %v", err)
	}

	newPref := commonPrefixLen(newA, newB)
	legPref := commonPrefixLen(legA, legB)

	if newPref <= legPref {
		t.Errorf("expected new ordering to beat legacy: new=%d legacy=%d", newPref, legPref)
	}
	// And the legacy prefix should collapse to a handful of bytes —
	// the moment ID diverges the match ends. Encoded ID lives just
	// after the v key in the legacy struct, so the prefix is the
	// `\x86\xa1v\x02\xa2id` framing plus a byte or two of the differing
	// uint64. Cap at 16 to leave room for msgpack header noise.
	if legPref > 16 {
		t.Errorf("legacy prefix unexpectedly long (%d) — does it really push ID early?", legPref)
	}
}

func TestComputeCachePrefix_DataStablePair(t *testing.T) {
	type sample struct {
		N int `msgpack:"n"`
	}
	data := proto.MustData(sample{N: 42})
	mk := func(id uint64, lat int64) *proto.Response {
		return &proto.Response{
			V: proto.ProtocolVersion, OK: true, Data: data,
			ID:      id,
			Metrics: &proto.Metrics{LatencyExecUs: lat, TokensOut: 100},
		}
	}
	in := []verbResp{
		{Verb: "grep", Rsp: mk(1, 10)},
		{Verb: "grep", Rsp: mk(2, 20)},
		{Verb: "grep", Rsp: mk(3, 30)},
	}
	r := computeCachePrefix(in)
	if r == nil {
		t.Fatal("nil result")
	}
	if r.Overall.Pairs != 2 {
		t.Errorf("Pairs: got %d want 2", r.Overall.Pairs)
	}
	if r.Overall.StablePairs != 2 {
		t.Errorf("StablePairs: got %d want 2", r.Overall.StablePairs)
	}
	if r.Overall.AvgPrefixGain <= 0 {
		t.Errorf("expected positive cache-prefix gain, got %d", r.Overall.AvgPrefixGain)
	}
	if r.Overall.AvgPrefixNew <= r.Overall.AvgPrefixOld {
		t.Errorf("new(%d) must exceed old(%d) for the A/B to mean anything",
			r.Overall.AvgPrefixNew, r.Overall.AvgPrefixOld)
	}
	// And the cache win should be substantial — at least a dozen
	// bytes, dominated by Data and Err sharing across calls under the
	// new ordering. (We can't demand a high fraction of encoded_len
	// here: a tiny Data payload makes the volatile suffix dominate.)
	if r.Overall.AvgPrefixGain < 10 {
		t.Errorf("new prefix gain only %d bytes — expected >= 10 (new=%d old=%d enc_len=%d)",
			r.Overall.AvgPrefixGain, r.Overall.AvgPrefixNew,
			r.Overall.AvgPrefixOld, r.Overall.AvgEncodedLen)
	}
}

func TestComputeCachePrefix_DataDifferentSkipsStable(t *testing.T) {
	type sample struct {
		Body string `msgpack:"body"`
	}
	mk := func(body string, id uint64) *proto.Response {
		return &proto.Response{
			V: proto.ProtocolVersion, OK: true,
			Data: proto.MustData(sample{Body: body}),
			ID:   id,
		}
	}
	in := []verbResp{
		{Verb: "read", Rsp: mk("hello", 1)},
		{Verb: "read", Rsp: mk("goodbye", 2)},
	}
	r := computeCachePrefix(in)
	if r.Overall.Pairs != 1 {
		t.Errorf("Pairs: got %d want 1", r.Overall.Pairs)
	}
	if r.Overall.StablePairs != 0 {
		t.Errorf("StablePairs: got %d want 0 — data differed", r.Overall.StablePairs)
	}
}

func TestComputeCachePrefix_PerVerbGrouping(t *testing.T) {
	type sample struct {
		N int `msgpack:"n"`
	}
	dataA := proto.MustData(sample{N: 1})
	dataB := proto.MustData(sample{N: 2})
	mk := func(verb string, data []byte, id uint64) verbResp {
		return verbResp{Verb: verb, Rsp: &proto.Response{
			V: proto.ProtocolVersion, OK: true, Data: data, ID: id,
		}}
	}
	in := []verbResp{
		mk("grep", dataA, 1),
		mk("read", dataB, 2),
		mk("grep", dataA, 3),
		mk("read", dataB, 4),
	}
	r := computeCachePrefix(in)
	// Cross-verb pairs are NOT counted: grep[0]↔grep[1] and
	// read[0]↔read[1] only.
	if r.Overall.Pairs != 2 {
		t.Errorf("Pairs: got %d want 2", r.Overall.Pairs)
	}
	verbs := map[string]bool{}
	for _, s := range r.ByVerb {
		verbs[s.Verb] = true
		if s.Pairs != 1 {
			t.Errorf("verb %s: pairs=%d want 1", s.Verb, s.Pairs)
		}
	}
	if !verbs["grep"] || !verbs["read"] {
		t.Errorf("expected both grep and read in ByVerb, got %v", verbs)
	}
}

func TestComputeCachePrefix_SortedByVerb(t *testing.T) {
	type sample struct {
		N int `msgpack:"n"`
	}
	mk := func(verb string, id uint64) verbResp {
		return verbResp{Verb: verb, Rsp: &proto.Response{
			V: proto.ProtocolVersion, OK: true,
			Data: proto.MustData(sample{N: 1}), ID: id,
		}}
	}
	// Insert in non-alphabetical order; result.ByVerb should be sorted.
	in := []verbResp{
		mk("zeta", 1), mk("zeta", 2),
		mk("alpha", 3), mk("alpha", 4),
		mk("mu", 5), mk("mu", 6),
	}
	r := computeCachePrefix(in)
	if len(r.ByVerb) != 3 {
		t.Fatalf("ByVerb len: got %d want 3", len(r.ByVerb))
	}
	want := []string{"alpha", "mu", "zeta"}
	for i, w := range want {
		if r.ByVerb[i].Verb != w {
			t.Errorf("ByVerb[%d]: got %s want %s", i, r.ByVerb[i].Verb, w)
		}
	}
}

func TestComputeCachePrefix_EmptyInput(t *testing.T) {
	r := computeCachePrefix(nil)
	if r == nil {
		t.Fatal("nil result on empty input")
	}
	if r.Overall.Pairs != 0 || len(r.ByVerb) != 0 {
		t.Errorf("expected zero pairs / no by_verb rows; got %+v", r)
	}
}

func TestComputeCachePrefix_SingleCallNoPairs(t *testing.T) {
	r := computeCachePrefix([]verbResp{
		{Verb: "grep", Rsp: &proto.Response{V: proto.ProtocolVersion, OK: true}},
	})
	if r.Overall.Pairs != 0 {
		t.Errorf("Pairs: got %d want 0 — single call cannot form a pair", r.Overall.Pairs)
	}
}

func TestParseArgs_CachePrefix(t *testing.T) {
	a, perr := ParseArgs(map[string]any{"cache_prefix": "true"})
	if perr != nil {
		t.Fatalf("parse: %v", perr)
	}
	if !a.CachePrefix {
		t.Error("CachePrefix should be true")
	}
	// Default is false.
	a2, _ := ParseArgs(map[string]any{})
	if a2.CachePrefix {
		t.Error("CachePrefix default should be false")
	}
}
