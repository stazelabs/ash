package bench

// Phase 5: publishable baseline.
//
// `ash bench --record-baseline` runs a fresh bench then writes three
// files under <projectRoot>/bench/:
//
//   bench/baseline.json          — tokens-only, the regression contract
//   bench/baseline.md            — human-readable rendering
//   bench/latency-snapshot.json  — latency, machine-tagged, informational
//
// `ash bench --export-md` renders the latest persisted run as Markdown
// (no fresh bench), suitable for piping into bench/baseline.md.
//
// `ash bench --compare baseline,latest` reads bench/baseline.json (the
// "baseline" token resolution lives here too).

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/stazelabs/ash/internal/bench"
	"github.com/stazelabs/ash/internal/ledger"
	"github.com/stazelabs/ash/internal/proto"
)

const (
	baselineSchemaVersion        = "ash-bench-baseline-v1"
	latencySnapshotSchemaVersion = "ash-bench-latency-snapshot-v1"

	baselineDirName        = "bench"
	baselineJSONName       = "baseline.json"
	baselineMDName         = "baseline.md"
	latencySnapshotJSONName = "latency-snapshot.json"

	kindRecord = "record_baseline"
	kindExport = "export_md"
)

// BaselineCase is one row in the published baseline.json. Tokens only.
type BaselineCase struct {
	Name          string `json:"name"`
	Verb          string `json:"verb"`
	AshTokens     int    `json:"ash_tokens"`
	BashTokens    int    `json:"bash_tokens"`
	AshTruncated  bool   `json:"ash_truncated,omitempty"`
	BashTruncated bool   `json:"bash_truncated,omitempty"`
}

// BaselineSummary is the top-level summary object in baseline.json.
type BaselineSummary struct {
	NCases          int     `json:"n_cases"`
	AshTokensTotal  int     `json:"ash_tokens_total"`
	BashTokensTotal int     `json:"bash_tokens_total"`
	DeltaTokPct     float64 `json:"delta_tok_pct"`
}

// BaselineFile is the serialized form of bench/baseline.json.
type BaselineFile struct {
	Schema         string          `json:"schema"`
	Timestamp      string          `json:"ts"` // RFC3339 for human readability
	AshVersion     string          `json:"ash_version"`
	AshCommitSHA   string          `json:"ash_commit_sha,omitempty"`
	CaseSetVersion string          `json:"case_set_version"`
	RepoSHA        string          `json:"repo_sha,omitempty"`
	RepoDirty      bool            `json:"repo_dirty,omitempty"`
	Cases          []BaselineCase  `json:"cases"`
	Summary        BaselineSummary `json:"summary"`
}

// LatencyCase is one row in latency-snapshot.json.
type LatencyCase struct {
	Name        string `json:"name"`
	Verb        string `json:"verb"`
	AshUsP50    int64  `json:"ash_us_p50"`
	AshUsMin    int64  `json:"ash_us_min"`
	BashUsP50   int64  `json:"bash_us_p50"`
	BashUsMin   int64  `json:"bash_us_min"`
}

// LatencySnapshotFile is the serialized form of latency-snapshot.json.
// Latency is machine-dependent and intentionally not the regression
// contract; this file is informational.
type LatencySnapshotFile struct {
	Schema    string        `json:"schema"`
	Timestamp string        `json:"ts"`
	Platform  string        `json:"platform,omitempty"`
	CPUCount  int           `json:"cpu_count,omitempty"`
	RepeatN   int           `json:"repeat_n"`
	WarmupN   int           `json:"warmup_n"`
	Cases     []LatencyCase `json:"cases"`
}

// RecordBaselineResult is the response shape for --record-baseline. It
// surfaces the file paths written + the same summary the user would
// see from a regular bench run, so the immediate output isn't silent.
type RecordBaselineResult struct {
	Kind          string         `msgpack:"kind"` // always "record_baseline"
	BaselinePath  string         `msgpack:"baseline_path"`
	MarkdownPath  string         `msgpack:"markdown_path"`
	LatencyPath   string         `msgpack:"latency_path"`
	BytesWritten  int            `msgpack:"bytes_written"`
	Run           *Result        `msgpack:"run"`
}

