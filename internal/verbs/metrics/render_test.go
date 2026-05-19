package metrics

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stazelabs/ash/internal/proto"
)

// pickColumns gates which sub-phase columns appear in the pretty table.
// A regression here would either crowd the table with empty columns
// (cluttered output) or silently drop columns that some rows do
// populate (hidden data).
func TestPickColumns_AllOff(t *testing.T) {
	cs := pickColumns([]Row{
		{Verb: "find", TokensIn: 10, TokensOut: 100, LatencyExecUs: 50},
	})
	if cs.walk || cs.io || cs.regex || cs.regexCompile || cs.dispatch || cs.emit || cs.cache {
		t.Errorf("all-zero sub-phases should produce zero colSet, got %+v", cs)
	}
}

func TestPickColumns_EachFlagIndependent(t *testing.T) {
	cases := []struct {
		name string
		row  Row
		want colSet
	}{
		{name: "walk", row: Row{WalkUs: 1}, want: colSet{walk: true}},
		{name: "io", row: Row{IOUs: 1}, want: colSet{io: true}},
		{name: "regex", row: Row{RegexUs: 1}, want: colSet{regex: true}},
		{name: "regex_compile", row: Row{RegexCompileUs: 1}, want: colSet{regexCompile: true}},
		{name: "dispatch", row: Row{LatencyDispatchUs: 1}, want: colSet{dispatch: true}},
		{name: "emit_from_bytes", row: Row{BytesOutEmit: 1}, want: colSet{emit: true}},
		{name: "cache_hit_only", row: Row{TokensCacheHit: 1}, want: colSet{cache: true}},
		{name: "cache_miss_only", row: Row{TokensCacheMiss: 1}, want: colSet{cache: true}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := pickColumns([]Row{c.row})
			if got != c.want {
				t.Errorf("got %+v, want %+v", got, c.want)
			}
		})
	}
}

// One-flag-on-any-row should be enough to light the column for the
// whole table — ensures we don't only check the first row.
func TestPickColumns_AnyRowLightsTheColumn(t *testing.T) {
	rows := []Row{
		{Verb: "find"},  // no sub-phases
		{Verb: "grep"},  // no sub-phases
		{Verb: "stat", WalkUs: 123, IOUs: 456}, // these two should light up
	}
	cs := pickColumns(rows)
	if !cs.walk || !cs.io {
		t.Errorf("walk + io should light up from the third row: %+v", cs)
	}
	if cs.regex || cs.dispatch {
		t.Errorf("untouched sub-phases should stay off: %+v", cs)
	}
}

// writeOptInt: zero → empty padded field; non-zero → numeric padded.
func TestWriteOptInt(t *testing.T) {
	cases := []struct {
		v     int64
		width int
		want  string
	}{
		{v: 0, width: 5, want: "  " + "     "}, // 2 leading spaces + width-padded empty
		{v: 42, width: 5, want: "  42   "},
		{v: 12345, width: 5, want: "  12345"},
		{v: -1, width: 5, want: "  " + "     "}, // negatives are also treated as "no signal"
	}
	for _, c := range cases {
		var b strings.Builder
		writeOptInt(&b, c.v, c.width)
		if b.String() != c.want {
			t.Errorf("writeOptInt(%d, %d) = %q, want %q", c.v, c.width, b.String(), c.want)
		}
	}
}

