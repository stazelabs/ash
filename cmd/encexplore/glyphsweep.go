package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/stazelabs/ash/internal/ledger"
)

// runGlyphSweep tries a list of candidate single-codepoint glyphs as the
// substitution target for the `truncation_compact` sub-set's two firing
// rules (`TRUNCATED→<g>`, `[truncation:→[<g>`). For each glyph and corpus,
// it reports the cl100k and Claude token deltas plus an agreement marker
// so the caller can decide whether a winning glyph exists.
//
// Background: ASH-117 dropped `truncation_compact` after `✂` (U+2702)
// proved to save cl100k tokens but zero Claude tokens. ASH-120 reopens the
// question — is there a single-codepoint glyph Claude tokenizes cheaper
// than the multi-byte prose? — and this is the probe.
//
// Requires ANTHROPIC_API_KEY in the environment.
func runGlyphSweep(args []string) {
	fs := flag.NewFlagSet("glyphsweep", flag.ExitOnError)
	corpusDir := fs.String("corpus", "testdata/corpus", "corpus directory")
	files := fs.String("files", "grep-common.txt,git-log.txt",
		"comma-separated corpus file basenames")
	glyphsCSV := fs.String("glyphs",
		"✂,…,▢,□,◊,▶,›,◌,◆,●,◯,◎,空,絶,斬,切,終,断",
		"comma-separated candidate glyphs")
	model := fs.String("model", "claude-sonnet-4-5", "Claude model for count_tokens")
	_ = fs.Parse(args)

	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" {
		die("ANTHROPIC_API_KEY not set")
	}

	counter, err := ledger.NewCounter()
	if err != nil {
		die("counter: %v", err)
	}

	cache := map[string]int{}
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

	corpora := strings.Split(*files, ",")
	glyphs := strings.Split(*glyphsCSV, ",")

	fmt.Println("# encexplore glyphsweep — candidate glyphs for truncation_compact")
	fmt.Println()
	fmt.Printf("Model: %s\n\n", *model)
	fmt.Println("Rules applied per glyph g:")
	fmt.Println("  1. TRUNCATED   → g")
	fmt.Println("  2. [truncation:→ [g")
	fmt.Println()
	fmt.Println("Agreement: ✓ if cl Δ and claude Δ both > 0; ✗ if they disagree in")
	fmt.Println("sign; — otherwise. A glyph is a winner only if claude Δ > 0.")
	fmt.Println()

	for _, fn := range corpora {
		fn = strings.TrimSpace(fn)
		raw, err := os.ReadFile(filepath.Join(*corpusDir, fn))
		if err != nil {
			die("read %s: %v", fn, err)
		}
		body := string(raw)
		occTrunc := strings.Count(body, "TRUNCATED")
		occBrack := strings.Count(body, "[truncation:")
		clBefore := counter.Count(body)
		claudeBefore, err := claudeCount(body)
		if err != nil {
			die("claude (%s): %v", fn, err)
		}
		fmt.Printf("## %s (TRUNCATED×%d, [truncation:×%d, cl=%d, claude=%d)\n\n",
			fn, occTrunc, occBrack, clBefore, claudeBefore)
		fmt.Println("| glyph | codepoint | cl Δ | claude Δ | agreement |")
		fmt.Println("|---|---|---:|---:|:---:|")

		for _, g := range glyphs {
			g = strings.TrimSpace(g)
			if g == "" {
				continue
			}
			out := body
			out = strings.ReplaceAll(out, "TRUNCATED", g)
			out = strings.ReplaceAll(out, "[truncation:", "["+g)
			clAfter := counter.Count(out)
			claudeAfter, err := claudeCount(out)
			if err != nil {
				die("claude (%s/%s): %v", fn, g, err)
			}
			clDelta := clBefore - clAfter
			claudeDelta := claudeBefore - claudeAfter
			marker := "—"
			if clDelta > 0 && claudeDelta <= 0 {
				marker = "✗"
			} else if clDelta < 0 && claudeDelta > 0 {
				marker = "✗"
			} else if clDelta > 0 && claudeDelta > 0 {
				marker = "✓"
			} else if clDelta < 0 && claudeDelta < 0 {
				marker = "✓neg"
			}
			cp := ""
			for _, r := range g {
				if cp != "" {
					cp += "+"
				}
				cp += fmt.Sprintf("U+%04X", r)
			}
			fmt.Printf("| %s | %s | %+d | %+d | %s |\n", g, cp, clDelta, claudeDelta, marker)
			fmt.Fprintf(os.Stderr, "glyphsweep: %s %s  cl Δ=%+d  claude Δ=%+d  %s\n",
				fn, g, clDelta, claudeDelta, marker)
		}
		fmt.Println()
	}
}