// ExportMdResult is the response shape for --export-md. The Body is
// the rendered markdown; the pretty form just prints it verbatim.
type ExportMdResult struct {
	Kind string `msgpack:"kind"` // always "export_md"
	Body string `msgpack:"body"`
}

// runRecordBaseline runs a fresh bench then writes the three baseline
// files into <ProjectRoot>/bench/. Returns paths + the run result so
// the caller can sanity-check tokens before reviewing the diff.
func runRecordBaseline(d Deps, a *Args) (*RecordBaselineResult, *proto.Error) {
	if d.ProjectRoot == "" {
		return nil, &proto.Error{Code: "config", Msg: "record_baseline: project_root not wired"}
	}

	// Run the fresh bench via the standard path.
	freshArgs := *a
	freshArgs.RecordBaseline = false // avoid recursion
	freshArgs.ExportMd = false
	res, perr := runStandard(d, &freshArgs)
	if perr != nil {
		return nil, perr
	}

	prov := bench.CaptureProvenance(d.DaemonStart, d.ProjectRoot)
	bf := buildBaselineFile(res, prov)
	lf := buildLatencySnapshot(res, prov, a)

	dir := filepath.Join(d.ProjectRoot, baselineDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, &proto.Error{Code: "io", Msg: "mkdir bench/: " + err.Error()}
	}

	bjPath := filepath.Join(dir, baselineJSONName)
	bmPath := filepath.Join(dir, baselineMDName)
	lsPath := filepath.Join(dir, latencySnapshotJSONName)

	bjBytes, _ := json.MarshalIndent(bf, "", "  ")
	bjBytes = append(bjBytes, '\n')
	if err := os.WriteFile(bjPath, bjBytes, 0o644); err != nil {
		return nil, &proto.Error{Code: "io", Msg: "write baseline.json: " + err.Error()}
	}
	mdBody := renderBaselineMarkdown(bf, lf)
	if err := os.WriteFile(bmPath, []byte(mdBody), 0o644); err != nil {
		return nil, &proto.Error{Code: "io", Msg: "write baseline.md: " + err.Error()}
	}
	lsBytes, _ := json.MarshalIndent(lf, "", "  ")
	lsBytes = append(lsBytes, '\n')
	if err := os.WriteFile(lsPath, lsBytes, 0o644); err != nil {
		return nil, &proto.Error{Code: "io", Msg: "write latency-snapshot.json: " + err.Error()}
	}

	return &RecordBaselineResult{
		Kind:         kindRecord,
		BaselinePath: relToProjectRoot(d.ProjectRoot, bjPath),
		MarkdownPath: relToProjectRoot(d.ProjectRoot, bmPath),
		LatencyPath:  relToProjectRoot(d.ProjectRoot, lsPath),
		BytesWritten: len(bjBytes) + len(mdBody) + len(lsBytes),
		Run:          res,
	}, nil
}

// runExportMd queries the latest persisted run (via the existing
// trend-side helpers) and returns the markdown rendering. Does not
// write to disk — the caller pipes the response body to a file.
func runExportMd(d Deps, _ *Args) (*ExportMdResult, *proto.Error) {
	if d.Ledger == nil {
		return nil, &proto.Error{Code: "config", Msg: "export_md: ledger not wired"}
	}
	runs, err := d.Ledger.QueryBenchRuns(1)
	if err != nil {
		return nil, &proto.Error{Code: "ledger", Msg: err.Error()}
	}
	if len(runs) == 0 {
		return nil, &proto.Error{Code: "no_runs", Msg: "no bench runs persisted", Hint: "run 'ash bench' first"}
	}
	run, cases, err := d.Ledger.QueryBenchRun(runs[0].RunUUID)
	if err != nil {
		return nil, &proto.Error{Code: "ledger", Msg: err.Error()}
	}
	bf := buildBaselineFileFromLedger(*run, cases)
	lf := buildLatencySnapshotFromLedger(*run, cases)
	return &ExportMdResult{Kind: kindExport, Body: renderBaselineMarkdown(bf, lf)}, nil
}

