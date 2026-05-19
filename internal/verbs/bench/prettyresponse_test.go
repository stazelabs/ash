package bench

import (
	"strings"
	"testing"

	"github.com/stazelabs/ash/internal/proto"
)

// PrettyResponse dispatches on Result.Kind. Construct an envelope for
// each branch and check we route to the right renderer. The renderers
// themselves are covered in helpers/baseline/trend/micro test files;
// here we just verify the routing.

func okResponse(t *testing.T, data any) *proto.Response {
	t.Helper()
	return &proto.Response{V: proto.ProtocolVersion, OK: true, Data: proto.MustData(data)}
}

func req() *proto.Request {
	return &proto.Request{V: proto.ProtocolVersion, Verb: "bench"}
}

func TestPrettyResponse_RoutesByKind(t *testing.T) {
	cases := []struct {
		name string
		data any
		want string
	}{
		{
			name: "list",
			data: ListResult{Kind: kindList},
			want: "§bench --list:",
		},
		{
			name: "compare",
			data: CompareResult{Kind: kindCompare, A: RunSummary{RunUUID: "A"}, B: RunSummary{RunUUID: "B"}, CaseSetMatch: true},
			want: "§bench compare:",
		},
		{
			name: "record_baseline",
			data: RecordBaselineResult{Kind: kindRecord, BaselinePath: "bench/baseline.json", MarkdownPath: "bench/baseline.md", LatencyPath: "bench/latency-snapshot.json"},
			want: "§bench --record-baseline",
		},
		{
			name: "export_md",
			data: ExportMdResult{Kind: kindExport, Body: "# rendered markdown"},
			want: "# rendered markdown",
		},
		{
			name: "micro",
			data: MicroResult{Kind: kindMicro, BenchTime: "1s", Count: 1},
			want: "§bench --micro:",
		},
		{
			name: "record_micro",
			data: RecordMicroResult{Kind: kindRecordMicro, JSONPath: "j", MarkdownPath: "m"},
			want: "§bench --record-micro",
		},
		{
			name: "diff_micro",
			data: DiffMicroResult{Kind: kindDiffMicro, RegressPct: 10, BaseTS: "2026-01-02T00:00:00Z"},
			want: "§bench --diff-micro:",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := PrettyResponse(req(), okResponse(t, c.data))
			if !strings.Contains(out, c.want) {
				t.Errorf("PrettyResponse(%s) missing %q\nactual:\n%s", c.name, c.want, out)
			}
		})
	}
}

// Standard fresh-bench result has no Kind field; PrettyResponse falls
// through to the per-case + by-verb + overall rendering branch.
func TestPrettyResponse_StandardResult(t *testing.T) {
	r := Result{
		Cases: []CaseResult{
			{Name: "case_a", Verb: "find", AshTokens: 100, BashTokens: 200, AshLatencyUs: 50, BashLatencyUs: 100, AshOK: true},
			{Name: "case_b", Verb: "grep", AshTokens: 50, BashTokens: 50, AshOK: false, AshErr: "boom", BashTruncated: true, BashRunErr: "bash blew up"},
		},
		ByVerb: []VerbSummary{
			{Verb: "find", Cases: 1, AshTokensTotal: 100, BashTokensTotal: 200, AshLatencyUsTotal: 50, BashLatencyUsTotal: 100},
			{Verb: "grep", Cases: 1, AshTokensTotal: 50, BashTokensTotal: 50, AshLatencyUsTotal: 0, BashLatencyUsTotal: 0},
		},
		Overall: VerbSummary{Verb: "overall", Cases: 2, AshTokensTotal: 150, BashTokensTotal: 250, AshLatencyUsTotal: 50, BashLatencyUsTotal: 100},
		NotRun:  []string{"skipped_case"},
		NotRunWhy: map[string]string{"skipped_case": "translation gap"},
	}
	out := PrettyResponse(req(), okResponse(t, r))
	for _, want := range []string{
		"§bench:",
		"2 case(s)",
		"1 skipped",
		"case_a",
		"case_b",
		"ash_err=boom",
		"bash_truncated",
		"bash_err=bash blew up",
		"not run (no bash translation or other gap):",
		"skipped_case — translation gap",
		"by verb:",
		"overall:",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestPrettyResponse_NotOK(t *testing.T) {
	rsp := &proto.Response{V: proto.ProtocolVersion, OK: false, Err: &proto.Error{Code: "config", Msg: "ledger missing"}}
	out := PrettyResponse(req(), rsp)
	// PrettyResponseHeader formatting: the renderer just delegates to
	// the proto helper, so we only check we got *something* with the code.
	if !strings.Contains(out, "config") {
		t.Errorf("error path should surface error code: %s", out)
	}
}
