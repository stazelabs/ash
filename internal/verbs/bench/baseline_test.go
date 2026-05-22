package bench

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stazelabs/ash/internal/bench"
	"github.com/stazelabs/ash/internal/ledger"
)

// buildBaselineFile is the regression-gate machinery. Edge cases on
// delta math (zero bash, zero ash + zero bash, normal) are the
// embarrassing bugs we'd notice late. Pin them.
func TestBuildBaselineFile_Normal(t *testing.T) {
	res := &Result{
		Cases: []CaseResult{
			{Name: "b_case", Verb: "find", AshTokens: 80, BashTokens: 100},
			{Name: "a_case", Verb: "grep", AshTokens: 60, BashTokens: 50, BashTruncated: true},
		},
	}
	prov := bench.Provenance{AshVersion: "v9.9.9", CaseSetVersion: "cs-deadbeef00000000"}

	bf := buildBaselineFile(res, prov)
	if bf.Schema != baselineSchemaVersion {
		t.Errorf("Schema: got %q", bf.Schema)
	}
	if bf.AshVersion != "v9.9.9" {
		t.Errorf("AshVersion not propagated: %q", bf.AshVersion)
	}
	if bf.CaseSetVersion != "cs-deadbeef00000000" {
		t.Errorf("CaseSetVersion not propagated: %q", bf.CaseSetVersion)
	}
	// Cases sorted alphabetically by Name.
	if len(bf.Cases) != 2 || bf.Cases[0].Name != "a_case" || bf.Cases[1].Name != "b_case" {
		t.Errorf("cases not sorted: %+v", bf.Cases)
	}
	// BashTruncated should propagate.
	if !bf.Cases[0].BashTruncated {
		t.Errorf("BashTruncated lost: %+v", bf.Cases[0])
	}
	if bf.Summary.NCases != 2 {
		t.Errorf("NCases: got %d, want 2", bf.Summary.NCases)
	}
	if bf.Summary.AshTokensTotal != 140 || bf.Summary.BashTokensTotal != 150 {
		t.Errorf("Totals: %+v", bf.Summary)
	}
	// (140-150)/150 * 100 = -6.6...%
	want := (140.0 - 150.0) / 150.0 * 100
	if bf.Summary.DeltaTokPct != want {
		t.Errorf("DeltaTokPct: got %v, want %v", bf.Summary.DeltaTokPct, want)
	}
}

// Zero bash on either side must NOT produce Inf/NaN — the published
// baseline file would then carry an unrenderable JSON number. The
// runner pins zero-bash to 0% via pctChange's zero handling.
func TestBuildBaselineFile_ZeroBashStaysZero(t *testing.T) {
	res := &Result{
		Cases: []CaseResult{
			{Name: "x", Verb: "stat", AshTokens: 100, BashTokens: 0},
		},
	}
	bf := buildBaselineFile(res, bench.Provenance{})
	if bf.Summary.BashTokensTotal != 0 {
		t.Fatalf("setup: BashTokensTotal should be 0, got %d", bf.Summary.BashTokensTotal)
	}
	if bf.Summary.DeltaTokPct != 0 {
		t.Errorf("zero bash should produce DeltaTokPct=0 (not Inf/NaN): got %v", bf.Summary.DeltaTokPct)
	}
}

func TestBuildBaselineFile_ZeroAshZeroBash(t *testing.T) {
	res := &Result{
		Cases: []CaseResult{{Name: "x", Verb: "stat", AshTokens: 0, BashTokens: 0}},
	}
	bf := buildBaselineFile(res, bench.Provenance{})
	if bf.Summary.DeltaTokPct != 0 {
		t.Errorf("both zero should produce DeltaTokPct=0: got %v", bf.Summary.DeltaTokPct)
	}
}

func TestBuildBaselineFile_EmptyResult(t *testing.T) {
	bf := buildBaselineFile(&Result{}, bench.Provenance{})
	if bf.Summary.NCases != 0 {
		t.Errorf("empty: NCases should be 0, got %d", bf.Summary.NCases)
	}
	if bf.Summary.DeltaTokPct != 0 {
		t.Errorf("empty: DeltaTokPct should be 0, got %v", bf.Summary.DeltaTokPct)
	}
}