// buildBaselineFile transforms a fresh in-memory Result into the
// schema-stable BaselineFile.
func buildBaselineFile(res *Result, prov bench.Provenance) BaselineFile {
	bf := BaselineFile{
		Schema:         baselineSchemaVersion,
		Timestamp:      time.Now().UTC().Format(time.RFC3339),
		AshVersion:     prov.AshVersion,
		AshCommitSHA:   prov.AshCommitSHA,
		CaseSetVersion: prov.CaseSetVersion,
		RepoSHA:        prov.RepoSHA,
		RepoDirty:      prov.RepoDirty,
	}
	cases := make([]BaselineCase, 0, len(res.Cases))
	totalAsh, totalBash := 0, 0
	for _, c := range res.Cases {
		cases = append(cases, BaselineCase{
			Name:          c.Name,
			Verb:          c.Verb,
			AshTokens:     c.AshTokens,
			BashTokens:    c.BashTokens,
			BashTruncated: c.BashTruncated,
		})
		totalAsh += c.AshTokens
		totalBash += c.BashTokens
	}
	sort.Slice(cases, func(i, j int) bool { return cases[i].Name < cases[j].Name })
	bf.Cases = cases
	bf.Summary = BaselineSummary{
		NCases:          len(cases),
		AshTokensTotal:  totalAsh,
		BashTokensTotal: totalBash,
		DeltaTokPct:     pctChange(totalBash, totalAsh) - pctChange(totalBash, totalBash), // (ash-bash)/bash style
	}
	if totalBash != 0 {
		bf.Summary.DeltaTokPct = float64(totalAsh-totalBash) / float64(totalBash) * 100
	}
	return bf
}

