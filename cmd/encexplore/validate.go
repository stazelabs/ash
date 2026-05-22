package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/stazelabs/ash/internal/ledger"
)

// runValidate cross-checks the cl100k_base predictions against Anthropic's
// count_tokens endpoint. For each (corpus file, sub set) it computes both
// cl100k and Claude deltas, and reports whether they agree.
//
// Requires ANTHROPIC_API_KEY in the environment.
func runValidate(args []string) {
	fs := flag.NewFlagSet("validate", flag.ExitOnError)
	corpusDir := fs.String("corpus", "testdata/corpus", "corpus directory")
	out := fs.String("out", "testdata/validate_results.md", "markdown output")
	model := fs.String("model", "claude-sonnet-4-5", "Claude model for count_tokens")
	setsCSV := fs.String("sets", "metrics_no_equals,metrics_short_ascii,headers_compact,errors_ascii,truncation_compact,combined_aggressive",
		"comma-separated sub-set names to validate")
	_ = fs.Parse(args)

	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" {
		die("ANTHROPIC_API_KEY not set")
	}

	counter, err := ledger.NewCounter()
	if err != nil {
		die("counter: %v", err)
	}

	manifestBytes, err := os.ReadFile(filepath.Join(*corpusDir, "manifest.txt"))
	if err != nil {
		die("manifest: %v", err)
	}
	files := strings.Fields(string(manifestBytes))

	wantNames := map[string]bool{}
	for _, n := range strings.Split(*setsCSV, ",") {
		wantNames[strings.TrimSpace(n)] = true
	}
	var selected []subSet
	all := append([]subSet{}, subSets...)
	all = append(all, combinedSubs())
	for _, s := range all {
		if wantNames[s.Name] {
			selected = append(selected, s)
		}
	}

	type row struct {
		Corpus, Set               string
		ClBefore, ClAfter         int
		ClDelta                   int
		ClPct                     float64
		ClaudeBefore, ClaudeAfter int
		ClaudeDelta               int
		ClaudePct                 float64
	}
	var rows []row
	cache := map[string]int{} // body -> Claude tokens, to avoid duplicate API calls

	claudeCount := func(s string) (int, error) {
		if v, ok := cache[s]; ok {
			return v, nil
		}
		n, err := countTokensAnthropic(key, *model, s)
		if err != nil {
			return 0, err
		}
		cache[s] = n
		return n, nil
	}

	for _, fn := range files {
		raw, err := os.ReadFile(filepath.Join(*corpusDir, fn))
		if err != nil {
			die("read %s: %v", fn, err)
		}
		body := string(raw)
		clBefore := counter.Count(body)
		claudeBefore, err := claudeCount(body)
		if err != nil {
			die("claude (%s): %v", fn, err)
		}

		for _, set := range selected {
			after := applySubs(body, set.Pairs)
			clAfter := counter.Count(after)
			claudeAfter, err := claudeCount(after)
			if err != nil {
				die("claude (%s/%s): %v", fn, set.Name, err)
			}
			r := row{
				Corpus:   strings.TrimSuffix(fn, ".txt"),
				Set:      set.Name,
				ClBefore: clBefore, ClAfter: clAfter,
				ClDelta:      clBefore - clAfter,
				ClaudeBefore: claudeBefore, ClaudeAfter: claudeAfter,
				ClaudeDelta: claudeBefore - claudeAfter,
			}
			if clBefore > 0 {
				r.ClPct = 100 * float64(r.ClDelta) / float64(clBefore)
			}
			if claudeBefore > 0 {
				r.ClaudePct = 100 * float64(r.ClaudeDelta) / float64(claudeBefore)
			}
			rows = append(rows, r)
			fmt.Fprintf(os.Stderr, "validate: %-20s %-22s cl Δ=%+d (%+.2f%%)  claude Δ=%+d (%+.2f%%)\n",
				r.Corpus, r.Set, r.ClDelta, r.ClPct, r.ClaudeDelta, r.ClaudePct)
		}
	}

	f, err := os.Create(*out)
	if err != nil {
		die("create %s: %v", *out, err)
	}
	defer f.Close()
	fmt.Fprintf(f, "# encexplore: cl100k vs Claude (%s) cross-validation\n\n", *model)
	fmt.Fprintln(f, "| corpus | sub-set | cl Δ | cl Δ% | claude Δ | claude Δ% | agreement |")
	fmt.Fprintln(f, "|---|---|---:|---:|---:|---:|:---:|")
	type agg struct {
		clBefore, clAfter, claudeBefore, claudeAfter int
	}
	totals := map[string]*agg{}
	for _, r := range rows {
		marker := "✓"
		if r.ClDelta == 0 && r.ClaudeDelta == 0 {
			marker = "—"
		} else if r.ClDelta > 0 && r.ClaudeDelta <= 0 {
			marker = "✗"
		} else if r.ClDelta < 0 && r.ClaudeDelta > 0 {
			marker = "✗"
		}
		fmt.Fprintf(f, "| %s | %s | %+d | %+.2f%% | %+d | %+.2f%% | %s |\n",
			r.Corpus, r.Set, r.ClDelta, r.ClPct, r.ClaudeDelta, r.ClaudePct, marker)
		a := totals[r.Set]
		if a == nil {
			a = &agg{}
			totals[r.Set] = a
		}
		a.clBefore += r.ClBefore
		a.clAfter += r.ClAfter
		a.claudeBefore += r.ClaudeBefore
		a.claudeAfter += r.ClaudeAfter
	}
	fmt.Fprintln(f)
	fmt.Fprintln(f, "## Aggregate by sub-set")
	fmt.Fprintln(f)
	fmt.Fprintln(f, "| sub-set | cl before | cl after | cl Δ% | claude before | claude after | claude Δ% |")
	fmt.Fprintln(f, "|---|---:|---:|---:|---:|---:|---:|")
	for _, s := range selected {
		a := totals[s.Name]
		if a == nil {
			continue
		}
		clPct := 100 * float64(a.clBefore-a.clAfter) / float64(maxInt(a.clBefore, 1))
		clPct = 0 - clPct
		clPct = -clPct
		clDelta := a.clBefore - a.clAfter
		claudeDelta := a.claudeBefore - a.claudeAfter
		clPctF := 100 * float64(clDelta) / float64(maxInt(a.clBefore, 1))
		claudePctF := 100 * float64(claudeDelta) / float64(maxInt(a.claudeBefore, 1))
		fmt.Fprintf(f, "| %s | %d | %d | %+.2f%% | %d | %d | %+.2f%% |\n",
			s.Name, a.clBefore, a.clAfter, clPctF, a.claudeBefore, a.claudeAfter, claudePctF)
	}
	fmt.Fprintf(os.Stderr, "validate: %d rows -> %s\n", len(rows), *out)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

type countTokensReq struct {
	Model    string                   `json:"model"`
	Messages []map[string]interface{} `json:"messages"`
}

type countTokensResp struct {
	InputTokens int `json:"input_tokens"`
}

func countTokensAnthropic(key, model, content string) (int, error) {
	req := countTokensReq{
		Model: model,
		Messages: []map[string]interface{}{
			{"role": "user", "content": content},
		},
	}
	body, err := json.Marshal(req)
	if err != nil {
		return 0, err
	}
	httpReq, err := http.NewRequest("POST", "https://api.anthropic.com/v1/messages/count_tokens", bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", key)
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	client := &http.Client{Timeout: 30 * time.Second}
	var resp *http.Response
	for attempt := 0; attempt < 3; attempt++ {
		resp, err = client.Do(httpReq)
		if err == nil && resp.StatusCode < 500 {
			break
		}
		if resp != nil {
			resp.Body.Close()
		}
		time.Sleep(time.Duration(1<<attempt) * time.Second)
	}
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("status %d: %s", resp.StatusCode, string(b))
	}
	var ctr countTokensResp
	if err := json.NewDecoder(resp.Body).Decode(&ctr); err != nil {
		return 0, err
	}
	return ctr.InputTokens, nil
}
