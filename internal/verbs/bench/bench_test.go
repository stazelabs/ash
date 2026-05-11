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
		line        string
		wantOK      bool
		wantBase    string
		wantNs      float64
		wantAllocs  float64
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
		"micro":          "true",
		"record_micro":   "false",
		"micro_count":    "3",
		"micro_benchtime": "2s",
		"micro_packages": "cmd/ash,internal/walker",
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
