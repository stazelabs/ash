package bench

// micro.go implements ash bench --micro, --record-micro, and --diff-micro.
//
// --micro runs Go Benchmark* functions for a curated set of hot-path packages
// via the ash test --bench machinery (ASH-90 prerequisite), captures ns/op,
// B/op, and allocs/op per sub-case, and returns a MicroResult.
//
// --record-micro runs --micro and writes bench/microbench.json +
// bench/microbench.md, mirroring the baseline.json / baseline.md shape.
//
// --diff-micro runs --micro and compares ns/op per sub-case against the saved
// bench/microbench.json, flagging regressions that exceed --regress-latency.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/stazelabs/ash/internal/bench"
	"github.com/stazelabs/ash/internal/proto"
	testverb "github.com/stazelabs/ash/internal/verbs/test"
)

const (
	kindMicro       = "micro"
	kindRecordMicro = "record_micro"
	kindDiffMicro   = "diff_micro"

	microbenchSchemaVersion = "ash-bench-microbench-v1"
	microbenchJSONName      = "microbench.json"
	microbenchMDName        = "microbench.md"
)

// defaultMicroPackages is the curated set of hot-path packages. Packages
// without Benchmark* functions are silently skipped (empty BenchOutput).
var defaultMicroPackages = []string{
	"cmd/ash",
	"internal/verbs/hook",
	"internal/verbs/grep",
	"internal/walker",
	"internal/gitignore",
}

// MicroBenchRow is one parsed Go benchmark result, averaged over --micro-count
// runs when count>1.
type MicroBenchRow struct {
	Name        string  `msgpack:"name" json:"name"`
	BaseName    string  `msgpack:"base_name" json:"base_name"`
	Package     string  `msgpack:"package" json:"package"`
	N           int64   `msgpack:"n" json:"n"`
	NsPerOp     float64 `msgpack:"ns_per_op" json:"ns_per_op"`
	BPerOp      float64 `msgpack:"b_per_op" json:"b_per_op"`
	AllocsPerOp float64 `msgpack:"allocs_per_op" json:"allocs_per_op"`
}

// MicroPackage is one package's results from --micro mode.
type MicroPackage struct {
	Package string          `msgpack:"package" json:"package"`
	Rows    []MicroBenchRow `msgpack:"rows,omitempty" json:"rows,omitempty"`
	Error   string          `msgpack:"error,omitempty" json:"error,omitempty"`
}

// MicroResult is the response shape for ash bench --micro.
type MicroResult struct {
	Kind      string          `msgpack:"kind"`
	Packages  []MicroPackage  `msgpack:"packages"`
	All       []MicroBenchRow `msgpack:"all,omitempty"`
	BenchTime string          `msgpack:"bench_time"`
	Count     int             `msgpack:"count"`
}

// MicroBaselineFile is the JSON schema for bench/microbench.json.
type MicroBaselineFile struct {
	Schema     string          `json:"schema"`
	Timestamp  string          `json:"ts"`
	AshVersion string          `json:"ash_version,omitempty"`
	RepoSHA    string          `json:"repo_sha,omitempty"`
	RepoDirty  bool            `json:"repo_dirty,omitempty"`
	BenchTime  string          `json:"bench_time"`
	Count      int             `json:"count"`
	Cases      []MicroBenchRow `json:"cases"`
}

// RecordMicroResult is the response for ash bench --record-micro.
type RecordMicroResult struct {
	Kind         string       `msgpack:"kind"`
	JSONPath     string       `msgpack:"json_path"`
	MarkdownPath string       `msgpack:"markdown_path"`
	BytesWritten int          `msgpack:"bytes_written"`
	Run          *MicroResult `msgpack:"run"`
}

// DiffMicroRow is one benchmark comparison row in --diff-micro output.
type DiffMicroRow struct {
	Name            string  `msgpack:"name"`
	Package         string  `msgpack:"package"`
	BaseNsPerOp     float64 `msgpack:"base_ns_per_op"`
	CurrNsPerOp     float64 `msgpack:"curr_ns_per_op"`
	NsDeltaPct      float64 `msgpack:"ns_delta_pct"`
	BaseAllocsPerOp float64 `msgpack:"base_allocs_per_op"`
	CurrAllocsPerOp float64 `msgpack:"curr_allocs_per_op"`
	Regressed       bool    `msgpack:"regressed,omitempty"`
}

