package proto

import (
	"strings"
	"testing"
)

// resWithTrunc is a stand-in for any truncating verb's Result. Every
// such verb names its hint field with msgpack tag "truncation_hint", so
// MCPTruncationHint behaves identically against this shape and against
// the real grep/find/read/log/diff Result types.
type resWithTrunc struct {
	Count int        `msgpack:"count"`
	Hint  *TruncInfo `msgpack:"truncation_hint,omitempty"`
}

// TestMCPTruncationHint covers the partial-decode probe. It must
// extract the hint when present, return nil when absent, and ignore
// any other fields the verb's Result might carry.
func TestMCPTruncationHint(t *testing.T) {
	rsp := &Response{
		V:  ProtocolVersion,
		ID: 1,
		OK: true,
		Data: MustData(resWithTrunc{
			Count: 256,
			Hint:  &TruncInfo{Trunc: 1, Limit: 256, Max: 4096},
		}),
	}
	got := MCPTruncationHint(rsp)
	if got == nil {
		t.Fatal("MCPTruncationHint returned nil on a truncated response")
	}
	if got.Limit != 256 || got.Max != 4096 || got.Trunc != 1 {
		t.Errorf("hint = %+v; want {Trunc:1 Limit:256 Max:4096}", got)
	}

	// Untruncated response — the verb's Result has no Hint set, so
	// the omitempty msgpack tag drops the field entirely; the probe
	// decodes a zero pointer.
	rspOK := &Response{
		V:    ProtocolVersion,
		ID:   2,
		OK:   true,
		Data: MustData(resWithTrunc{Count: 10}),
	}
	if got := MCPTruncationHint(rspOK); got != nil {
		t.Errorf("MCPTruncationHint(untruncated) = %+v; want nil", got)
	}

	// Defensive paths must all return nil so the sentinel stays off
	// in any error / pre-decode state.
	for name, r := range map[string]*Response{
		"nil response":   nil,
		"err response":   {OK: false, Err: &Error{Code: "boom", Msg: "x"}},
		"empty data":     {OK: true},
		"non-ok with data": {
			OK:   false,
			Data: MustData(resWithTrunc{Count: 1, Hint: &TruncInfo{Trunc: 1, Limit: 1, Max: 1}}),
		},
	} {
		if got := MCPTruncationHint(r); got != nil {
			t.Errorf("%s: got %+v; want nil", name, got)
		}
	}
}

// TestMCPTruncationSentinel pins the two phrasings that ashmcp emits
// and ashd accounts for in tokens_out_emit. Limit==Max yields the
// "hard cap" wording (raising the limit cannot help); Limit<Max yields
// the "or raise the verb's limit flag" wording. The strings live in
// proto so both sides cannot drift.
func TestMCPTruncationSentinel(t *testing.T) {
	rspSoft := &Response{
		V:  ProtocolVersion,
		ID: 1,
		OK: true,
		Data: MustData(resWithTrunc{
			Hint: &TruncInfo{Trunc: 1, Limit: 256, Max: 4096},
		}),
	}
	got := MCPTruncationSentinel(rspSoft)
	if !strings.HasPrefix(got, "truncated:") {
		t.Errorf("soft cap sentinel = %q; want prefix \"truncated:\"", got)
	}
	if !strings.Contains(got, "256") || !strings.Contains(got, "4096") {
		t.Errorf("soft cap sentinel = %q; want both limit and max inline", got)
	}
	if !strings.Contains(got, "raise") {
		t.Errorf("soft cap sentinel = %q; want \"raise\" wording when Limit<Max", got)
	}

	rspHard := &Response{
		V:  ProtocolVersion,
		ID: 2,
		OK: true,
		Data: MustData(resWithTrunc{
			Hint: &TruncInfo{Trunc: 1, Limit: 4096, Max: 4096},
		}),
	}
	got = MCPTruncationSentinel(rspHard)
	if !strings.Contains(got, "hard cap") {
		t.Errorf("hard cap sentinel = %q; want \"hard cap\" wording when Limit==Max", got)
	}
	if strings.Contains(got, "raise") {
		t.Errorf("hard cap sentinel = %q; must not suggest raising the limit", got)
	}

	// No hint → empty sentinel. Lets ashd add `len("")` and
	// `Count("")` unconditionally without inflating tokens_out_emit
	// on non-truncated calls.
	rspOK := &Response{OK: true, Data: MustData(resWithTrunc{Count: 5})}
	if got := MCPTruncationSentinel(rspOK); got != "" {
		t.Errorf("untruncated sentinel = %q; want empty string", got)
	}
}
