package bench

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stazelabs/ash/internal/bench"
	testverb "github.com/stazelabs/ash/internal/verbs/test"
)

func TestStripGOMAXPROCS(t *testing.T) {
	cases := []struct{ in, want string }{
		{"BenchmarkFoo-8", "BenchmarkFoo"},
		{"BenchmarkFoo/bar-16", "BenchmarkFoo/bar"},
		{"BenchmarkFoo", "BenchmarkFoo"},                     // no suffix
		{"BenchmarkFoo-not-a-num", "BenchmarkFoo-not-a-num"}, // non-numeric suffix
		{"-8", ""},                         // edge: just the suffix
		{"BenchmarkFoo-", "BenchmarkFoo-"}, // empty after dash
	}
	for _, c := range cases {
		if got := stripGOMAXPROCS(c.in); got != c.want {
			t.Errorf("stripGOMAXPROCS(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCountMicroPackages(t *testing.T) {
	rows := []MicroBenchRow{
		{Package: "cmd/ash"},
		{Package: "cmd/ash"},
		{Package: "internal/walker"},
		{Package: "internal/gitignore"},
		{Package: "internal/walker"},
	}
	if got := countMicroPackages(rows); got != 3 {
		t.Errorf("countMicroPackages: got %d, want 3 (cmd/ash, internal/walker, internal/gitignore)", got)
	}
	if got := countMicroPackages(nil); got != 0 {
		t.Errorf("nil: got %d, want 0", got)
	}
}

// parseMicroRows folds multiple benchmark output lines (one per count
// iteration) into a single averaged row per benchmark name.
func TestParseMicroRows_Aggregation(t *testing.T) {
	tr := &testverb.Result{
		Packages: []testverb.Package{
			{
				BenchOutput: []string{
					"BenchmarkFoo-8\t1000\t200 ns/op\t100 B/op\t1 allocs/op",
					"BenchmarkFoo-8\t1000\t300 ns/op\t100 B/op\t1 allocs/op",
					"BenchmarkBar-8\t500\t1000 ns/op",
					"not-a-benchmark-line",
				},
			},
		},
	}
	rows := parseMicroRows("cmd/ash", tr)
	if len(rows) != 2 {
		t.Fatalf("expected 2 distinct benchmark rows, got %d: %+v", len(rows), rows)
	}
	// First row order matches first-seen order.
	if rows[0].BaseName != "BenchmarkFoo" {
		t.Errorf("first row BaseName: got %q", rows[0].BaseName)
	}
	if rows[0].NsPerOp != 250 {
		t.Errorf("Foo NsPerOp avg: got %v, want 250 (avg of 200, 300)", rows[0].NsPerOp)
	}
	if rows[0].Package != "cmd/ash" {
		t.Errorf("Package: got %q", rows[0].Package)
	}
	if rows[1].BaseName != "BenchmarkBar" {
		t.Errorf("second row: got %q", rows[1].BaseName)
	}
	if rows[1].NsPerOp != 1000 {
		t.Errorf("Bar NsPerOp: got %v, want 1000", rows[1].NsPerOp)
	}
	// Bar has no B/op or allocs/op (plain form) — fields stay 0.
	if rows[1].BPerOp != 0 || rows[1].AllocsPerOp != 0 {
		t.Errorf("plain-form row should have zero B/op and allocs/op: %+v", rows[1])
	}
}

func TestParseMicroRows_Empty(t *testing.T) {
	tr := &testverb.Result{}
	rows := parseMicroRows("x", tr)
	if len(rows) != 0 {
		t.Errorf("empty: got %d rows", len(rows))
	}
}

func TestBuildMicroBaselineFile(t *testing.T) {
	res := &MicroResult{
		BenchTime: "1s",
		Count:     2,
		All: []MicroBenchRow{
			{Package: "z-pkg", BaseName: "BenchmarkA", NsPerOp: 100},
			{Package: "a-pkg", BaseName: "BenchmarkC", NsPerOp: 300},
			{Package: "a-pkg", BaseName: "BenchmarkB", NsPerOp: 200},
		},
	}
	bf := buildMicroBaselineFile(res, bench.Provenance{AshVersion: "v1", RepoSHA: "abc", RepoDirty: true})
	if bf.Schema != microbenchSchemaVersion {
		t.Errorf("Schema: %q", bf.Schema)
	}
	if bf.AshVersion != "v1" || bf.RepoSHA != "abc" || !bf.RepoDirty {
		t.Errorf("provenance not propagated: %+v", bf)
	}
	if bf.BenchTime != "1s" || bf.Count != 2 {
		t.Errorf("run config not propagated: %+v", bf)
	}
	// Sorted by (Package, BaseName).
	if len(bf.Cases) != 3 {
		t.Fatalf("Cases len: %d", len(bf.Cases))
	}
	if bf.Cases[0].Package != "a-pkg" || bf.Cases[0].BaseName != "BenchmarkB" {
		t.Errorf("sort order[0]: %+v", bf.Cases[0])
	}
	if bf.Cases[1].BaseName != "BenchmarkC" {
		t.Errorf("sort order[1]: %+v", bf.Cases[1])
	}
	if bf.Cases[2].Package != "z-pkg" {
		t.Errorf("sort order[2]: %+v", bf.Cases[2])
	}
}

func TestRenderMicroMarkdown(t *testing.T) {
	bf := MicroBaselineFile{
		Schema:     microbenchSchemaVersion,
		Timestamp:  "2026-01-02T00:00:00Z",
		AshVersion: "v9.0.0",
		RepoSHA:    "abc123def456",
		RepoDirty:  true,
		BenchTime:  "1s",
		Count:      3,
		Cases: []MicroBenchRow{
			{Package: "cmd/ash", BaseName: "BenchmarkX", NsPerOp: 100.5, BPerOp: 50, AllocsPerOp: 2},
		},
	}
	md := renderMicroMarkdown(bf)
	for _, want := range []string{
		"# ash bench --micro — 2026-01-02",
		"benchtime: `1s`",
		"count: `3`",
		"ash_version: `v9.0.0`",
		"repo: `abc123de`",
		"(dirty)",
		"| `BenchmarkX` | cmd/ash | 100.5 | 50 | 2 |",
		"1 benchmark(s) across 1 package(s).",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("missing %q\nactual:\n%s", want, md)
		}
	}
}

func TestLoadMicroBaselineFile_RoundTrip(t *testing.T) {
	tmp := t.TempDir()
	dir := filepath.Join(tmp, baselineDirName)
	_ = os.MkdirAll(dir, 0o755)
	want := MicroBaselineFile{
		Schema:    microbenchSchemaVersion,
		Timestamp: "2026-01-02T00:00:00Z",
		BenchTime: "1s",
		Count:     1,
		Cases:     []MicroBenchRow{{Package: "p", BaseName: "BenchmarkX", NsPerOp: 100}},
	}
	data, _ := json.Marshal(want)
	_ = os.WriteFile(filepath.Join(dir, microbenchJSONName), data, 0o644)
	got, err := loadMicroBaselineFile(tmp)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.Schema != want.Schema || got.BenchTime != want.BenchTime || len(got.Cases) != 1 {
		t.Errorf("round-trip: %+v", got)
	}
}

func TestLoadMicroBaselineFile_MissingAndBadSchema(t *testing.T) {
	if _, err := loadMicroBaselineFile(t.TempDir()); err == nil {
		t.Error("expected error for missing file")
	}
	tmp := t.TempDir()
	dir := filepath.Join(tmp, baselineDirName)
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, microbenchJSONName), []byte(`{"schema":"wrong"}`), 0o644)
	_, err := loadMicroBaselineFile(tmp)
	if err == nil || !strings.Contains(err.Error(), "schema") {
		t.Errorf("expected schema mismatch error, got %v", err)
	}
}