// DiffMicroResult is the response for ash bench --diff-micro.
type DiffMicroResult struct {
	Kind       string         `msgpack:"kind"`
	Rows       []DiffMicroRow `msgpack:"rows"`
	BaseTS     string         `msgpack:"base_ts"`
	NewOnly    []string       `msgpack:"new_only,omitempty"`
	BaseOnly   []string       `msgpack:"base_only,omitempty"`
	RegressPct int            `msgpack:"regress_pct"`
}

// runMicro runs Go benchmarks for curated packages via ash test --bench and
// returns structured per-sub-case results.
func runMicro(d Deps, a *Args) (*MicroResult, *proto.Error) {
	if d.Run == nil {
		return nil, &proto.Error{Code: "config", Msg: "bench --micro: deps not wired"}
	}
	benchTime := a.MicroBenchTime
	if benchTime == "" {
		benchTime = "1s"
	}
	count := a.MicroCount
	if count < 1 {
		count = 1
	}
	packages := defaultMicroPackages
	if a.MicroPackages != "" {
		packages = strings.Split(a.MicroPackages, ",")
		for i := range packages {
			packages[i] = strings.TrimSpace(packages[i])
		}
	}

	res := &MicroResult{
		Kind:      kindMicro,
		BenchTime: benchTime,
		Count:     count,
	}
	for _, pkg := range packages {
		mp := MicroPackage{Package: pkg}
		testArgs := map[string]any{
			"packages":  pkg,
			"bench":     ".",
			"benchmem":  true,
			"benchtime": benchTime,
			"count":     count,
			"timeout":   "5m",
		}
		data, perr := d.Run("test", testArgs)
		if perr != nil {
			mp.Error = perr.Code + ": " + perr.Msg
			res.Packages = append(res.Packages, mp)
			continue
		}
		tr, ok := data.(*testverb.Result)
		if !ok {
			mp.Error = "unexpected result type from test verb"
			res.Packages = append(res.Packages, mp)
			continue
		}
		rows := parseMicroRows(pkg, tr)
		mp.Rows = rows
		res.All = append(res.All, rows...)
		res.Packages = append(res.Packages, mp)
	}
	return res, nil
}

// runRecordMicro runs --micro and writes bench/microbench.json + bench/microbench.md.
func runRecordMicro(d Deps, a *Args) (*RecordMicroResult, *proto.Error) {
	if d.ProjectRoot == "" {
		return nil, &proto.Error{Code: "config", Msg: "record_micro: project_root not wired"}
	}
	res, perr := runMicro(d, a)
	if perr != nil {
		return nil, perr
	}
	prov := bench.CaptureProvenance(d.DaemonStart, d.ProjectRoot)
	bf := buildMicroBaselineFile(res, prov)

	dir := filepath.Join(d.ProjectRoot, baselineDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, &proto.Error{Code: "io", Msg: "mkdir bench/: " + err.Error()}
	}
	jsonPath := filepath.Join(dir, microbenchJSONName)
	mdPath := filepath.Join(dir, microbenchMDName)

	jsonBytes, _ := json.MarshalIndent(bf, "", "  ")
	jsonBytes = append(jsonBytes, '\n')
	if err := os.WriteFile(jsonPath, jsonBytes, 0o644); err != nil {
		return nil, &proto.Error{Code: "io", Msg: "write microbench.json: " + err.Error()}
	}
	mdBody := renderMicroMarkdown(bf)
	if err := os.WriteFile(mdPath, []byte(mdBody), 0o644); err != nil {
		return nil, &proto.Error{Code: "io", Msg: "write microbench.md: " + err.Error()}
	}

	return &RecordMicroResult{
		Kind:         kindRecordMicro,
		JSONPath:     relToProjectRoot(d.ProjectRoot, jsonPath),
		MarkdownPath: relToProjectRoot(d.ProjectRoot, mdPath),
		BytesWritten: len(jsonBytes) + len(mdBody),
		Run:          res,
	}, nil
}