// scopeFromArgs decides what shows up in the §metrics header brackets.
// Default last is suppressed; non-default is surfaced; verb always
// surfaces when non-empty; both join with comma+space.
func TestScopeFromArgs(t *testing.T) {
	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{name: "nil_args", args: nil, want: ""},
		{name: "empty_args", args: map[string]any{}, want: ""},
		{name: "default_last_suppressed", args: map[string]any{"last": DefaultLast}, want: ""},
		{name: "non_default_last", args: map[string]any{"last": 50}, want: "last=50"},
		{name: "non_default_last_string", args: map[string]any{"last": "50"}, want: "last=50"},
		{name: "verb_only", args: map[string]any{"verb": "grep"}, want: "verb=grep"},
		{name: "empty_verb_suppressed", args: map[string]any{"verb": ""}, want: ""},
		{name: "both_combined", args: map[string]any{"last": 100, "verb": "find"}, want: "last=100, verb=find"},
		{name: "verb_present_default_last", args: map[string]any{"last": DefaultLast, "verb": "find"}, want: "verb=find"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := &proto.Request{Args: c.args}
			if got := scopeFromArgs(req); got != c.want {
				t.Errorf("scopeFromArgs(%v) = %q, want %q", c.args, got, c.want)
			}
		})
	}
}

func TestScopeFromArgs_NilRequest(t *testing.T) {
	if got := scopeFromArgs(nil); got != "" {
		t.Errorf("nil request: got %q", got)
	}
}

// PrettyResponse covers the routing + zero-rows short-circuit + header
// rendering. Constructs a synthetic Result envelope via proto.MustData.
func okResp(t *testing.T, r *Result) *proto.Response {
	t.Helper()
	return &proto.Response{V: proto.ProtocolVersion, OK: true, Data: proto.MustData(r)}
}

func TestPrettyResponse_NotOK(t *testing.T) {
	rsp := &proto.Response{V: proto.ProtocolVersion, OK: false, Err: &proto.Error{Code: "ledger", Msg: "boom"}}
	out := PrettyResponse(&proto.Request{}, rsp)
	if !strings.Contains(out, "ledger") {
		t.Errorf("error path should surface error code: %s", out)
	}
}

func TestPrettyResponse_ZeroRowsShortCircuit(t *testing.T) {
	r := &Result{Rows: nil, Count: 0}
	out := PrettyResponse(&proto.Request{}, okResp(t, r))
	if !strings.Contains(out, "§metrics: 0 calls") {
		t.Errorf("header: %s", out)
	}
	// Zero-row path skips the column header — no "ts" "verb" "ok" line.
	if strings.Contains(out, "  ok ") {
		t.Errorf("zero-rows path should not emit the column header: %s", out)
	}
}

