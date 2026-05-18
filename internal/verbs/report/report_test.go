package report

import (
	"strings"
	"testing"
	"time"

	"github.com/stazelabs/ash/internal/jail"
	"github.com/stazelabs/ash/internal/ledger"
	"github.com/stazelabs/ash/internal/proto"
	"github.com/vmihailenco/msgpack/v5"
)

// encodeArgs encodes an args map into the current ledger format (plain msgpack map).
func encodeArgs(t *testing.T, _ string, args map[string]any) []byte {
	t.Helper()
	b, err := msgpack.Marshal(args)
	if err != nil {
		t.Fatalf("encodeArgs: %v", err)
	}
	return b
}

func TestDecodeArgsMap(t *testing.T) {
	args := map[string]any{"path": ".", "glob": "**/*.go"}
	blob, err := msgpack.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	got := decodeArgsMap(blob)
	if got["path"] != "." || got["glob"] != "**/*.go" {
		t.Errorf("got %v", got)
	}
	if decodeArgsMap(nil) != nil {
		t.Error("nil blob should return nil")
	}
}

func TestPercentile(t *testing.T) {
	tests := []struct {
		vals []int64
		p    float64
		want int64
	}{
		{nil, 0.5, 0},
		{[]int64{100}, 0.5, 100},
		{[]int64{100}, 0.95, 100},
		{[]int64{10, 20, 30, 40, 50}, 0.50, 30},
		{[]int64{10, 20, 30, 40, 50}, 0.95, 40}, // floor(0.95*4)=3 → sorted[3]=40
		{[]int64{50, 10, 30, 20, 40}, 0.50, 30}, // unsorted input
		{[]int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}, 0.90, 9}, // floor(0.90*9)=8 → sorted[8]=9
	}
	for _, tt := range tests {
		got := percentile(tt.vals, tt.p)
		if got != tt.want {
			t.Errorf("percentile(%v, %.2f) = %d, want %d", tt.vals, tt.p, got, tt.want)
		}
	}
}

func TestPct(t *testing.T) {
	if pct(0, 0) != 0 {
		t.Error("pct(0,0) should be 0")
	}
	if pct(1, 2) != 50 {
		t.Errorf("pct(1,2) = %v, want 50", pct(1, 2))
	}
	if pct(3, 3) != 100 {
		t.Errorf("pct(3,3) = %v, want 100", pct(3, 3))
	}
}

