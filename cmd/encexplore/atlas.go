package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"sort"
	"unicode/utf8"

	"github.com/stazelabs/ash/internal/ledger"
)

// atlasRanges enumerates the Unicode ranges we scan for single-token runes.
// Coverage is intentionally biased toward symbol/punctuation/dingbat ranges
// (most likely to contain BPE-merged runes) and a PUA sample (to confirm
// the hypothesis that arbitrary unmerged bytes tokenize poorly).
var atlasRanges = []struct {
	Name   string
	Lo, Hi rune
}{
	{"ASCII printable", 0x0020, 0x007E},
	{"Latin-1 Supplement", 0x00A0, 0x00FF},
	{"Latin Extended-A", 0x0100, 0x017F},
	{"Latin Extended-B", 0x0180, 0x024F},
	{"IPA Extensions", 0x0250, 0x02AF},
	{"Combining Diacriticals", 0x0300, 0x036F},
	{"Greek and Coptic", 0x0370, 0x03FF},
	{"Cyrillic", 0x0400, 0x04FF},
	{"Cyrillic Supplement", 0x0500, 0x052F},
	{"Armenian", 0x0530, 0x058F},
	{"Hebrew", 0x0590, 0x05FF},
	{"Arabic", 0x0600, 0x06FF},
	{"Devanagari", 0x0900, 0x097F},
	{"Bengali", 0x0980, 0x09FF},
	{"Thai", 0x0E00, 0x0E7F},
	{"Tibetan", 0x0F00, 0x0FFF},
	{"Georgian", 0x10A0, 0x10FF},
	{"Hangul Jamo", 0x1100, 0x11FF},
	{"General Punctuation", 0x2000, 0x206F},
	{"Superscripts/Subscripts", 0x2070, 0x209F},
	{"Currency Symbols", 0x20A0, 0x20CF},
	{"Letterlike Symbols", 0x2100, 0x214F},
	{"Number Forms", 0x2150, 0x218F},
	{"Arrows", 0x2190, 0x21FF},
	{"Math Operators", 0x2200, 0x22FF},
	{"Misc Technical", 0x2300, 0x23FF},
	{"Control Pictures", 0x2400, 0x243F},
	{"Box Drawing", 0x2500, 0x257F},
	{"Block Elements", 0x2580, 0x259F},
	{"Geometric Shapes", 0x25A0, 0x25FF},
	{"Misc Symbols", 0x2600, 0x26FF},
	{"Dingbats", 0x2700, 0x27BF},
	{"Misc Math A", 0x27C0, 0x27EF},
	{"Supp Arrows A", 0x27F0, 0x27FF},
	{"Supp Arrows B", 0x2900, 0x297F},
	{"Misc Math B", 0x2980, 0x29FF},
	{"Supp Math Operators", 0x2A00, 0x2AFF},
	{"CJK Symbols/Punctuation", 0x3000, 0x303F},
	{"Hiragana", 0x3040, 0x309F},
	{"Katakana", 0x30A0, 0x30FF},
	{"Bopomofo", 0x3100, 0x312F},
	{"Hangul Compat Jamo", 0x3130, 0x318F},
	{"CJK Strokes", 0x31C0, 0x31EF},
	{"Katakana Phonetic Ext", 0x31F0, 0x31FF},
	{"Enclosed CJK Letters", 0x3200, 0x32FF},
	{"CJK Compatibility", 0x3300, 0x33FF},
	{"CJK Unified Ext A", 0x3400, 0x4DBF},
	{"CJK Unified Ideographs", 0x4E00, 0x9FFF},
	{"Hangul Syllables", 0xAC00, 0xD7AF},
	{"Private Use Area sample", 0xE000, 0xE0FF},
	{"CJK Compat Ideographs", 0xF900, 0xFAFF},
	{"Half/Fullwidth Forms", 0xFF00, 0xFFEF},
	{"Math Alphanumeric Symbols", 0x1D400, 0x1D7FF},
	{"Latin Ext-D", 0xA720, 0xA7FF},
	{"Yijing Hexagram", 0x4DC0, 0x4DFF},
	{"Misc Symbols/Arrows", 0x2B00, 0x2BFF},
	{"Latin Ext Additional", 0x1E00, 0x1EFF},
	{"Greek Extended", 0x1F00, 0x1FFF},
	{"Phonetic Extensions", 0x1D00, 0x1D7F},
	{"Vertical Forms", 0xFE10, 0xFE1F},
	{"Small Form Variants", 0xFE50, 0xFE6F},
	{"Variation Selectors", 0xFE00, 0xFE0F},
	{"Spacing Modifier Letters", 0x02B0, 0x02FF},
	{"Emoticons", 0x1F600, 0x1F64F},
	{"Misc Symbols/Pictographs", 0x1F300, 0x1F5FF},
	{"Transport/Map", 0x1F680, 0x1F6FF},
	{"Supp Symbols/Pictographs", 0x1F900, 0x1F9FF},
}