func TestParseBenchLine_ScientificNotation(t *testing.T) {
	// extension to existing TestParseBenchLine — the regex supports e+ form.
	row, ok := parseBenchLine("BenchmarkSci-8\t1\t1.23e+03 ns/op")
	if !ok {
		t.Fatal("scientific notation should parse")
	}
	if row.NsPerOp != 1230 {
		t.Errorf("NsPerOp: got %v, want 1230", row.NsPerOp)
	}
}

func TestPrettyMicro_Empty(t *testing.T) {
	out := prettyMicro(&MicroResult{BenchTime: "1s", Count: 1})
	if !strings.Contains(out, "0 benchmark(s)") {
		t.Errorf("zero-count header: %s", out)
	}
	if !strings.Contains(out, "no benchmarks found") {
		t.Errorf("guidance: %s", out)
	}
}

func TestPrettyMicro_WithRows(t *testing.T) {
	r := &MicroResult{
		BenchTime: "1s",
		Count:     1,
		Packages: []MicroPackage{
			{
				Package: "cmd/ash",
				Rows: []MicroBenchRow{
					{Name: "BenchmarkFoo-8", BaseName: "BenchmarkFoo", Package: "cmd/ash", NsPerOp: 200, BPerOp: 64, AllocsPerOp: 1},
				},
			},
			{Package: "internal/broken", Error: "build failed"},
		},
		All: []MicroBenchRow{
			{Name: "BenchmarkFoo-8", BaseName: "BenchmarkFoo", Package: "cmd/ash", NsPerOp: 200},
		},
	}
	out := prettyMicro(r)
	for _, want := range []string{
		"§bench --micro: 1 benchmark(s)",
		"benchmark",    // header line
		"BenchmarkFoo", // body
		"cmd/ash",
		"ERROR internal/broken",
		"build failed",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("prettyMicro missing %q\nactual:\n%s", want, out)
		}
	}
}