func TestParseDuration(t *testing.T) {
	tests := []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{"1h", time.Hour, false},
		{"15m", 15 * time.Minute, false},
		{"1d", 24 * time.Hour, false},
		{"7d", 7 * 24 * time.Hour, false},
		{"0d", 0, true},
		{"-1d", 0, true},
		{"bad", 0, true},
	}
	for _, tt := range tests {
		got, err := parseDuration(tt.in)
		if tt.wantErr {
			if err == nil {
				t.Errorf("parseDuration(%q) expected error, got nil", tt.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseDuration(%q) unexpected error: %v", tt.in, err)
		}
		if got != tt.want {
			t.Errorf("parseDuration(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestParseArgs(t *testing.T) {
	// Defaults
	a, err := ParseArgs(map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if a.Session != "current" {
		t.Errorf("default session = %q, want 'current'", a.Session)
	}
	if a.Last != 0 || a.Since != 0 || a.Verb != "" {
		t.Errorf("unexpected non-zero default: last=%d since=%v verb=%q", a.Last, a.Since, a.Verb)
	}

	// All fields
	a, err = ParseArgs(map[string]any{
		"session": "all",
		"since":   "1h",
		"last":    float64(50),
		"verb":    "find",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if a.Session != "all" {
		t.Errorf("session = %q, want 'all'", a.Session)
	}
	if a.Since != time.Hour {
		t.Errorf("since = %v, want 1h", a.Since)
	}
	if a.Last != 50 {
		t.Errorf("last = %d, want 50", a.Last)
	}
	if a.Verb != "find" {
		t.Errorf("verb = %q, want 'find'", a.Verb)
	}

	// last > MaxLast is clamped
	a, _ = ParseArgs(map[string]any{"last": float64(99999)})
	if a.Last != MaxLast {
		t.Errorf("last not clamped: got %d", a.Last)
	}
}

func makeCalls(verb string, n int, execUs int64, ok bool, truncated bool) []ledger.Call {
	calls := make([]ledger.Call, n)
	for i := range calls {
		calls[i] = ledger.Call{
			Verb:          verb,
			OK:            ok,
			Truncated:     truncated,
			LatencyExecUs: execUs + int64(i)*10,
			TokensOut:     100 + i,
		}
	}
	return calls
}

func TestAggregate_Empty(t *testing.T) {
	r := aggregate(nil, Scope{Session: "current"})
	if r.Totals.Calls != 0 {
		t.Errorf("expected 0 calls, got %d", r.Totals.Calls)
	}
	if len(r.ByVerb) != 0 {
		t.Errorf("expected empty ByVerb, got %v", r.ByVerb)
	}
}

func TestAggregate_SingleVerb(t *testing.T) {
	calls := makeCalls("find", 4, 1000, true, false)
	calls[2].Truncated = true // 1 truncated

	r := aggregate(calls, Scope{Session: "current"})
	if r.Totals.Calls != 4 {
		t.Errorf("Totals.Calls = %d, want 4", r.Totals.Calls)
	}
	if r.Totals.OK != 4 {
		t.Errorf("Totals.OK = %d, want 4", r.Totals.OK)
	}
	if len(r.ByVerb) != 1 {
		t.Fatalf("len(ByVerb) = %d, want 1", len(r.ByVerb))
	}
	vs := r.ByVerb[0]
	if vs.Verb != "find" {
		t.Errorf("Verb = %q, want 'find'", vs.Verb)
	}
	if vs.N != 4 {
		t.Errorf("N = %d, want 4", vs.N)
	}
	if vs.OKPct != 100 {
		t.Errorf("OKPct = %.1f, want 100", vs.OKPct)
	}
	if vs.TruncatedN != 1 {
		t.Errorf("TruncatedN = %d, want 1", vs.TruncatedN)
	}
}

func TestAggregate_MultiVerb(t *testing.T) {
	calls := append(makeCalls("find", 3, 1000, true, false), makeCalls("grep", 2, 5000, false, false)...)
	r := aggregate(calls, Scope{Session: "current"})

	if len(r.ByVerb) != 2 {
		t.Fatalf("len(ByVerb) = %d, want 2", len(r.ByVerb))
	}
	if r.ByVerb[0].Verb != "find" {
		t.Errorf("first verb = %q, want 'find'", r.ByVerb[0].Verb)
	}
	if r.ByVerb[1].Verb != "grep" {
		t.Errorf("second verb = %q, want 'grep'", r.ByVerb[1].Verb)
	}
	if r.ByVerb[1].OKPct != 0 {
		t.Errorf("grep ok%% = %.1f, want 0", r.ByVerb[1].OKPct)
	}
	if r.Totals.Errors != 2 {
		t.Errorf("Totals.Errors = %d, want 2", r.Totals.Errors)
	}
}

func TestFmtUs(t *testing.T) {
	tests := []struct{ us int64; want string }{
		{0, "0us"},
		{142, "142us"},
		{999, "999us"},
		{1000, "1.0ms"},
		{2400, "2.4ms"},
		{999999, "1000.0ms"},
		{1_000_000, "1.0s"},
		{26_400_000, "26.4s"},
	}
	for _, tt := range tests {
		got := fmtUs(tt.us)
		if got != tt.want {
			t.Errorf("fmtUs(%d) = %q, want %q", tt.us, got, tt.want)
		}
	}
}

func makeCallsWithPhases(verb string, execUs, walkUs, ioUs, regexUs int64) []ledger.Call {
	return []ledger.Call{{
		Verb:          verb,
		OK:            true,
		LatencyExecUs: execUs,
		WalkUs:        walkUs,
		IOUs:          ioUs,
		RegexUs:       regexUs,
	}}
}

func TestAggregate_SubPhases_FindLike(t *testing.T) {
	// exec=1000, walk=830 (io=210, regex=0 are subsets)
	// walk_excl = 830-210 = 620 → 62%, io = 21%, regex = 0%, other = 17%
	calls := makeCallsWithPhases("find", 1000, 830, 210, 0)
	r := aggregate(calls, Scope{Session: "current"})

	if len(r.ByVerb) != 1 {
		t.Fatalf("expected 1 verb, got %d", len(r.ByVerb))
	}
	vs := r.ByVerb[0]
	if len(vs.SubPhases) == 0 {
		t.Fatal("expected SubPhases to be populated")
	}

	cases := []struct{ name string; want float64 }{
		{"walk", 62},
		{"io", 21},
		{"regex", 0},
		{"other", 17},
	}
	for _, c := range cases {
		got := subPhasePct(vs.SubPhases, c.name)
		if got != c.want {
			t.Errorf("subPhase %q = %.0f, want %.0f", c.name, got, c.want)
		}
	}
}

func TestAggregate_SubPhases_GrepLike(t *testing.T) {
	// exec=1000, walk=900 (io=440, regex=80 are subsets)
	// walk_excl = 900-440-80 = 380 → 38%, io = 44%, regex = 8%, other = 10%
	calls := makeCallsWithPhases("grep", 1000, 900, 440, 80)
	r := aggregate(calls, Scope{Session: "current"})

	vs := r.ByVerb[0]
	cases := []struct{ name string; want float64 }{
		{"walk", 38},
		{"io", 44},
		{"regex", 8},
		{"other", 10},
	}
	for _, c := range cases {
		got := subPhasePct(vs.SubPhases, c.name)
		if got != c.want {
			t.Errorf("subPhase %q = %.0f, want %.0f", c.name, got, c.want)
		}
	}
}

func TestAggregate_SubPhases_NoPhaseData(t *testing.T) {
	calls := makeCalls("read", 2, 500, true, false) // WalkUs/IOUs/RegexUs all zero
	r := aggregate(calls, Scope{Session: "current"})

	if len(r.ByVerb[0].SubPhases) != 0 {
		t.Errorf("expected no SubPhases when phases are zero, got %v", r.ByVerb[0].SubPhases)
	}
}

func TestAggregate_SubPhases_WalkExclClamped(t *testing.T) {
	// Walk < io+regex (clock skew / data anomaly): exclusive walk should clamp to 0.
	calls := makeCallsWithPhases("grep", 1000, 100, 400, 100)
	r := aggregate(calls, Scope{Session: "current"})

	vs := r.ByVerb[0]
	walkPct := subPhasePct(vs.SubPhases, "walk")
	if walkPct < 0 {
		t.Errorf("walk%% should be >= 0, got %.1f", walkPct)
	}
}

func TestPctOf(t *testing.T) {
	if pctOf(0, 0) != 0 {
		t.Error("pctOf(0,0) should be 0")
	}
	if pctOf(620, 1000) != 62.0 {
		t.Errorf("pctOf(620,1000) = %v, want 62.0", pctOf(620, 1000))
	}
	if pctOf(1000, 1000) != 100.0 {
		t.Errorf("pctOf(1000,1000) = %v, want 100.0", pctOf(1000, 1000))
	}
}

func TestSubPhasePct(t *testing.T) {
	phases := []VerbSubPhase{
		{Name: "walk", Pct: 62},
		{Name: "io", Pct: 21},
		{Name: "regex", Pct: 0},
		{Name: "other", Pct: 17},
	}
	if subPhasePct(phases, "walk") != 62 {
		t.Errorf("expected 62, got %v", subPhasePct(phases, "walk"))
	}
	if subPhasePct(phases, "missing") != 0 {
		t.Errorf("expected 0 for missing phase, got %v", subPhasePct(phases, "missing"))
	}
}

func TestPrettyResponse_SubPhaseSection(t *testing.T) {
	calls := makeCallsWithPhases("find", 1000, 830, 210, 0)
	r := aggregate(calls, Scope{Session: "current"})

	// Wire shape: PrettyResponse decodes Data via msgpack.Unmarshal.
	rsp := &proto.Response{OK: true}
	rsp.Data = proto.MustData(r)
	out := PrettyResponse(nil, rsp)

	if !strings.Contains(out, "sub-phase breakdown") {
		t.Errorf("expected sub-phase breakdown section in output:\n%s", out)
	}
	if !strings.Contains(out, "find") {
		t.Errorf("expected verb name in sub-phase section:\n%s", out)
	}
}

func makeFailedCalls(verb, errCode, errMsg string, n int) []ledger.Call {
	calls := make([]ledger.Call, n)
	for i := range calls {
		calls[i] = ledger.Call{
			Verb:          verb,
			OK:            false,
			ErrCode:       errCode,
			ErrMsg:        errMsg,
			LatencyExecUs: 100,
		}
	}
	return calls
}

func TestAggregate_ErrHistogram(t *testing.T) {
	calls := append(
		makeFailedCalls("grep", "args", "bad pattern", 3),
		makeFailedCalls("find", "not_found", "", 1)...,
	)
	r := aggregate(calls, Scope{Session: "current"})

	if len(r.ErrHistogram) != 2 {
		t.Fatalf("expected 2 err entries, got %d", len(r.ErrHistogram))
	}
	// Sorted by count desc: args(3) before not_found(1).
	if r.ErrHistogram[0].Code != "args" {
		t.Errorf("first err code = %q, want 'args'", r.ErrHistogram[0].Code)
	}
	if r.ErrHistogram[0].Count != 3 {
		t.Errorf("args count = %d, want 3", r.ErrHistogram[0].Count)
	}
	if r.ErrHistogram[0].SampleMsg != "bad pattern" {
		t.Errorf("args sample_msg = %q, want 'bad pattern'", r.ErrHistogram[0].SampleMsg)
	}
	if r.ErrHistogram[1].Code != "not_found" {
		t.Errorf("second err code = %q, want 'not_found'", r.ErrHistogram[1].Code)
	}
}

func TestAggregate_ErrHistogram_NoErrCode(t *testing.T) {
	// Errors without an err_code should not appear in the histogram.
	calls := makeFailedCalls("read", "", "", 2)
	r := aggregate(calls, Scope{Session: "current"})
	if len(r.ErrHistogram) != 0 {
		t.Errorf("expected empty histogram for calls with no err_code, got %v", r.ErrHistogram)
	}
}

func TestAggregate_TruncHotspots(t *testing.T) {
	calls := makeCalls("find", 3, 1000, true, true)       // 3 truncated
	calls = append(calls, makeCalls("grep", 2, 500, true, true)...) // 2 truncated
	calls = append(calls, makeCalls("read", 4, 200, true, false)...) // 0 truncated

	r := aggregate(calls, Scope{Session: "current"})

	if len(r.TruncHotspots) != 2 {
		t.Fatalf("expected 2 trunc hotspots, got %d: %v", len(r.TruncHotspots), r.TruncHotspots)
	}
	// Sorted by count desc: find(3) before grep(2).
	if r.TruncHotspots[0].Verb != "find" {
		t.Errorf("first hotspot verb = %q, want 'find'", r.TruncHotspots[0].Verb)
	}
	if r.TruncHotspots[0].Count != 3 {
		t.Errorf("find trunc count = %d, want 3", r.TruncHotspots[0].Count)
	}
	if r.TruncHotspots[1].Verb != "grep" {
		t.Errorf("second hotspot verb = %q, want 'grep'", r.TruncHotspots[1].Verb)
	}
}

func TestAggregate_NoHotspots_WhenClean(t *testing.T) {
	calls := makeCalls("find", 5, 1000, true, false)
	r := aggregate(calls, Scope{Session: "current"})
	if len(r.TruncHotspots) != 0 {
		t.Errorf("expected no trunc hotspots, got %v", r.TruncHotspots)
	}
	if len(r.ErrHistogram) != 0 {
		t.Errorf("expected no err histogram, got %v", r.ErrHistogram)
	}
}

func TestPrettyResponse_HotspotSections(t *testing.T) {
	calls := append(
		makeCalls("find", 3, 1000, true, true),
		makeFailedCalls("grep", "args", "bad pattern", 2)...,
	)
	r := aggregate(calls, Scope{Session: "current"})

	rsp := &proto.Response{OK: true}
	rsp.Data = proto.MustData(r)
	out := PrettyResponse(nil, rsp)

	if !strings.Contains(out, "truncation") {
		t.Errorf("expected truncation section in output:\n%s", out)
	}
	if !strings.Contains(out, "find") {
		t.Errorf("expected 'find' in truncation section:\n%s", out)
	}
	if !strings.Contains(out, "errors") {
		t.Errorf("expected errors section in output:\n%s", out)
	}
	if !strings.Contains(out, "args") {
		t.Errorf("expected 'args' error code in output:\n%s", out)
	}
	if !strings.Contains(out, "bad pattern") {
		t.Errorf("expected sample err_msg in output:\n%s", out)
	}
}

func TestPrettyResponse_NoHotspotSections_WhenClean(t *testing.T) {
	calls := makeCalls("find", 5, 1000, true, false)
	r := aggregate(calls, Scope{Session: "current"})

	rsp := &proto.Response{OK: true}
	rsp.Data = proto.MustData(r)
	out := PrettyResponse(nil, rsp)

	if strings.Contains(out, "truncation") {
		t.Errorf("unexpected truncation section in clean output:\n%s", out)
	}
	if strings.Contains(out, "errors") {
		t.Errorf("unexpected errors section in clean output:\n%s", out)
	}
}

func makeCallsWithBytes(verb string, n int, tokOut, bytesOut int) []ledger.Call {
	calls := make([]ledger.Call, n)
	for i := range calls {
		calls[i] = ledger.Call{
			Verb:          verb,
			OK:            true,
			LatencyExecUs: 100,
			TokensOut:     tokOut,
			BytesOut:      bytesOut,
		}
	}
	return calls
}

func TestAggregate_TokPerKiB(t *testing.T) {
	// 200 tokens, 1024 bytes → 200 tok/KiB
	calls := makeCallsWithBytes("read", 4, 200, 1024)
	r := aggregate(calls, Scope{Session: "current"})

	if len(r.ByVerb) != 1 {
		t.Fatalf("expected 1 verb, got %d", len(r.ByVerb))
	}
	got := r.ByVerb[0].TokPerKiB
	if got != 200.0 {
		t.Errorf("TokPerKiB = %.2f, want 200.0", got)
	}
}

func TestAggregate_TokPerKiB_ZeroBytesOut(t *testing.T) {
	calls := makeCalls("read", 2, 100, true, false) // BytesOut = 0
	r := aggregate(calls, Scope{Session: "current"})

	if r.ByVerb[0].TokPerKiB != 0 {
		t.Errorf("TokPerKiB should be 0 when bytes_out=0, got %.2f", r.ByVerb[0].TokPerKiB)
	}
}

func TestCollectArgDists_basic(t *testing.T) {
	blob1 := encodeArgs(t, "find", map[string]any{"path": ".", "glob": "**/*.go"})
	blob2 := encodeArgs(t, "find", map[string]any{"path": ".", "glob": "**/*.md"})
	blob3 := encodeArgs(t, "find", map[string]any{"path": ".", "glob": "**/*.go"})

	calls := []ledger.Call{
		{Verb: "find", OK: true, ArgsMsgpack: blob1},
		{Verb: "find", OK: true, ArgsMsgpack: blob2},
		{Verb: "find", OK: true, ArgsMsgpack: blob3},
	}
	byVerb := map[string][]ledger.Call{"find": calls}
	order := []string{"find"}

	dists := collectArgDists(byVerb, order)
	if len(dists) != 1 {
		t.Fatalf("expected 1 verb dist, got %d", len(dists))
	}
	vd := dists[0]
	if vd.Verb != "find" {
		t.Errorf("verb = %q, want 'find'", vd.Verb)
	}

	// Find the glob key.
	var globDist *ArgDist
	for i := range vd.Args {
		if vd.Args[i].Key == "glob" {
			globDist = &vd.Args[i]
		}
	}
	if globDist == nil {
		t.Fatal("expected 'glob' key in arg distributions")
	}
	// **/*.go appears twice, **/*.md once — sorted by count desc.
	if len(globDist.Values) != 2 {
		t.Fatalf("expected 2 glob values, got %d", len(globDist.Values))
	}
	if globDist.Values[0].Value != "**/*.go" || globDist.Values[0].Count != 2 {
		t.Errorf("first glob value = %q (%d×), want **/*.go (2×)",
			globDist.Values[0].Value, globDist.Values[0].Count)
	}
	if globDist.Values[1].Value != "**/*.md" || globDist.Values[1].Count != 1 {
		t.Errorf("second glob value = %q (%d×), want **/*.md (1×)",
			globDist.Values[1].Value, globDist.Values[1].Count)
	}
}

func TestCollectArgDists_noArgsMsgpack(t *testing.T) {
	calls := makeCalls("find", 3, 100, true, false) // ArgsMsgpack is nil
	byVerb := map[string][]ledger.Call{"find": calls}
	order := []string{"find"}

	dists := collectArgDists(byVerb, order)
	if len(dists) != 0 {
		t.Errorf("expected no dists when ArgsMsgpack is nil, got %d", len(dists))
	}
}

func TestCollectArgDists_keysAlphabetical(t *testing.T) {
	blob := encodeArgs(t, "grep", map[string]any{"pattern": "TODO", "path": ".", "glob": "**/*.go"})
	calls := []ledger.Call{{Verb: "grep", OK: true, ArgsMsgpack: blob}}
	byVerb := map[string][]ledger.Call{"grep": calls}

	dists := collectArgDists(byVerb, []string{"grep"})
	if len(dists) == 0 {
		t.Fatal("expected arg dists")
	}
	keys := make([]string, len(dists[0].Args))
	for i, d := range dists[0].Args {
		keys[i] = d.Key
	}
	for i := 1; i < len(keys); i++ {
		if keys[i] < keys[i-1] {
			t.Errorf("keys not sorted alphabetically: %v", keys)
		}
	}
}

// ASH-71c: arg distribution values for string-typed args have the active
// project-root prefix stripped before counting, so reports do not echo
// the full absolute prefix on every row.
func TestCollectArgDists_StripsProjectRootPrefix(t *testing.T) {
	root := t.TempDir()
	jail.SetPolicy(jail.FromConfig(false, root, nil, nil))
	defer jail.SetPolicy(nil)

	subpath := root + "/internal/verbs/find/find.go"
	blob := encodeArgs(t, "grep", map[string]any{"pattern": "Run", "path": subpath})
	calls := []ledger.Call{{Verb: "grep", OK: true, ArgsMsgpack: blob}}

	dists := collectArgDists(map[string][]ledger.Call{"grep": calls}, []string{"grep"})
	if len(dists) == 0 || len(dists[0].Args) == 0 {
		t.Fatal("expected arg dists")
	}
	var pathDist *ArgDist
	for i, d := range dists[0].Args {
		if d.Key == "path" {
			pathDist = &dists[0].Args[i]
			break
		}
	}
	if pathDist == nil {
		t.Fatal("expected path key in arg dist")
	}
	if len(pathDist.Values) != 1 {
		t.Fatalf("expected 1 value, got %d: %v", len(pathDist.Values), pathDist.Values)
	}
	got := pathDist.Values[0].Value
	if strings.HasPrefix(got, root) {
		t.Errorf("arg dist value should not carry the project-root prefix: got %q (root=%s)", got, root)
	}
	// What remains should be the in-repo subpath.
	if got != "internal/verbs/find/find.go" {
		t.Errorf("stripped value: got %q, want %q", got, "internal/verbs/find/find.go")
	}
}

// ASH-71c: decodeArgsSummary (used in truncation hotspots) also strips
// the project-root prefix so the "sample args" line on each hotspot is
// short and readable.
func TestDecodeArgsSummary_StripsProjectRootPrefix(t *testing.T) {
	root := t.TempDir()
	jail.SetPolicy(jail.FromConfig(false, root, nil, nil))
	defer jail.SetPolicy(nil)

	subpath := root + "/cmd/ashd"
	blob := encodeArgs(t, "find", map[string]any{"path": subpath, "glob": "**/*.go"})
	got := decodeArgsSummary(blob)
	if strings.Contains(got, root) {
		t.Errorf("summary should not contain project root: %q (root=%s)", got, root)
	}
	if !strings.Contains(got, "path=cmd/ashd") {
		t.Errorf("expected stripped path=cmd/ashd in summary: %q", got)
	}
}

func TestPrettyResponse_TokPerKiB(t *testing.T) {
	calls := makeCallsWithBytes("read", 2, 200, 1024)
	r := aggregate(calls, Scope{Session: "current"})

	rsp := &proto.Response{OK: true, Data: proto.MustData(r)}
	out := PrettyResponse(nil, rsp)

	if !strings.Contains(out, "token efficiency") {
		t.Errorf("expected token efficiency section:\n%s", out)
	}
	if !strings.Contains(out, "tok/KiB") {
		t.Errorf("expected tok/KiB header:\n%s", out)
	}
	if !strings.Contains(out, "read") {
		t.Errorf("expected verb 'read' in token efficiency section:\n%s", out)
	}
}

func TestPrettyResponse_NoTokPerKiB_WhenZeroBytesOut(t *testing.T) {
	calls := makeCalls("find", 3, 1000, true, false)
	r := aggregate(calls, Scope{Session: "current"})

	rsp := &proto.Response{OK: true, Data: proto.MustData(r)}
	out := PrettyResponse(nil, rsp)

	if strings.Contains(out, "token efficiency") {
		t.Errorf("unexpected token efficiency section when bytes_out=0:\n%s", out)
	}
}

func TestPrettyResponse_ArgDists(t *testing.T) {
	blob := encodeArgs(t, "find", map[string]any{"path": ".", "glob": "**/*.go"})
	calls := []ledger.Call{
		{Verb: "find", OK: true, LatencyExecUs: 100, ArgsMsgpack: blob},
		{Verb: "find", OK: true, LatencyExecUs: 100, ArgsMsgpack: blob},
	}
	r := aggregate(calls, Scope{Session: "current"})

	rsp := &proto.Response{OK: true, Data: proto.MustData(r)}
	out := PrettyResponse(nil, rsp)

	if !strings.Contains(out, "arg distributions") {
		t.Errorf("expected arg distributions section:\n%s", out)
	}
	if !strings.Contains(out, "glob") {
		t.Errorf("expected 'glob' key in arg distributions:\n%s", out)
	}
	if !strings.Contains(out, "**/*.go") {
		t.Errorf("expected '**/*.go' value in arg distributions:\n%s", out)
	}
}

func TestPrettyResponse_NoArgDists_WhenNoMsgpack(t *testing.T) {
	calls := makeCalls("find", 3, 1000, true, false)
	r := aggregate(calls, Scope{Session: "current"})

	rsp := &proto.Response{OK: true, Data: proto.MustData(r)}
	out := PrettyResponse(nil, rsp)

	if strings.Contains(out, "arg distributions") {
		t.Errorf("unexpected arg distributions section when no ArgsMsgpack:\n%s", out)
	}
}

// TestParseArgs_WireShape verifies that the last and top int args accept
// string-typed values (the wire shape from CLI parseFlags) and reject
// garbage. Guards against a future implementation skipping argutil and
// silently breaking the string→int coercion path.
func TestParseArgs_WireShape(t *testing.T) {
	a, perr := ParseArgs(map[string]any{
		"last": "100",
		"top":  "10",
	})
	if perr != nil {
		t.Fatalf("valid string args rejected: %v", perr)
	}
	if a.Last != 100 {
		t.Errorf("last: got %d, want 100", a.Last)
	}
	if a.TopN != 10 {
		t.Errorf("top: got %d, want 10", a.TopN)
	}

	for _, bad := range []struct{ key, val string }{
		{"last", "abc"},
		{"top", "abc"},
	} {
		_, perr := ParseArgs(map[string]any{bad.key: bad.val})
		if perr == nil {
			t.Errorf("expected error for %s=%q", bad.key, bad.val)
		}
	}
}

func TestParseArgs_LastZeroAllowed(t *testing.T) {
	// last=0 means "no row cap" — omitting the flag has the same effect,
	// but explicit 0 must not error (ASH-58).
	a, perr := ParseArgs(map[string]any{"last": 0})
	if perr != nil {
		t.Fatalf("last=0 should be allowed: %v", perr)
	}
	if a.Last != 0 {
		t.Errorf("last: got %d, want 0", a.Last)
	}
}

// -- ASH-162: hook-denial histogram ----------------------------------------

// TestComputeHookDenials covers the happy path: hook rows of mixed tool
// types are grouped by recomputed MatchedRule, ordered by count desc,
// and each entry resolves to the right top-suggested verb.
func TestComputeHookDenials(t *testing.T) {
	mkCall := func(args map[string]any) ledger.Call {
		return ledger.Call{Verb: "hook", OK: true, ArgsMsgpack: encodeArgs(t, "hook", args)}
	}
	calls := []ledger.Call{
		mkCall(map[string]any{"tool": "Bash", "command": "grep foo ."}),
		mkCall(map[string]any{"tool": "Bash", "command": "grep bar src/"}),
		mkCall(map[string]any{"tool": "Bash", "command": "cat /tmp/x"}),
		mkCall(map[string]any{"tool": "Grep", "pattern": "foo", "path": "."}),
		mkCall(map[string]any{"tool": "Bash", "command": "ls /tmp"}), // allow — no rule fires
	}
	entries := computeHookDenials(calls)
	if entries == nil {
		t.Fatal("expected HookDenials entries, got nil")
	}
	wantCounts := map[string]int{
		"Bash:grep": 2,
		"Grep":      1,
		"Bash:cat":  1,
	}
	if len(entries) != len(wantCounts) {
		t.Fatalf("got %d entries, want %d: %+v", len(entries), len(wantCounts), entries)
	}
	for _, e := range entries {
		if e.Count != wantCounts[e.Rule] {
			t.Errorf("rule %q: count %d, want %d", e.Rule, e.Count, wantCounts[e.Rule])
		}
	}
	if entries[0].Rule != "Bash:grep" {
		t.Errorf("first entry should be Bash:grep (count=2), got %q", entries[0].Rule)
	}
	wantVerb := map[string]string{"Bash:grep": "grep", "Grep": "grep", "Bash:cat": "read"}
	for _, e := range entries {
		if e.TopSuggestedVerb != wantVerb[e.Rule] {
			t.Errorf("rule %q: top verb %q, want %q", e.Rule, e.TopSuggestedVerb, wantVerb[e.Rule])
		}
	}
}

// TestComputeHookDenials_EmptyAndAllowsOnly guards two boring inputs:
// no calls, and calls that all recompute to allow. Both must yield nil
// so the renderer suppresses the section entirely.
func TestComputeHookDenials_EmptyAndAllowsOnly(t *testing.T) {
	if computeHookDenials(nil) != nil {
		t.Error("nil calls should return nil")
	}
	allows := []ledger.Call{
		{Verb: "hook", OK: true, ArgsMsgpack: encodeArgs(t, "hook", map[string]any{"tool": "Bash", "command": "ls"})},
		{Verb: "hook", OK: true, ArgsMsgpack: encodeArgs(t, "hook", map[string]any{"tool": "Read", "file": "screenshot.png"})},
	}
	if entries := computeHookDenials(allows); entries != nil {
		t.Errorf("allow-only calls should return nil, got %+v", entries)
	}
}

func TestSuggestedAshVerb(t *testing.T) {
	cases := map[string]string{
		"ash grep --path . --pattern foo":        "grep",
		"ash read --path README.md":              "read",
		"ash write --path x --content - << 'EOF'": "write",
		"":                                        "",
		"grep --path .":                           "",
		"echo ash":                                "",
	}
	for in, want := range cases {
		if got := suggestedAshVerb(in); got != want {
			t.Errorf("suggestedAshVerb(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestPrettyResponse_HookDenials is the end-to-end rendering check: a
// report with hook rows shows the new section with rule, count, and
// ash-verb suggestion, fed straight out of aggregate().
func TestPrettyResponse_HookDenials(t *testing.T) {
	calls := []ledger.Call{
		{Verb: "hook", OK: true, LatencyExecUs: 100, ArgsMsgpack: encodeArgs(t, "hook", map[string]any{"tool": "Bash", "command": "grep foo ."})},
		{Verb: "hook", OK: true, LatencyExecUs: 100, ArgsMsgpack: encodeArgs(t, "hook", map[string]any{"tool": "Bash", "command": "cat /tmp/x"})},
	}
	r := aggregate(calls, Scope{Session: "current"})
	rsp := &proto.Response{OK: true, Data: proto.MustData(r)}
	out := PrettyResponse(nil, rsp)

	for _, want := range []string{"hook denials by rule", "Bash:grep", "Bash:cat", "ash grep", "ash read"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in PrettyResponse:\n%s", want, out)
		}
	}
}

// TestPrettyResponse_NoHookDenials_WhenNoHookCalls guards the silent
// path: a report with no hook rows must omit the section entirely so
// CLI-only / non-hook sessions stay byte-identical to today's output.
func TestPrettyResponse_NoHookDenials_WhenNoHookCalls(t *testing.T) {
	calls := makeCalls("find", 3, 1000, true, false)
	r := aggregate(calls, Scope{Session: "current"})
	rsp := &proto.Response{OK: true, Data: proto.MustData(r)}
	out := PrettyResponse(nil, rsp)
	if strings.Contains(out, "hook denials by rule") {
		t.Errorf("unexpected hook-denials section when no hook calls:\n%s", out)
	}
}
