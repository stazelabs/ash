// harnessbench computes a one-shot ash-vs-Claude-Code-harness comparison
// over the bench corpus, for the value-assessment writeup (ASH-181).
//
// Why a standalone command and not an `ash bench --mode harness` flag:
// the harness baseline is a *simulation* (the hook denies real harness
// tool invocations in-repo, so we can't drive them from inside this
// session). The simulation is deliberately documented in one place so
// the methodology + caveats stay legible. Folding it into `ash bench`
// would conflate measured numbers with extrapolated ones.
//
// Methodology
//
//	read  → harness Read returns "cat -n" format (6-char right-padded
//	        line number, tab, content). We re-run the bash equivalent,
//	        transform stdout to cat -n shape, tokenize.
//	grep  → harness Grep wraps ripgrep; default output mode is
//	        file:line:content — byte-identical to `grep -rn`. We use
//	        the existing bash_tokens from bench/baseline.json.
//	find  → harness Glob returns paths matching the pattern. Content
//	        is the same set of paths as `find` (just sorted by mtime
//	        rather than walk order); token count is identical. We use
//	        the existing bash_tokens from bench/baseline.json.
//	other → no clean harness equivalent (git/stat/diff/edit/write/test).
//	        Marked n/a.
//
// Not modeled: the tool-call envelope on the harness side (the JSON
// framing around content blocks adds ~10–30 tokens per call). These
// numbers are the payload cost, not the full envelope cost. ASH-182
// is the companion ticket for measuring envelope tax.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"sort"

	"github.com/stazelabs/ash/internal/bench"
	"github.com/stazelabs/ash/internal/ledger"
)

type baseline struct {
	Cases []baselineCase `json:"cases"`
}

type baselineCase struct {
	Name       string `json:"name"`
	Verb       string `json:"verb"`
	AshTokens  int    `json:"ash_tokens"`
	BashTokens int    `json:"bash_tokens"`
}

type row struct {
	name       string
	verb       string
	ashTok     int
	bashTok    int
	harnessTok int
	note       string
}

func main() {
	in := flag.String("in", "bench/baseline.json", "path to bench baseline json")
	out := flag.String("out", "-", "output markdown path; '-' for stdout")
	flag.Parse()

	raw, err := os.ReadFile(*in)
	if err != nil {
		log.Fatalf("read %s: %v", *in, err)
	}
	var bl baseline
	if err := json.Unmarshal(raw, &bl); err != nil {
		log.Fatalf("parse %s: %v", *in, err)
	}
	counter, err := ledger.NewCounter()
	if err != nil {
		log.Fatalf("new counter: %v", err)
	}

	caseByName := make(map[string]bench.Case, len(bench.Cases))
	for _, c := range bench.Cases {
		caseByName[c.Name] = c
	}

	rows := make([]row, 0, len(bl.Cases))
	for _, b := range bl.Cases {
		r := row{name: b.Name, verb: b.Verb, ashTok: b.AshTokens, bashTok: b.BashTokens}
		switch b.Verb {
		case "read":
			c, ok := caseByName[b.Name]
			if !ok {
				r.note = "case not found in bench.Cases"
				r.harnessTok = -1
				rows = append(rows, r)
				continue
			}
			tok, note, err := harnessReadTokens(c, counter)
			if err != nil {
				r.note = "error: " + err.Error()
				r.harnessTok = -1
			} else {
				r.harnessTok = tok
				r.note = note
			}
		case "grep":
			r.harnessTok = b.BashTokens
			r.note = "= bash grep (harness Grep wraps ripgrep, same default format)"
		case "find":
			r.harnessTok = b.BashTokens
			r.note = "= bash find (harness Glob returns same paths, mtime-sorted)"
		default:
			r.harnessTok = -1
			r.note = "no clean harness equivalent"
		}
		rows = append(rows, r)
	}

	sort.Slice(rows, func(i, j int) bool { return rows[i].name < rows[j].name })

	var w bytes.Buffer
	emit(&w, rows)

	if *out == "-" {
		fmt.Print(w.String())
		return
	}
	if err := os.WriteFile(*out, w.Bytes(), 0o644); err != nil {
		log.Fatalf("write %s: %v", *out, err)
	}
}