// runDiffMicro runs --micro and compares ns/op per sub-case against bench/microbench.json.
func runDiffMicro(d Deps, a *Args) (*DiffMicroResult, *proto.Error) {
	if d.ProjectRoot == "" {
		return nil, &proto.Error{Code: "config", Msg: "diff_micro: project_root not wired"}
	}
	baseline, err := loadMicroBaselineFile(d.ProjectRoot)
	if err != nil {
		return nil, &proto.Error{Code: "no_baseline", Msg: "bench/microbench.json: " + err.Error(),
			Hint: "run 'ash bench --record-micro' first"}
	}
	curr, perr := runMicro(d, a)
	if perr != nil {
		return nil, perr
	}

	regressPct := a.RegressLatPct
	if regressPct == 0 {
		regressPct = 20
	}

	baseIdx := map[string]MicroBenchRow{}
	for _, c := range baseline.Cases {
		key := c.Package + "/" + c.BaseName
		baseIdx[key] = c
	}
	currIdx := map[string]MicroBenchRow{}
	for _, r := range curr.All {
		key := r.Package + "/" + r.BaseName
		currIdx[key] = r
	}

	dr := &DiffMicroResult{
		Kind:       kindDiffMicro,
		BaseTS:     baseline.Timestamp,
		RegressPct: regressPct,
	}
	var rows []DiffMicroRow
	for key, base := range baseIdx {
		if c, ok := currIdx[key]; ok {
			nsDelta := 0.0
			if base.NsPerOp > 0 {
				nsDelta = (c.NsPerOp - base.NsPerOp) / base.NsPerOp * 100
			}
			rows = append(rows, DiffMicroRow{
				Name:            base.BaseName,
				Package:         base.Package,
				BaseNsPerOp:     base.NsPerOp,
				CurrNsPerOp:     c.NsPerOp,
				NsDeltaPct:      nsDelta,
				BaseAllocsPerOp: base.AllocsPerOp,
				CurrAllocsPerOp: c.AllocsPerOp,
				Regressed:       nsDelta > float64(regressPct),
			})
		} else {
			dr.BaseOnly = append(dr.BaseOnly, key)
		}
	}
	for key := range currIdx {
		if _, ok := baseIdx[key]; !ok {
			dr.NewOnly = append(dr.NewOnly, key)
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Package != rows[j].Package {
			return rows[i].Package < rows[j].Package
		}
		return rows[i].Name < rows[j].Name
	})
	dr.Rows = rows
	return dr, nil
}

// parseMicroRows extracts benchmark rows from a test result. When -count>1,
// multiple lines appear for the same benchmark name; ns/op and allocs/op are
// averaged across them.
func parseMicroRows(pkg string, tr *testverb.Result) []MicroBenchRow {
	type acc struct {
		count     int
		nsSum     float64
		bSum      float64
		allocsSum float64
		n         int64
		name      string
		baseName  string
	}
	accs := map[string]*acc{}
	var order []string

	for _, p := range tr.Packages {
		for _, line := range p.BenchOutput {
			row, ok := parseBenchLine(line)
			if !ok {
				continue
			}
			key := row.BaseName
			if a, exists := accs[key]; exists {
				a.count++
				a.nsSum += row.NsPerOp
				a.bSum += row.BPerOp
				a.allocsSum += row.AllocsPerOp
				if row.N > a.n {
					a.n = row.N
				}
			} else {
				order = append(order, key)
				accs[key] = &acc{
					count:     1,
					nsSum:     row.NsPerOp,
					bSum:      row.BPerOp,
					allocsSum: row.AllocsPerOp,
					n:         row.N,
					name:      row.Name,
					baseName:  row.BaseName,
				}
			}
		}
	}

	rows := make([]MicroBenchRow, 0, len(order))
	for _, key := range order {
		a := accs[key]
		n := float64(a.count)
		rows = append(rows, MicroBenchRow{
			Name:        a.name,
			BaseName:    a.baseName,
			Package:     pkg,
			N:           a.n,
			NsPerOp:     a.nsSum / n,
			BPerOp:      a.bSum / n,
			AllocsPerOp: a.allocsSum / n,
		})
	}
	return rows
}

