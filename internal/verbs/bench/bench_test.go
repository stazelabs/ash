package bench

import "testing"

// TestParseArgs_WireShape verifies that the limit int arg accepts a
// string-typed value (the wire shape from CLI parseFlags) and rejects
// garbage. Guards against a future implementation skipping argutil and
// silently breaking the string→int coercion path.
func TestParseArgs_WireShape(t *testing.T) {
	a, perr := ParseArgs(map[string]any{"limit": "5"})
	if perr != nil {
		t.Fatalf("string limit rejected: %v", perr)
	}
	if a.Limit != 5 {
		t.Errorf("limit: got %d, want 5", a.Limit)
	}
	_, perr = ParseArgs(map[string]any{"limit": "abc"})
	if perr == nil {
		t.Error("expected error for limit=abc")
	}
}

// TestParseBenchLine verifies the benchmark output parser handles both the
// plain form (ns/op only) and the -benchmem form (ns/op + B/op + allocs/op),
// and rejects non-benchmark lines.
func TestParseBenchLine(t *testing.T) {
	cases := []struct {
		line       string
		wantOK     bool
		wantBase   string
		wantNs     float64
		wantAllocs float64
	}{
		{
			line:       "BenchmarkHookDecide/deny_grep-8\t5614714\t213.8 ns/op\t0 B/op\t0 allocs/op",
			wantOK:     true,
			wantBase:   "BenchmarkHookDecide/deny_grep",
			wantNs:     213.8,
			wantAllocs: 0,
		},
		{
			line:       "BenchmarkWalkRepo_NoGlob-8\t1000\t610708 ns/op\t96656 B/op\t1037 allocs/op",
			wantOK:     true,
			wantBase:   "BenchmarkWalkRepo_NoGlob",
			wantNs:     610708,
			wantAllocs: 1037,
		},
		{
			// plain form (no -benchmem)
			line:     "BenchmarkFoo-4\t1000000\t142 ns/op",
			wantOK:   true,
			wantBase: "BenchmarkFoo",
			wantNs:   142,
		},
		{line: "goos: darwin", wantOK: false},
		{line: "ok  github.com/foo/bar  1.234s", wantOK: false},
		{line: "", wantOK: false},
	}
	for _, tc := range cases {
		row, ok := parseBenchLine(tc.line)
		if ok != tc.wantOK {
			t.Errorf("line %q: ok=%v, want %v", tc.line, ok, tc.wantOK)
			continue
		}
		if !ok {
			continue
		}
		if row.BaseName != tc.wantBase {
			t.Errorf("line %q: BaseName=%q, want %q", tc.line, row.BaseName, tc.wantBase)
		}
		if row.NsPerOp != tc.wantNs {
			t.Errorf("line %q: NsPerOp=%v, want %v", tc.line, row.NsPerOp, tc.wantNs)
		}
		if row.AllocsPerOp != tc.wantAllocs {
			t.Errorf("line %q: AllocsPerOp=%v, want %v", tc.line, row.AllocsPerOp, tc.wantAllocs)
		}
	}
}

// TestParseArgs_MicroFlags verifies that micro-related bool and string args
// accept wire-shape inputs from the CLI parseFlags path.
func TestParseArgs_MicroFlags(t *testing.T) {
	a, perr := ParseArgs(map[string]any{
		"micro":           "true",
		"record_micro":    "false",
		"micro_count":     "3",
		"micro_benchtime": "2s",
		"micro_packages":  "cmd/ash,internal/walker",
	})
	if perr != nil {
		t.Fatalf("parse failed: %v", perr)
	}
	if !a.Micro {
		t.Error("Micro should be true")
	}
	if a.MicroCount != 3 {
		t.Errorf("MicroCount: got %d, want 3", a.MicroCount)
	}
	if a.MicroBenchTime != "2s" {
		t.Errorf("MicroBenchTime: got %q, want %q", a.MicroBenchTime, "2s")
	}
	if a.MicroPackages != "cmd/ash,internal/walker" {
		t.Errorf("MicroPackages: got %q", a.MicroPackages)
	}
}