// harnessReadTokens re-runs the bash equivalent for a read case, transforms
// its stdout into "cat -n" format (the harness Read response shape), and
// returns the tokenized count.
func harnessReadTokens(c bench.Case, counter *ledger.Counter) (int, string, error) {
	argv, err := bench.BashFor(c)
	if err != nil {
		return 0, "", err
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	stdout, err := cmd.Output()
	if err != nil {
		// non-zero exit (e.g. grep no-match) is normal; preserve stdout.
		if ee, ok := err.(*exec.ExitError); ok {
			_ = ee
		} else {
			return 0, "", err
		}
	}
	harness := catNFormat(stdout)
	return counter.Count(string(harness)), "Read (cat -n format applied to bash output)", nil
}

// catNFormat prepends the standard `cat -n` line-number prefix to each
// line of the input. cat -n format: 6-character right-padded line number,
// tab, content, newline. Verified byte-for-byte against awk equivalent.
func catNFormat(b []byte) []byte {
	if len(b) == 0 {
		return b
	}
	lines := bytes.Split(b, []byte("\n"))
	// Trailing newline produces an empty final element; skip it so we
	// don't add a phantom "N+1\t\n" line.
	if len(lines) > 0 && len(lines[len(lines)-1]) == 0 {
		lines = lines[:len(lines)-1]
	}
	var out bytes.Buffer
	out.Grow(len(b) + len(lines)*8)
	for i, line := range lines {
		fmt.Fprintf(&out, "%6d\t%s\n", i+1, line)
	}
	return out.Bytes()
}

func emit(w *bytes.Buffer, rows []row) {
	var totalAsh, totalBash, totalHarness int
	var comparable int

	fmt.Fprintln(w, "| case | verb | ash_tok | bash_tok | harness_tok | Δash-vs-bash | Δash-vs-harness | note |")
	fmt.Fprintln(w, "|---|---|---:|---:|---:|---:|---:|---|")
	for _, r := range rows {
		dBash := pct(r.ashTok, r.bashTok)
		var dHarness, harnessCol string
		if r.harnessTok >= 0 {
			dHarness = fmt.Sprintf("%+d%%", pctInt(r.ashTok, r.harnessTok))
			harnessCol = fmt.Sprintf("%d", r.harnessTok)
			totalHarness += r.harnessTok
			totalAsh += r.ashTok
			totalBash += r.bashTok
			comparable++
		} else {
			dHarness = "—"
			harnessCol = "n/a"
		}
		fmt.Fprintf(w, "| `%s` | %s | %d | %d | %s | %s | %s | %s |\n",
			r.name, r.verb, r.ashTok, r.bashTok, harnessCol, dBash, dHarness, r.note)
	}
	fmt.Fprintf(w, "\n**Comparable subset (%d cases):** ash %d tok, bash %d tok, harness %d tok.\n",
		comparable, totalAsh, totalBash, totalHarness)
	if totalBash != 0 {
		fmt.Fprintf(w, "* ash vs bash:    **%+d%%**\n", pctInt(totalAsh, totalBash))
	}
	if totalHarness != 0 {
		fmt.Fprintf(w, "* ash vs harness: **%+d%%**\n", pctInt(totalAsh, totalHarness))
	}
}

func pct(a, b int) string {
	if b == 0 {
		if a == 0 {
			return "+0%"
		}
		return "+inf%"
	}
	return fmt.Sprintf("%+d%%", pctInt(a, b))
}

func pctInt(a, b int) int {
	if b == 0 {
		return 0
	}
	return int(float64(a-b) / float64(b) * 100)
}