// buildMicroBaselineFile converts a MicroResult to the JSON schema.
func buildMicroBaselineFile(res *MicroResult, prov bench.Provenance) MicroBaselineFile {
	cases := make([]MicroBenchRow, len(res.All))
	copy(cases, res.All)
	sort.Slice(cases, func(i, j int) bool {
		if cases[i].Package != cases[j].Package {
			return cases[i].Package < cases[j].Package
		}
		return cases[i].BaseName < cases[j].BaseName
	})
	return MicroBaselineFile{
		Schema:     microbenchSchemaVersion,
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
		AshVersion: prov.AshVersion,
		RepoSHA:    prov.RepoSHA,
		RepoDirty:  prov.RepoDirty,
		BenchTime:  res.BenchTime,
		Count:      res.Count,
		Cases:      cases,
	}
}

// renderMicroMarkdown produces bench/microbench.md.
func renderMicroMarkdown(bf MicroBaselineFile) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# ash bench --micro — %s\n\n", bf.Timestamp[:10])
	fmt.Fprintf(&b, "benchtime: `%s`  count: `%d`", bf.BenchTime, bf.Count)
	if bf.AshVersion != "" {
		fmt.Fprintf(&b, "  ash_version: `%s`", bf.AshVersion)
	}
	if bf.RepoSHA != "" {
		fmt.Fprintf(&b, "  repo: `%s`", shortRunID(bf.RepoSHA))
		if bf.RepoDirty {
			b.WriteString(" (dirty)")
		}
	}
	b.WriteString("\n\n")
	b.WriteString("| benchmark | package | ns/op | B/op | allocs/op |\n")
	b.WriteString("|---|---|---:|---:|---:|\n")
	for _, c := range bf.Cases {
		fmt.Fprintf(&b, "| `%s` | %s | %.1f | %.0f | %.0f |\n",
			c.BaseName, c.Package, c.NsPerOp, c.BPerOp, c.AllocsPerOp)
	}
	pkgs := countMicroPackages(bf.Cases)
	fmt.Fprintf(&b, "\n%d benchmark(s) across %d package(s).\n", len(bf.Cases), pkgs)
	return b.String()
}

// loadMicroBaselineFile reads bench/microbench.json.
func loadMicroBaselineFile(projectRoot string) (*MicroBaselineFile, error) {
	p := filepath.Join(projectRoot, baselineDirName, microbenchJSONName)
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var bf MicroBaselineFile
	if err := json.Unmarshal(data, &bf); err != nil {
		return nil, err
	}
	if bf.Schema != microbenchSchemaVersion {
		return nil, fmt.Errorf("schema %q != expected %q", bf.Schema, microbenchSchemaVersion)
	}
	return &bf, nil
}

