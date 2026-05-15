package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/stazelabs/ash/internal/ledger"
)

// runTruncBody probes candidate body-shape compactions for the
// `[truncation: …]` hint emitted by find/grep/read/git log/diff (ASH-121).
//
// Per ticket scope: probe candidate compactions against count_tokens to
// decide which body shape gives the best (cl100k Δ × claude Δ × agent
// utility) tradeoff. Each candidate is a full sentence-level (before, after)
// rewrite applied to the corpora that contain a real truncation hint
// (grep-common.txt, git-log.txt by default).
//
// Candidates use the existing ASH-120 `truncation_compact` glyph rewrites
// as the *baseline*; the body-shape compactions are measured on top of
// `[truncation:` already collapsed to `[…`. This mirrors what the live
// stack will look like once the body change ships alongside the existing
// glyph compaction.
//
// Requires ANTHROPIC_API_KEY in the environment.
func runTruncBody(args []string) {
	fs := flag.NewFlagSet("truncbody", flag.ExitOnError)
	corpusDir := fs.String("corpus", "testdata/corpus", "corpus directory")
	files := fs.String("files", "grep-common.txt,git-log.txt",
		"comma-separated corpus file basenames")
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

	// Baseline rewrites carried into every candidate: the ASH-120
	// `truncation_compact` glyph compaction. The body-shape candidates
	// then layer on top of this. (Two passes per call: glyph first, then
	// body, mirroring the order rewrites would apply in subSets.)
	glyphPairs := [][2]string{
		{"TRUNCATED", "…"},
		{"[truncation:", "[…"},
	}

	// Candidate body-shape rewrites. Each candidate has a name and a list
	// of (before, after) full-sentence pairs covering the two real hint
	// shapes in the probe corpora:
	//
	//   grep:    "narrow with --glob, --mpf, --exclude, or raise --max"
	//   git log: "narrow with --range/--author/--since/--pathspec, or raise --limit"
	//
	// `drop_ceiling` also strips the trailing " (max N)" clause.
	type candidate struct {
		Name        string
		Description string
		Pairs       [][2]string
	}
	candidates := []candidate{
		{
			Name:        "baseline",
			Description: "no body change (just ASH-120 glyph compaction)",
			Pairs:       nil,
		},
		{
			Name:        "drop_raise_clause",
			Description: `drop the entire ", or raise --X (max N)" tail (loses ceiling AND raise hint)`,
			Pairs: [][2]string{
				{", or raise --max (max 4096)", ""},
				{", or raise --limit (max 200)", ""},
			},
		},
		{
			Name:        "drop_ceiling_only",
			Description: `strip just the " (max N)" parenthetical; keep "or raise --X"`,
			Pairs: [][2]string{
				{" (max 4096)", ""},
				{" (max 200)", ""},
			},
		},
		{
			Name:        "clump_flags",
			Description: `replace ", " between flags with "/"; collapse ", or raise --X" → "/--X"`,
			Pairs: [][2]string{
				{"narrow with --glob, --mpf, --exclude, or raise --max", "narrow with --glob/--mpf/--exclude/--max"},
				{"narrow with --range/--author/--since/--pathspec, or raise --limit", "narrow with --range/--author/--since/--pathspec/--limit"},
			},
		},
		{
			Name:        "drop_narrow",
			Description: `drop the "narrow with " verb prefix`,
			Pairs: [][2]string{
				{"narrow with --glob, --mpf, --exclude, or raise --max", "--glob, --mpf, --exclude, or raise --max"},
				{"narrow with --range/--author/--since/--pathspec, or raise --limit", "--range/--author/--since/--pathspec, or raise --limit"},
			},
		},
		{
			Name:        "compact",
			Description: `clump_flags + drop_narrow (keep ceiling)`,
			Pairs: [][2]string{
				{"narrow with --glob, --mpf, --exclude, or raise --max", "--glob/--mpf/--exclude/--max"},
				{"narrow with --range/--author/--since/--pathspec, or raise --limit", "--range/--author/--since/--pathspec/--limit"},
			},
		},
		{
			Name:        "compact_no_raise",
			Description: `clump_flags + drop_narrow + drop_raise_clause (no flag for cap)`,
			Pairs: [][2]string{
				{"narrow with --glob, --mpf, --exclude, or raise --max (max 4096)", "--glob/--mpf/--exclude"},
				{"narrow with --range/--author/--since/--pathspec, or raise --limit (max 200)", "--range/--author/--since/--pathspec"},
			},
		},
		{
			Name:        "compact_keep_raise",
			Description: `clump_flags + drop_narrow + drop_ceiling_only (keeps --X-as-flag clue)`,
			Pairs: [][2]string{
				{"narrow with --glob, --mpf, --exclude, or raise --max (max 4096)", "--glob/--mpf/--exclude/--max"},
				{"narrow with --range/--author/--since/--pathspec, or raise --limit (max 200)", "--range/--author/--since/--pathspec/--limit"},
			},
		},
		{
			Name:        "drop_ceiling_only_keep_narrow",
			Description: `keep "narrow with" + comma list; just drop the " (max N)" parenthetical`,
			Pairs: [][2]string{
				{" (max 4096)", ""},
				{" (max 200)", ""},
			},
		},
	}

	corpora := strings.Split(*files, ",")

	fmt.Println("# encexplore truncbody — body-shape compactions for `[truncation: …]`")
	fmt.Println()
	fmt.Printf("Model: %s\n\n", *model)
	fmt.Println("Each candidate is layered on top of the ASH-120 glyph compaction")
	fmt.Println("(`TRUNCATED`→`…`, `[truncation:`→`[…`). Baseline row shows the")
	fmt.Println("glyph-only delta; subsequent rows add the body-shape rewrite.")
	fmt.Println()
	fmt.Println("Agreement: ✓ if cl Δ and claude Δ both > 0; ✗ if they disagree in")
	fmt.Println("sign; — otherwise.")
	fmt.Println()

	for _, fn := range corpora {
		fn = strings.TrimSpace(fn)
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
		fmt.Printf("## %s (cl=%d, claude=%d)\n\n", fn, clBefore, claudeBefore)
		fmt.Println("| candidate | description | cl Δ | claude Δ | agreement |")
		fmt.Println("|---|---|---:|---:|:---:|")

		for _, c := range candidates {
			out := body
			for _, p := range glyphPairs {
				out = strings.ReplaceAll(out, p[0], p[1])
			}
			for _, p := range c.Pairs {
				out = strings.ReplaceAll(out, p[0], p[1])
			}
			clAfter := counter.Count(out)
			claudeAfter, err := claudeCount(out)
			if err != nil {
				die("claude (%s/%s): %v", fn, c.Name, err)
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
			fmt.Printf("| %s | %s | %+d | %+d | %s |\n",
				c.Name, c.Description, clDelta, claudeDelta, marker)
			fmt.Fprintf(os.Stderr, "truncbody: %s %-22s cl Δ=%+d  claude Δ=%+d  %s\n",
				fn, c.Name, clDelta, claudeDelta, marker)
		}
		fmt.Println()
	}
}