type atlasEntry struct {
	Codepoint rune
	Char      string
	Bytes     int
	Tokens    int
	Range     string
}

func runAtlas(args []string) {
	fs := flag.NewFlagSet("atlas", flag.ExitOnError)
	out := fs.String("out", "testdata/single_token_runes.txt", "output path")
	includeMulti := fs.Bool("all", false, "include multi-token runes in output (for analysis)")
	_ = fs.Parse(args)

	counter, err := ledger.NewCounter()
	if err != nil {
		die("counter: %v", err)
	}

	var entries []atlasEntry
	rangeCounts := map[string]struct{ single, total int }{}

	for _, rng := range atlasRanges {
		for cp := rng.Lo; cp <= rng.Hi; cp++ {
			if !utf8.ValidRune(cp) {
				continue
			}
			s := string(cp)
			n := counter.Count(s)
			rc := rangeCounts[rng.Name]
			rc.total++
			if n == 1 {
				rc.single++
			}
			rangeCounts[rng.Name] = rc
			if n == 1 || *includeMulti {
				entries = append(entries, atlasEntry{
					Codepoint: cp,
					Char:      s,
					Bytes:     utf8.RuneLen(cp),
					Tokens:    n,
					Range:     rng.Name,
				})
			}
		}
	}

	f, err := os.Create(*out)
	if err != nil {
		die("create %s: %v", *out, err)
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	defer w.Flush()

	fmt.Fprintf(w, "# single-token Unicode atlas (cl100k_base)\n")
	fmt.Fprintf(w, "# columns: codepoint\\tchar\\tbytes\\ttokens\\trange\n")
	fmt.Fprintf(w, "# scanned %d ranges; %d single-token entries below\n",
		len(atlasRanges), countSingle(entries))
	fmt.Fprintln(w)
	fmt.Fprintln(w, "## summary by range")
	for _, rng := range atlasRanges {
		rc := rangeCounts[rng.Name]
		fmt.Fprintf(w, "# %-32s  %4d / %4d single-token  (U+%04X..U+%04X)\n",
			rng.Name, rc.single, rc.total, rng.Lo, rng.Hi)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "## entries")

	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].Codepoint < entries[j].Codepoint
	})
	for _, e := range entries {
		fmt.Fprintf(w, "U+%04X\t%s\t%d\t%d\t%s\n",
			e.Codepoint, e.Char, e.Bytes, e.Tokens, e.Range)
	}

	fmt.Fprintf(os.Stderr, "atlas: %d single-token runes -> %s\n", countSingle(entries), *out)
}

func countSingle(entries []atlasEntry) int {
	n := 0
	for _, e := range entries {
		if e.Tokens == 1 {
			n++
		}
	}
	return n
}

func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "encexplore: "+format+"\n", args...)
	os.Exit(1)
}