// End-to-end pretty: build a small Result with rows that exercise
// each conditional column + each flag.
func TestPrettyResponse_FullTable(t *testing.T) {
	now := time.Date(2026, 1, 2, 15, 4, 5, 0, time.UTC)
	r := &Result{
		Rows: []Row{
			{
				Timestamp: now.UnixNano(), Verb: "grep", OK: true,
				TokensIn: 10, TokensOut: 100, LatencyExecUs: 500,
				WalkUs: 200, IOUs: 150, RegexUs: 80, RegexCompileUs: 30,
				LatencyDispatchUs: 20,
				BytesOutEmit: 1024, TokensOutEmit: 42,
				TokensCacheHit: 500, TokensCacheMiss: 100,
			},
			{
				Timestamp: now.UnixNano(), Verb: "read", OK: false,
				ErrCode: "args", Truncated: true,
				TokensIn: 1, TokensOut: 0, LatencyExecUs: 30,
			},
		},
		Count: 2,
	}
	out := PrettyResponse(&proto.Request{Args: map[string]any{"last": 100}}, okResp(t, r))
	for _, want := range []string{
		"§metrics: 2 calls",
		"[last=100]",
		// header letters
		"ts", "verb", "ok",
		"n", "o", "x",
		"w", "i", "r", "R", "d",
		"oE", "ch", "cm",
		"flags",
		// row data
		"grep", "read",
		"2026-01-02T15:04:05Z",
		"ok ", "ERR",
		"err=args", "trunc",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

// Row-level OK vs ERR is rendered as "ok " (with trailing space — the
// 3-char-wide field) vs "ERR". A regression that flipped this would
// confuse readers scanning a busy table.
func TestPrettyResponse_OKAndERRRendering(t *testing.T) {
	r := &Result{
		Rows: []Row{
			{Verb: "find", OK: true, Timestamp: time.Now().UnixNano()},
			{Verb: "find", OK: false, ErrCode: "args", Timestamp: time.Now().UnixNano()},
		},
		Count: 2,
	}
	out := PrettyResponse(&proto.Request{}, okResp(t, r))
	// "ok " appears in the success row; "ERR" in the failed.
	if !regexp.MustCompile(`(?m)^.*find.*ok\b`).MatchString(out) {
		t.Errorf("ok row missing 'ok' status:\n%s", out)
	}
	if !regexp.MustCompile(`(?m)^.*find.*ERR\b`).MatchString(out) {
		t.Errorf("ERR row missing 'ERR' status:\n%s", out)
	}
}

func TestPrettyResponse_NonOKResponseRoute(t *testing.T) {
	// proto.UnmarshalData fails when Data is missing — should fall
	// through to the unrecognized-result branch.
	rsp := &proto.Response{V: proto.ProtocolVersion, OK: true} // no Data
	out := PrettyResponse(&proto.Request{}, rsp)
	if !strings.Contains(out, "unrecognized metrics result") {
		t.Errorf("unrecognized branch not hit:\n%s", out)
	}
}

// CompactResponse — the cols/rows hybrid shape. K column order is the
// contract — agents/MCP clients positional-decode by it. Pin the
// order explicitly.
func TestCompactResponse_ColumnOrderContract(t *testing.T) {
	r := &Result{
		Rows: []Row{
			{Verb: "grep", Timestamp: 12345, OK: true, TokensIn: 1, TokensOut: 2, LatencyExecUs: 3},
		},
	}
	cd, err := CompactResponse(okResp(t, r))
	if err != nil {
		t.Fatalf("CompactResponse: %v", err)
	}
	got := cd.(proto.CompactData)
	want := []string{
		"ts", "verb", "ok", "err", "ti", "to", "ex_us",
		"bi", "bo", "trunc",
		"walk", "io", "re", "recp", "disp",
		"toE", "boE",
		"ch", "cm",
	}
	if len(got.K) != len(want) {
		t.Fatalf("K len: got %d, want %d\ngot:  %v\nwant: %v", len(got.K), len(want), got.K, want)
	}
	for i, k := range want {
		if got.K[i] != k {
			t.Errorf("K[%d]: got %q, want %q", i, got.K[i], k)
		}
	}
	// One row in, one row out, in the same column order.
	if len(got.R) != 1 || len(got.R[0]) != len(want) {
		t.Fatalf("R shape: %+v", got.R)
	}
	// Spot-check a few values via positional index.
	row := got.R[0]
	if row[0] != int64(12345) {
		t.Errorf("R[0][0] (ts): got %v, want 12345", row[0])
	}
	if row[1] != "grep" {
		t.Errorf("R[0][1] (verb): got %v, want grep", row[1])
	}
	if row[2] != true {
		t.Errorf("R[0][2] (ok): got %v, want true", row[2])
	}
}

func TestCompactResponse_NotOK(t *testing.T) {
	rsp := &proto.Response{V: proto.ProtocolVersion, OK: false, Err: &proto.Error{Code: "x"}}
	got, err := CompactResponse(rsp)
	if err != nil {
		t.Errorf("non-OK should not error: %v", err)
	}
	if got != nil {
		t.Errorf("non-OK should return nil data: %v", got)
	}
}

func TestCompactResponse_MissingDataErrors(t *testing.T) {
	// OK=true but no Data → UnmarshalData fails → CompactResponse
	// surfaces the error.
	rsp := &proto.Response{V: proto.ProtocolVersion, OK: true}
	_, err := CompactResponse(rsp)
	if err == nil {
		t.Error("missing Data on OK response should error")
	}
}