func TestParseArgs_WarmupDefaults(t *testing.T) {
	cases := []struct {
		name string
		in   map[string]any
		want int
	}{
		// Implicit warmup. Default = 1 when Repeat>1, else 0.
		{name: "repeat_1_no_warmup", in: map[string]any{"repeat": "1"}, want: 0},
		{name: "repeat_3_implies_warmup_1", in: map[string]any{"repeat": "3"}, want: 1},
		{name: "default_repeat_no_warmup", in: map[string]any{}, want: 0},
		// Explicit warmup wins, regardless of repeat.
		{name: "explicit_warmup_0_with_high_repeat", in: map[string]any{"repeat": "5", "warmup": "0"}, want: 0},
		{name: "explicit_warmup_5_with_repeat_1", in: map[string]any{"repeat": "1", "warmup": "5"}, want: 5},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a, perr := ParseArgs(c.in)
			if perr != nil {
				t.Fatalf("ParseArgs: %v", perr)
			}
			if a.Warmup != c.want {
				t.Errorf("Warmup: got %d, want %d", a.Warmup, c.want)
			}
		})
	}
}

func TestParseArgs_RepeatZeroDefaultsToOne(t *testing.T) {
	a, perr := ParseArgs(map[string]any{"repeat": "0"})
	if perr != nil {
		t.Fatalf("ParseArgs: %v", perr)
	}
	if a.Repeat != 1 {
		t.Errorf("Repeat: got %d, want 1 (zero coerces to 1)", a.Repeat)
	}
}

func TestParseArgs_CompareConvenienceForm(t *testing.T) {
	a, perr := ParseArgs(map[string]any{"compare": "abc,def"})
	if perr != nil {
		t.Fatalf("ParseArgs: %v", perr)
	}
	if a.CompareA != "abc" || a.CompareB != "def" {
		t.Errorf("compare split wrong: a=%q b=%q", a.CompareA, a.CompareB)
	}
}

func TestParseArgs_CompareConvenienceFormErrors(t *testing.T) {
	bad := []string{"only-one", "a,", ",b", ","}
	for _, in := range bad {
		_, perr := ParseArgs(map[string]any{"compare": in})
		if perr == nil || perr.Code != "args" {
			t.Errorf("compare=%q: expected args error, got %v", in, perr)
		}
	}
}

func TestParseArgs_RegressThresholds(t *testing.T) {
	a, perr := ParseArgs(map[string]any{})
	if perr != nil {
		t.Fatalf("ParseArgs: %v", perr)
	}
	if a.RegressTokPct != 10 || a.RegressLatPct != 20 {
		t.Errorf("default thresholds: tok=%d, lat=%d", a.RegressTokPct, a.RegressLatPct)
	}
	a, perr = ParseArgs(map[string]any{"regress_tokens": "25", "regress_latency": "50"})
	if perr != nil {
		t.Fatalf("ParseArgs: %v", perr)
	}
	if a.RegressTokPct != 25 || a.RegressLatPct != 50 {
		t.Errorf("override thresholds: tok=%d, lat=%d", a.RegressTokPct, a.RegressLatPct)
	}
}

func TestParseArgs_BaselineAndPublishFlags(t *testing.T) {
	a, perr := ParseArgs(map[string]any{
		"baseline":        "7d",
		"record_baseline": "true",
		"export_md":       "true",
	})
	if perr != nil {
		t.Fatalf("ParseArgs: %v", perr)
	}
	if a.Baseline != "7d" {
		t.Errorf("Baseline: got %q", a.Baseline)
	}
	if !a.RecordBaseline {
		t.Error("RecordBaseline: expected true")
	}
	if !a.ExportMd {
		t.Error("ExportMd: expected true")
	}
}

func TestParseArgs_ListAndListLimit(t *testing.T) {
	a, perr := ParseArgs(map[string]any{})
	if perr != nil {
		t.Fatalf("ParseArgs: %v", perr)
	}
	if a.ListLimit != 20 {
		t.Errorf("default ListLimit: got %d, want 20", a.ListLimit)
	}
	a, perr = ParseArgs(map[string]any{"list": "true", "list_limit": "5"})
	if perr != nil {
		t.Fatalf("ParseArgs: %v", perr)
	}
	if !a.List || a.ListLimit != 5 {
		t.Errorf("list flags: %+v", a)
	}
}

func TestParseArgs_VerbAndCaseFilters(t *testing.T) {
	a, perr := ParseArgs(map[string]any{"verb": "find", "case": "find_shallow"})
	if perr != nil {
		t.Fatalf("ParseArgs: %v", perr)
	}
	if a.Verb != "find" || a.Case != "find_shallow" {
		t.Errorf("filters: %+v", a)
	}
}

func TestParseArgs_MicroCountZeroDefaultsToOne(t *testing.T) {
	a, perr := ParseArgs(map[string]any{"micro_count": "0"})
	if perr != nil {
		t.Fatalf("ParseArgs: %v", perr)
	}
	if a.MicroCount != 1 {
		t.Errorf("MicroCount: got %d, want 1", a.MicroCount)
	}
}