func TestBuildBaselineFileFromLedger_Normal(t *testing.T) {
	run := ledger.BenchRun{
		Timestamp:    time.Date(2026, 1, 2, 15, 4, 5, 0, time.UTC),
		AshVersion:   "v9.0.0",
		AshCommitSHA: "abc123",
		RepoDirty:    true,
	}
	cases := []ledger.BenchCaseResult{
		{CaseName: "z", Verb: "find", AshTokens: 200, BashTokens: 400},
		{CaseName: "a", Verb: "grep", AshTokens: 100, BashTokens: 100, AshTruncated: true},
	}
	bf := buildBaselineFileFromLedger(run, cases)
	if bf.Timestamp != "2026-01-02T15:04:05Z" {
		t.Errorf("Timestamp: got %q", bf.Timestamp)
	}
	if !bf.RepoDirty {
		t.Error("RepoDirty not propagated")
	}
	if len(bf.Cases) != 2 || bf.Cases[0].Name != "a" || bf.Cases[1].Name != "z" {
		t.Errorf("cases not sorted: %+v", bf.Cases)
	}
	if !bf.Cases[0].AshTruncated {
		t.Errorf("AshTruncated lost: %+v", bf.Cases[0])
	}
	if bf.Summary.AshTokensTotal != 300 || bf.Summary.BashTokensTotal != 500 {
		t.Errorf("Totals: %+v", bf.Summary)
	}
	want := (300.0 - 500.0) / 500.0 * 100
	if bf.Summary.DeltaTokPct != want {
		t.Errorf("DeltaTokPct: got %v, want %v", bf.Summary.DeltaTokPct, want)
	}
}

func TestBuildBaselineFileFromLedger_ZeroBashStaysZero(t *testing.T) {
	bf := buildBaselineFileFromLedger(ledger.BenchRun{}, []ledger.BenchCaseResult{
		{CaseName: "x", AshTokens: 10, BashTokens: 0},
	})
	if bf.Summary.DeltaTokPct != 0 {
		t.Errorf("zero bash: got DeltaTokPct=%v, want 0", bf.Summary.DeltaTokPct)
	}
}

func TestBuildLatencySnapshot(t *testing.T) {
	res := &Result{
		Cases: []CaseResult{
			{Name: "b", Verb: "find", AshLatencyUsP50: 200, AshLatencyUsMin: 150, BashLatencyUsP50: 400, BashLatencyUsMin: 350},
			{Name: "a", Verb: "grep", AshLatencyUsP50: 100, AshLatencyUsMin: 80, BashLatencyUsP50: 300, BashLatencyUsMin: 250},
		},
	}
	prov := bench.Provenance{Platform: "darwin/arm64", CPUCount: 10}
	lf := buildLatencySnapshot(res, prov, &Args{Repeat: 5, Warmup: 2})
	if lf.Schema != latencySnapshotSchemaVersion {
		t.Errorf("Schema: got %q", lf.Schema)
	}
	if lf.Platform != "darwin/arm64" || lf.CPUCount != 10 {
		t.Errorf("Provenance not propagated: %+v", lf)
	}
	if lf.RepeatN != 5 || lf.WarmupN != 2 {
		t.Errorf("Repeat/Warmup not propagated: %+v", lf)
	}
	// Sorted by name.
	if len(lf.Cases) != 2 || lf.Cases[0].Name != "a" || lf.Cases[1].Name != "b" {
		t.Errorf("cases not sorted: %+v", lf.Cases)
	}
	if lf.Cases[1].AshUsP50 != 200 || lf.Cases[1].BashUsMin != 350 {
		t.Errorf("per-case values: %+v", lf.Cases[1])
	}
}

// Repeat<1 must still produce RepeatN>=1 — maxInt(a.Repeat, 1) is the
// guard. Without it the snapshot file would say "repeat=0" which is
// nonsense for a measurement that did happen.
func TestBuildLatencySnapshot_RepeatLessThanOne(t *testing.T) {
	lf := buildLatencySnapshot(&Result{}, bench.Provenance{}, &Args{Repeat: 0})
	if lf.RepeatN != 1 {
		t.Errorf("RepeatN: got %d, want 1 (maxInt guard)", lf.RepeatN)
	}
}

func TestBuildLatencySnapshotFromLedger(t *testing.T) {
	run := ledger.BenchRun{
		Timestamp: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
		Platform:  "linux/amd64",
		CPUCount:  4,
		RepeatN:   3,
		WarmupN:   1,
	}
	cases := []ledger.BenchCaseResult{
		{CaseName: "x", Verb: "find", AshLatencyUsP50: 10, BashLatencyUsP50: 20},
	}
	lf := buildLatencySnapshotFromLedger(run, cases)
	if lf.Platform != "linux/amd64" || lf.CPUCount != 4 || lf.RepeatN != 3 || lf.WarmupN != 1 {
		t.Errorf("ledger fields not propagated: %+v", lf)
	}
	if len(lf.Cases) != 1 || lf.Cases[0].Name != "x" {
		t.Errorf("cases: %+v", lf.Cases)
	}
}