func TestPrettyRecordMicro(t *testing.T) {
	r := &RecordMicroResult{
		JSONPath:     "bench/microbench.json",
		MarkdownPath: "bench/microbench.md",
		BytesWritten: 1024,
		Run:          &MicroResult{All: []MicroBenchRow{{}, {}}},
	}
	out := prettyRecordMicro(r)
	for _, want := range []string{
		"§bench --record-micro",
		"wrote bench/microbench.json",
		"wrote bench/microbench.md",
		"1024 bytes total",
		"2 benchmark(s) recorded",
		"review the diff: git diff bench/",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestPrettyDiffMicro_Empty(t *testing.T) {
	out := prettyDiffMicro(&DiffMicroResult{RegressPct: 20, BaseTS: "2026-01-02T00:00:00Z"})
	if !strings.Contains(out, "no benchmarks to compare") {
		t.Errorf("empty diff: %s", out)
	}
}

func TestPrettyDiffMicro_WithRowsAndOnlySets(t *testing.T) {
	r := &DiffMicroResult{
		BaseTS:     "2026-01-02T00:00:00Z",
		RegressPct: 20,
		Rows: []DiffMicroRow{
			{Name: "BenchmarkRegressed", BaseNsPerOp: 100, CurrNsPerOp: 200, NsDeltaPct: 100, Regressed: true},
			{Name: "BenchmarkSteady", BaseNsPerOp: 100, CurrNsPerOp: 105, NsDeltaPct: 5},
		},
		NewOnly:  []string{"BenchmarkNew/sub", "BenchmarkNew/other"},
		BaseOnly: []string{"BenchmarkGone"},
	}
	out := prettyDiffMicro(r)
	for _, want := range []string{
		"§bench --diff-micro: vs baseline 2026-01-02 (regress_pct=20%)",
		"REGRESS",
		"BenchmarkRegressed",
		"BenchmarkSteady",
		"new (not in baseline):",
		"BenchmarkNew/sub",
		"removed (only in baseline):",
		"BenchmarkGone",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}
