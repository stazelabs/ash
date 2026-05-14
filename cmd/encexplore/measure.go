package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/stazelabs/ash/internal/ledger"
)

type rowResult struct {
	Corpus  string
	SubSet  string
	Surface string
	Before  int
	After   int
	Delta   int
	Pct     float64
}

func runMeasure(args []string) {
	fs := flag.NewFlagSet("measure", flag.ExitOnError)
	corpusDir := fs.String("corpus", "testdata/corpus", "corpus directory")
	out := fs.String("out", "testdata/measure_results.md", "markdown output")
	_ = fs.Parse(args)

	counter, err := ledger.NewCounter()
	if err != nil {
		die("counter: %v", err)
	}

	manifest, err := os.ReadFile(filepath.Join(*corpusDir, "manifest.txt"))
	if err != nil {
		die("manifest: %v", err)
	}
	corpusFiles := strings.Fields(string(manifest))

	allSets := append([]subSet{}, subSets...)
	allSets = append(allSets, combinedSubs())

	var results []rowResult
	totals := map[string]struct{ before, after int }{}

	for _, fn := range corpusFiles {
		raw, err := os.ReadFile(filepath.Join(*corpusDir, fn))
		if err != nil {
			die("read %s: %v", fn, err)
		}
		body := string(raw)
		before := counter.Count(body)

		for _, set := range allSets {
			rewritten := applySubs(body, set.Pairs)
			after := counter.Count(rewritten)
			delta := before - after
			pct := 0.0
			if before > 0 {
				pct = 100 * float64(delta) / float64(before)
			}
			results = append(results, rowResult{
				Corpus: strings.TrimSuffix(fn, ".txt"),
				SubSet: set.Name, Surface: set.Surface,
				Before: before, After: after, Delta: delta, Pct: pct,
			})
			t := totals[set.Name]
			t.before += before
			t.after += after
			totals[set.Name] = t
		}
	}

	f, err := os.Create(*out)
	if err != nil {
		die("create %s: %v", *out, err)
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	defer w.Flush()

	fmt.Fprintln(w, "# encexplore: substitution measurement results (cl100k_base)")
	fmt.Fprintln(w)

	// Aggregate table — most useful at a glance.
	fmt.Fprintln(w, "## Aggregate (across the entire corpus)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "| sub-set | surface | before | after | Δ tokens | Δ % |")
	fmt.Fprintln(w, "|---|---|---:|---:|---:|---:|")
	names := []string{}
	for name := range totals {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		di := totals[names[i]].before - totals[names[i]].after
		dj := totals[names[j]].before - totals[names[j]].after
		return di > dj
	})
	for _, name := range names {
		t := totals[name]
		delta := t.before - t.after
		pct := 0.0
		if t.before > 0 {
			pct = 100 * float64(delta) / float64(t.before)
		}
		surface := ""
		for _, s := range allSets {
			if s.Name == name {
				surface = s.Surface
				break
			}
		}
		fmt.Fprintf(w, "| %s | %s | %d | %d | %d | %+.2f%% |\n",
			name, surface, t.before, t.after, delta, pct)
	}

	// Per-corpus, per-subset detail (only rows with non-zero delta).
	fmt.Fprintln(w)
	fmt.Fprintln(w, "## Per-corpus detail (rows with non-zero Δ)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "| corpus | sub-set | before | after | Δ tokens | Δ % |")
	fmt.Fprintln(w, "|---|---|---:|---:|---:|---:|")
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Corpus != results[j].Corpus {
			return results[i].Corpus < results[j].Corpus
		}
		return results[i].Delta > results[j].Delta
	})
	for _, r := range results {
		if r.Delta == 0 {
			continue
		}
		fmt.Fprintf(w, "| %s | %s | %d | %d | %d | %+.2f%% |\n",
			r.Corpus, r.SubSet, r.Before, r.After, r.Delta, r.Pct)
	}

	fmt.Fprintf(os.Stderr, "measure: %d rows -> %s\n", len(results), *out)
}

// applySubs rewrites body by replacing every Pair.0 with Pair.1 in order.
// Naive ReplaceAll — substitution pairs must be designed so partial matches
// don't happen (e.g. always include the trailing "=" for metric labels).
func applySubs(body string, pairs [][2]string) string {
	for _, p := range pairs {
		body = strings.ReplaceAll(body, p[0], p[1])
	}
	return body
}