// Markdown rendering — load-bearing for the bench/baseline.md commit
// artifact. Pin field presence; exact whitespace is brittle to test.
func TestRenderBaselineMarkdown_ContainsAllFields(t *testing.T) {
	bf := BaselineFile{
		Schema:         baselineSchemaVersion,
		Timestamp:      "2026-01-02T00:00:00Z",
		AshVersion:     "v9.0.0",
		AshCommitSHA:   "abc123def4567890",
		CaseSetVersion: "cs-feedface00000000",
		RepoSHA:        "112233445566778899",
		RepoDirty:      true,
		Cases: []BaselineCase{
			{Name: "c1", Verb: "find", AshTokens: 100, BashTokens: 200, BashTruncated: true},
			{Name: "c2", Verb: "grep", AshTokens: 50, BashTokens: 0, AshTruncated: true}, // zero-bash row → 0%
			{Name: "c3", Verb: "stat", AshTokens: 75, BashTokens: 50},
		},
		Summary: BaselineSummary{
			NCases: 3, AshTokensTotal: 225, BashTokensTotal: 250, DeltaTokPct: -10.0,
		},
	}
	lf := LatencySnapshotFile{
		Platform: "darwin/arm64", CPUCount: 10, RepeatN: 5, WarmupN: 2,
		Cases: []LatencyCase{
			{Name: "c1", AshUsP50: 100, AshUsMin: 80, BashUsP50: 200, BashUsMin: 180},
		},
	}
	md := renderBaselineMarkdown(bf, lf)
	for _, want := range []string{
		"# ash bench — baseline 2026-01-02",
		"ash_version: `v9.0.0`",
		"ash_commit: `abc123de`", // shortRunID truncates to 8 chars
		"case_set: `cs-feedface00000000`",
		"repo: `11223344`",
		"(dirty)",
		"| `c1` | find | 100 | 200 |",
		"bash",                     // truncation column for c1
		"| `c2` | grep | 50 | 0 |", // zero-bash row stays in table
		"+0%",                      // c2's dpct should be the +0% inline guard
		"3 cases, ash 225 tok, bash 250 tok",
		"-10.0%",
		"darwin/arm64",
		"10 CPUs", // CPU count from the fixture above
		"repeat=5, warmup=2",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("rendered markdown missing %q\nactual:\n%s", want, md)
		}
	}
}

func TestRenderBaselineMarkdown_NoLatencySection(t *testing.T) {
	bf := BaselineFile{
		Schema:    baselineSchemaVersion,
		Timestamp: "2026-01-02T00:00:00Z",
	}
	md := renderBaselineMarkdown(bf, LatencySnapshotFile{})
	if strings.Contains(md, "Latency") {
		t.Errorf("empty latency-snapshot should not produce a latency section\n%s", md)
	}
}

// loadBaselineFile — round-trip with a real on-disk file, plus the
// failure modes a reviewer would actually hit (missing file, bad JSON,
// schema mismatch).
func TestLoadBaselineFile_RoundTrip(t *testing.T) {
	tmp := t.TempDir()
	dir := filepath.Join(tmp, baselineDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	want := BaselineFile{
		Schema:         baselineSchemaVersion,
		Timestamp:      "2026-01-02T00:00:00Z",
		AshVersion:     "v1.0",
		CaseSetVersion: "cs-12345678",
		Cases: []BaselineCase{
			{Name: "x", Verb: "find", AshTokens: 10, BashTokens: 20},
		},
		Summary: BaselineSummary{NCases: 1, AshTokensTotal: 10, BashTokensTotal: 20, DeltaTokPct: -50.0},
	}
	data, _ := json.MarshalIndent(want, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, baselineJSONName), data, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := loadBaselineFile(tmp)
	if err != nil {
		t.Fatalf("loadBaselineFile: %v", err)
	}
	if got.AshVersion != want.AshVersion || got.Summary.DeltaTokPct != want.Summary.DeltaTokPct {
		t.Errorf("round-trip differs: got %+v, want %+v", got, want)
	}
}

func TestLoadBaselineFile_MissingFile(t *testing.T) {
	if _, err := loadBaselineFile(t.TempDir()); err == nil {
		t.Error("expected error for missing bench/baseline.json")
	}
}

func TestLoadBaselineFile_BadJSON(t *testing.T) {
	tmp := t.TempDir()
	dir := filepath.Join(tmp, baselineDirName)
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, baselineJSONName), []byte("{not valid"), 0o644)
	if _, err := loadBaselineFile(tmp); err == nil {
		t.Error("expected JSON unmarshal error")
	}
}