// buildBaselineFileFromLedger is the same shape, sourced from a
// persisted run + its case rows (used by --export-md).
func buildBaselineFileFromLedger(run ledger.BenchRun, cases []ledger.BenchCaseResult) BaselineFile {
	bf := BaselineFile{
		Schema:         baselineSchemaVersion,
		Timestamp:      run.Timestamp.UTC().Format(time.RFC3339),
		AshVersion:     run.AshVersion,
		AshCommitSHA:   run.AshCommitSHA,
		CaseSetVersion: run.CaseSetVersion,
		RepoSHA:        run.RepoSHA,
		RepoDirty:      run.RepoDirty,
	}
	out := make([]BaselineCase, 0, len(cases))
	totalAsh, totalBash := 0, 0
	for _, c := range cases {
		out = append(out, BaselineCase{
			Name:          c.CaseName,
			Verb:          c.Verb,
			AshTokens:     c.AshTokens,
			BashTokens:    c.BashTokens,
			AshTruncated:  c.AshTruncated,
			BashTruncated: c.BashTruncated,
		})
		totalAsh += c.AshTokens
		totalBash += c.BashTokens
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	bf.Cases = out
	bf.Summary = BaselineSummary{
		NCases:          len(out),
		AshTokensTotal:  totalAsh,
		BashTokensTotal: totalBash,
	}
	if totalBash != 0 {
		bf.Summary.DeltaTokPct = float64(totalAsh-totalBash) / float64(totalBash) * 100
	}
	return bf
}

func buildLatencySnapshot(res *Result, prov bench.Provenance, a *Args) LatencySnapshotFile {
	lf := LatencySnapshotFile{
		Schema:    latencySnapshotSchemaVersion,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Platform:  prov.Platform,
		CPUCount:  prov.CPUCount,
		RepeatN:   maxInt(a.Repeat, 1),
		WarmupN:   a.Warmup,
	}
	out := make([]LatencyCase, 0, len(res.Cases))
	for _, c := range res.Cases {
		out = append(out, LatencyCase{
			Name:      c.Name,
			Verb:      c.Verb,
			AshUsP50:  c.AshLatencyUsP50,
			AshUsMin:  c.AshLatencyUsMin,
			BashUsP50: c.BashLatencyUsP50,
			BashUsMin: c.BashLatencyUsMin,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	lf.Cases = out
	return lf
}

func buildLatencySnapshotFromLedger(run ledger.BenchRun, cases []ledger.BenchCaseResult) LatencySnapshotFile {
	lf := LatencySnapshotFile{
		Schema:    latencySnapshotSchemaVersion,
		Timestamp: run.Timestamp.UTC().Format(time.RFC3339),
		Platform:  run.Platform,
		CPUCount:  run.CPUCount,
		RepeatN:   run.RepeatN,
		WarmupN:   run.WarmupN,
	}
	out := make([]LatencyCase, 0, len(cases))
	for _, c := range cases {
		out = append(out, LatencyCase{
			Name:      c.CaseName,
			Verb:      c.Verb,
			AshUsP50:  c.AshLatencyUsP50,
			AshUsMin:  c.AshLatencyUsMin,
			BashUsP50: c.BashLatencyUsP50,
			BashUsMin: c.BashLatencyUsMin,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	lf.Cases = out
	return lf
}

// renderBaselineMarkdown produces the human-readable bench/baseline.md.
func renderBaselineMarkdown(bf BaselineFile, lf LatencySnapshotFile) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# ash bench — baseline %s\n\n", bf.Timestamp[:10])
	fmt.Fprintf(&b, "ash_version: `%s`  ", bf.AshVersion)
	if bf.AshCommitSHA != "" {
		fmt.Fprintf(&b, "ash_commit: `%s`  ", shortRunID(bf.AshCommitSHA))
	}
	fmt.Fprintf(&b, "case_set: `%s`  ", bf.CaseSetVersion)
	if bf.RepoSHA != "" {
		fmt.Fprintf(&b, "repo: `%s`", shortRunID(bf.RepoSHA))
		if bf.RepoDirty {
			b.WriteString(" (dirty)")
		}
	}
	b.WriteString("\n\n")

	b.WriteString("| case | verb | ash_tok | bash_tok | Δtok% | trunc |\n")
	b.WriteString("|---|---|---:|---:|---:|---|\n")
	for _, c := range bf.Cases {
		dpct := 0.0
		if c.BashTokens != 0 {
			dpct = float64(c.AshTokens-c.BashTokens) / float64(c.BashTokens) * 100
		}
		trunc := ""
		switch {
		case c.AshTruncated && c.BashTruncated:
			trunc = "ash+bash"
		case c.AshTruncated:
			trunc = "ash"
		case c.BashTruncated:
			trunc = "bash"
		}
		fmt.Fprintf(&b, "| `%s` | %s | %d | %d | %+.0f%% | %s |\n",
			c.Name, c.Verb, c.AshTokens, c.BashTokens, dpct, trunc)
	}

	fmt.Fprintf(&b, "\n**Overall:** %d cases, ash %d tok, bash %d tok, **%+.1f%%**.\n",
		bf.Summary.NCases, bf.Summary.AshTokensTotal, bf.Summary.BashTokensTotal, bf.Summary.DeltaTokPct)

	if len(lf.Cases) > 0 {
		fmt.Fprintf(&b, "\nLatency (informational; platform `%s`, %d CPUs, repeat=%d, warmup=%d):\n\n",
			lf.Platform, lf.CPUCount, lf.RepeatN, lf.WarmupN)
		b.WriteString("| case | ash_us_p50 | ash_us_min | bash_us_p50 | bash_us_min |\n")
		b.WriteString("|---|---:|---:|---:|---:|\n")
		for _, c := range lf.Cases {
			fmt.Fprintf(&b, "| `%s` | %d | %d | %d | %d |\n",
				c.Name, c.AshUsP50, c.AshUsMin, c.BashUsP50, c.BashUsMin)
		}
	}

	return b.String()
}

// loadBaselineFile reads <projectRoot>/bench/baseline.json. Returns
// the deserialized form or a typed error so resolveRun can surface a
// clean error code.
func loadBaselineFile(projectRoot string) (*BaselineFile, error) {
	if projectRoot == "" {
		return nil, fmt.Errorf("project_root unset")
	}
	p := filepath.Join(projectRoot, baselineDirName, baselineJSONName)
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var bf BaselineFile
	if err := json.Unmarshal(data, &bf); err != nil {
		return nil, err
	}
	if bf.Schema != baselineSchemaVersion {
		return nil, fmt.Errorf("baseline.json schema %q != expected %q", bf.Schema, baselineSchemaVersion)
	}
	return &bf, nil
}

// baselineToRunSummary adapts a BaselineFile into the RunSummary +
// per-case shape used by buildCompare. Latency is left zero — the
// baseline file is tokens-only by design.
func baselineToRunSummary(bf *BaselineFile) (RunSummary, []ledger.BenchCaseResult) {
	rs := RunSummary{
		RunUUID:        "baseline",
		AshVersion:     bf.AshVersion,
		AshCommitSHA:   bf.AshCommitSHA,
		CaseSetVersion: bf.CaseSetVersion,
		RepoSHA:        bf.RepoSHA,
		RepoDirty:      bf.RepoDirty,
		Cases:          bf.Summary.NCases,
		AshTokensTotal: bf.Summary.AshTokensTotal,
		BashTokensTotal: bf.Summary.BashTokensTotal,
		DeltaTokPct:    bf.Summary.DeltaTokPct,
	}
	if t, err := time.Parse(time.RFC3339, bf.Timestamp); err == nil {
		rs.Timestamp = t
	}
	rows := make([]ledger.BenchCaseResult, 0, len(bf.Cases))
	for _, c := range bf.Cases {
		rows = append(rows, ledger.BenchCaseResult{
			CaseName:      c.Name,
			Verb:          c.Verb,
			AshTokens:     c.AshTokens,
			BashTokens:    c.BashTokens,
			AshTruncated:  c.AshTruncated,
			BashTruncated: c.BashTruncated,
		})
	}
	return rs, rows
}

// prettyRecord renders the confirmation summary after
// `ash bench --record-baseline` writes the three files.
func prettyRecord(r *RecordBaselineResult) string {
	var b strings.Builder
	b.WriteString("=== ash bench --record-baseline ===\n")
	fmt.Fprintf(&b, "wrote %s\n", r.BaselinePath)
	fmt.Fprintf(&b, "wrote %s\n", r.MarkdownPath)
	fmt.Fprintf(&b, "wrote %s\n", r.LatencyPath)
	fmt.Fprintf(&b, "%d bytes total\n\n", r.BytesWritten)
	if r.Run != nil {
		o := r.Run.Overall
		fmt.Fprintf(&b, "run: %d cases, ash %d tok, bash %d tok\n",
			o.Cases, o.AshTokensTotal, o.BashTokensTotal)
		if o.BashTokensTotal != 0 {
			dpct := float64(o.AshTokensTotal-o.BashTokensTotal) / float64(o.BashTokensTotal) * 100
			fmt.Fprintf(&b, "Δtok%%=%+.1f%%\n", dpct)
		}
	}
	b.WriteString("\nreview the diff: git diff bench/")
	return b.String()
}

func relToProjectRoot(root, abs string) string {
	if r, err := filepath.Rel(root, abs); err == nil {
		return r
	}
	return abs
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