// prettyMicro renders a MicroResult for terminal output.
func prettyMicro(r *MicroResult) string {
	var b strings.Builder
	n := len(r.All)
	fmt.Fprintf(&b, "§bench --micro: %d benchmark(s), benchtime=%s count=%d\n",
		n, r.BenchTime, r.Count)
	if n == 0 {
		b.WriteString("no benchmarks found in curated packages\n")
		for _, mp := range r.Packages {
			if mp.Error != "" {
				fmt.Fprintf(&b, "  %s: %s\n", mp.Package, mp.Error)
			}
		}
		return strings.TrimRight(b.String(), "\n")
	}
	fmt.Fprintf(&b, "%-52s %-32s %12s %8s %12s\n",
		"benchmark", "package", "ns/op", "B/op", "allocs/op")
	for _, mp := range r.Packages {
		if mp.Error != "" {
			fmt.Fprintf(&b, "  ERROR %-30s %s\n", mp.Package, mp.Error)
			continue
		}
		for _, row := range mp.Rows {
			fmt.Fprintf(&b, "%-52s %-32s %12.1f %8.0f %12.0f\n",
				truncStr(row.BaseName, 52), truncStr(row.Package, 32),
				row.NsPerOp, row.BPerOp, row.AllocsPerOp)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// prettyRecordMicro renders a RecordMicroResult.
func prettyRecordMicro(r *RecordMicroResult) string {
	var b strings.Builder
	b.WriteString("§bench --record-micro\n")
	fmt.Fprintf(&b, "wrote %s\n", r.JSONPath)
	fmt.Fprintf(&b, "wrote %s\n", r.MarkdownPath)
	fmt.Fprintf(&b, "%d bytes total\n", r.BytesWritten)
	if r.Run != nil {
		fmt.Fprintf(&b, "%d benchmark(s) recorded\n", len(r.Run.All))
	}
	b.WriteString("\nreview the diff: git diff bench/")
	return strings.TrimRight(b.String(), "\n")
}

// prettyDiffMicro renders a DiffMicroResult.
func prettyDiffMicro(r *DiffMicroResult) string {
	var b strings.Builder
	baseDate := r.BaseTS
	if len(baseDate) >= 10 {
		baseDate = baseDate[:10]
	}
	fmt.Fprintf(&b, "§bench --diff-micro: vs baseline %s (regress_pct=%d%%)\n",
		baseDate, r.RegressPct)
	if len(r.Rows) == 0 && len(r.NewOnly) == 0 && len(r.BaseOnly) == 0 {
		b.WriteString("no benchmarks to compare\n")
		return strings.TrimRight(b.String(), "\n")
	}
	fmt.Fprintf(&b, "%-52s %12s %12s %8s %10s\n",
		"benchmark", "base_ns/op", "curr_ns/op", "Δns%", "")
	for _, row := range r.Rows {
		flag := ""
		if row.Regressed {
			flag = "REGRESS"
		}
		fmt.Fprintf(&b, "%-52s %12.1f %12.1f %8s %10s\n",
			truncStr(row.Name, 52),
			row.BaseNsPerOp, row.CurrNsPerOp,
			fmt.Sprintf("%+.0f%%", row.NsDeltaPct),
			flag)
	}
	if len(r.NewOnly) > 0 {
		sort.Strings(r.NewOnly)
		b.WriteString("\nnew (not in baseline):\n")
		for _, n := range r.NewOnly {
			fmt.Fprintf(&b, "  %s\n", n)
		}
	}
	if len(r.BaseOnly) > 0 {
		sort.Strings(r.BaseOnly)
		b.WriteString("\nremoved (only in baseline):\n")
		for _, n := range r.BaseOnly {
			fmt.Fprintf(&b, "  %s\n", n)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// benchLineRe parses a single Go benchmark result line. Handles both the
// plain form (ns/op only) and the -benchmem form (ns/op + B/op + allocs/op).
// The [0-9.e+]+ pattern covers scientific notation (e.g. 1.23e+03).
var benchLineRe = regexp.MustCompile(
	`^(Benchmark\S+)\s+(\d+)\s+([0-9.e+]+)\s+ns/op(?:\s+([0-9.e+]+)\s+B/op\s+([0-9.e+]+)\s+allocs/op)?`)

// parseBenchLine parses one Go benchmark output line into a MicroBenchRow.
func parseBenchLine(line string) (MicroBenchRow, bool) {
	m := benchLineRe.FindStringSubmatch(strings.TrimSpace(line))
	if m == nil {
		return MicroBenchRow{}, false
	}
	row := MicroBenchRow{Name: m[1], BaseName: stripGOMAXPROCS(m[1])}
	row.N, _ = strconv.ParseInt(m[2], 10, 64)
	row.NsPerOp, _ = strconv.ParseFloat(m[3], 64)
	if m[4] != "" {
		row.BPerOp, _ = strconv.ParseFloat(m[4], 64)
		row.AllocsPerOp, _ = strconv.ParseFloat(m[5], 64)
	}
	return row, true
}

// stripGOMAXPROCS removes the trailing -N GOMAXPROCS suffix from a benchmark
// name. e.g. "BenchmarkFoo/bar-8" → "BenchmarkFoo/bar". Names where the
// suffix is not all digits are left unchanged.
func stripGOMAXPROCS(name string) string {
	i := strings.LastIndexByte(name, '-')
	if i < 0 {
		return name
	}
	suffix := name[i+1:]
	if len(suffix) == 0 {
		return name
	}
	for _, c := range suffix {
		if c < '0' || c > '9' {
			return name
		}
	}
	return name[:i]
}

func countMicroPackages(rows []MicroBenchRow) int {
	seen := map[string]bool{}
	for _, r := range rows {
		seen[r.Package] = true
	}
	return len(seen)
}