func TestLoadBaselineFile_SchemaMismatch(t *testing.T) {
	tmp := t.TempDir()
	dir := filepath.Join(tmp, baselineDirName)
	_ = os.MkdirAll(dir, 0o755)
	data, _ := json.Marshal(BaselineFile{Schema: "ash-bench-baseline-v999"})
	_ = os.WriteFile(filepath.Join(dir, baselineJSONName), data, 0o644)
	_, err := loadBaselineFile(tmp)
	if err == nil || !strings.Contains(err.Error(), "schema") {
		t.Errorf("expected schema-mismatch error, got %v", err)
	}
}

func TestLoadBaselineFile_EmptyProjectRoot(t *testing.T) {
	if _, err := loadBaselineFile(""); err == nil {
		t.Error("empty projectRoot should error")
	}
}

func TestBaselineToRunSummary_RoundTrip(t *testing.T) {
	bf := &BaselineFile{
		Timestamp:      "2026-01-02T15:04:05Z",
		AshVersion:     "v1.0",
		AshCommitSHA:   "abc",
		CaseSetVersion: "cs-x",
		RepoSHA:        "rrr",
		RepoDirty:      true,
		Cases: []BaselineCase{
			{Name: "x", Verb: "find", AshTokens: 10, BashTokens: 20, AshTruncated: true},
		},
		Summary: BaselineSummary{NCases: 1, AshTokensTotal: 10, BashTokensTotal: 20, DeltaTokPct: -50},
	}
	rs, rows := baselineToRunSummary(bf)
	if rs.RunUUID != "baseline" {
		t.Errorf("RunUUID: got %q, want 'baseline'", rs.RunUUID)
	}
	if rs.AshVersion != "v1.0" || rs.AshCommitSHA != "abc" || rs.CaseSetVersion != "cs-x" {
		t.Errorf("metadata not propagated: %+v", rs)
	}
	if !rs.RepoDirty {
		t.Error("RepoDirty not propagated")
	}
	if rs.Cases != 1 || rs.AshTokensTotal != 10 || rs.BashTokensTotal != 20 || rs.DeltaTokPct != -50 {
		t.Errorf("summary fields: %+v", rs)
	}
	wantTime := time.Date(2026, 1, 2, 15, 4, 5, 0, time.UTC)
	if !rs.Timestamp.Equal(wantTime) {
		t.Errorf("Timestamp: got %v, want %v", rs.Timestamp, wantTime)
	}
	if len(rows) != 1 || rows[0].CaseName != "x" || !rows[0].AshTruncated {
		t.Errorf("rows: %+v", rows)
	}
}

func TestBaselineToRunSummary_BadTimestamp(t *testing.T) {
	bf := &BaselineFile{Timestamp: "not-a-date"}
	rs, _ := baselineToRunSummary(bf)
	if !rs.Timestamp.IsZero() {
		t.Errorf("bad timestamp should leave Timestamp zero, got %v", rs.Timestamp)
	}
}

func TestPrettyRecord(t *testing.T) {
	r := &RecordBaselineResult{
		BaselinePath: "bench/baseline.json",
		MarkdownPath: "bench/baseline.md",
		LatencyPath:  "bench/latency-snapshot.json",
		BytesWritten: 12345,
		Run: &Result{
			Overall: VerbSummary{Cases: 3, AshTokensTotal: 100, BashTokensTotal: 200},
		},
	}
	out := prettyRecord(r)
	for _, want := range []string{
		"§bench --record-baseline",
		"wrote bench/baseline.json",
		"wrote bench/baseline.md",
		"wrote bench/latency-snapshot.json",
		"12345 bytes total",
		"3 cases, ash 100 tok, bash 200 tok",
		"-50.0%",
		"review the diff: git diff bench/",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("prettyRecord missing %q\nactual:\n%s", want, out)
		}
	}
}

func TestPrettyRecord_ZeroBashSuppressesDelta(t *testing.T) {
	r := &RecordBaselineResult{
		Run: &Result{Overall: VerbSummary{Cases: 1, AshTokensTotal: 10, BashTokensTotal: 0}},
	}
	out := prettyRecord(r)
	if strings.Contains(out, "Δtok%") {
		t.Errorf("zero bash should suppress Δtok line: %s", out)
	}
}

func TestRelToProjectRoot(t *testing.T) {
	// Inside the root → relative path.
	root := "/abs/repo"
	if got := relToProjectRoot(root, "/abs/repo/bench/x.json"); got != "bench/x.json" {
		t.Errorf("inside-root: got %q, want bench/x.json", got)
	}
	// Outside the root → filepath.Rel returns "../something", which is
	// still a relative form, so we return it as-is.
	out := relToProjectRoot(root, "/abs/other/x.json")
	if out == "" || filepath.IsAbs(out) {
		t.Errorf("outside-root: unexpected %q", out)
	}
}
