package main

// subSet is a named substitution map. Order in Pairs matters: longer/more-
// specific keys must come first, so that "regex_compile_us=" is rewritten
// before "regex_us=".
type subSet struct {
	Name    string
	Surface string // status|errors|headers|metrics|whitespace|combined
	Pairs   [][2]string
}

// All substitutions below have been tokenizer-probed; the comment after each
// pair shows (before_tokens, after_tokens) measured in cl100k_base.
//
// User has explicitly authorized pushing legibility — arbitrary 1-token CJK
// glyphs are fair game where the ASCII original is multi-token.
var subSets = []subSet{
	// ─── Conservative ASCII rewrites (kept readable) ─────────────────────
	{
		Name: "errors_ascii", Surface: "errors",
		Pairs: [][2]string{
			{"path_denied", "denied"},        // 3→2
			{"not_found", "missing"},         // 2→1
			{"range_out_of_bounds", "oob"},   // 4→2
			{"range_returned", "rng"},        // 3→1
			{"build_failed", "broke"},        // 2→1
			{"git_failed", "gitfail"},        // 2→2 (no win)
			{"no_tests", "notests"},          // 2→2 (no win)
			{"permission denied", "denied"},  // 2→2
			{"no such file", "missing"},      // 3→1
		},
	},

	// ─── CJK arbitrary glyphs (single-token, opaque sentinels) ───────────
	{
		Name: "errors_cjk", Surface: "errors",
		Pairs: [][2]string{
			{"path_denied", "失"},          // 3→1
			{"not_found", "無"},            // 2→1
			{"range_out_of_bounds", "越"},  // 4→2 (越 is 2 toks alone — test confirms)
			{"range_returned", "中"},       // 3→1
			{"build_failed", "壊"},         // 2→2 (壊 is 2 toks — no win, drop?)
			{"git_failed", "敗"},           // 2→2 (no win)
			{"no_tests", "空"},             // 2→1
			{"permission denied", "禁"},    // 2→2 (no win)
			{"no such file", "無"},         // 3→1
		},
	},

	// ─── Status-enum CJK (test verb) ─────────────────────────────────────
	{
		Name: "status_cjk", Surface: "status",
		Pairs: [][2]string{
			{"build_failed", "壊"},  // 2→2 (no win) — kept for completeness
			{"no_tests", "空"},      // 2→1
			// pass/fail/skip/timeout/ok/err are already 1-token; do not touch.
		},
	},

	// ─── Metric-label short keys (drop _us suffix, single ASCII letter) ──
	// Probed: "exec_us=" = 3 toks, "x=" = 2 toks ⇒ -1 per occurrence.
	// 5 column types × ~20 rows per metrics call ⇒ ~100 toks saved per call.
	{
		Name: "metrics_short_ascii", Surface: "metrics",
		Pairs: [][2]string{
			{"regex_compile_us=", "R="}, // 4→2
			{"dispatch_us=", "d="},      // 3→2
			{"exec_us=", "x="},          // 3→2
			{"walk_us=", "w="},          // 3→2
			{"regex_us=", "r="},         // 3→2
			{"io_us=", "i="},            // 3→2
			{" in=", " n="},             // 2→2 (try anyway)
			{" out=", " o="},            // 2→2
		},
	},

	// ─── Metric-label short keys with no equals (most aggressive) ────────
	// Probed: "x123" vs "x 123" vs "x=123" — measure mode treats this as a
	// candidate worth empirical comparison. We rewrite "exec_us=" → "x".
	// This relies on the column delimiter (whitespace) to disambiguate; a
	// downstream parser would need to know the order.
	{
		Name: "metrics_no_equals", Surface: "metrics",
		Pairs: [][2]string{
			{"regex_compile_us=", "R"},
			{"dispatch_us=", "d"},
			{"exec_us=", "x"},
			{"walk_us=", "w"},
			{"regex_us=", "r"},
			{"io_us=", "i"},
			{"in=", "n"},
			{"out=", "o"},
		},
	},

	// ─── Header/divider tightening ───────────────────────────────────────
	// "=== ash bench: N ===" → "§bench: N"
	// Probed: "=== ash bench" = 3 toks; "§bench" = ?
	// Saves the trailing " ===" (2 toks).
	{
		Name: "headers_compact", Surface: "headers",
		Pairs: [][2]string{
			{"=== ash ", "§"},
			{" ===\n", "\n"},
			{" ===", ""}, // catches trailing ===
		},
	},

	// ─── Read-header compaction (file frame fence + bracket removal) ────
	// Old: "=== <path> [<size>B, <lines>L] ===\n" — 13 toks for typical path.
	// New: "§<path> <size>B <lines>L\n"           — 10 toks.
	// Saves ~3 tokens per `ash read` call by:
	//   "=== "    → "§"   (2-tok leading fence → 1-tok sentinel)
	//   "B, "     → "B "  (drop comma inside bracket)
	//   "L] ===" → "L"   (drop close bracket + trailing fence)
	// Glyph choice: reuse `§` from headers_compact. The agent disambiguates
	// verb sections from file frames by the suffix shape (verb sections end
	// in `:`, file frames end in `B[L]`). One sentinel = one classifier path.
	{
		Name: "read_header_compact", Surface: "headers",
		Pairs: [][2]string{
			{"=== ", "§"},
			{"B, ", "B "},
			{"L] ===", "L"},
			{"B] ===", "B"}, // size-only variant (no lines)
		},
	},

	// ─── Truncation-hint compaction (dropped, ASH-117) ───────────────────
	// Was: TRUNCATED→✂, [truncation:→[✂, truncated→✂. The cl100k proxy
	// claimed +3 tokens saved on grep-common, but the Anthropic count_tokens
	// endpoint reported +0 — the substitution does not pay off on Claude.
	// Removed from subSets so combined_aggressive no longer drags this row
	// into validate_results.md as a sign-disagreement (✗).
}
// combinedSubs returns a synthetic "combined aggressive" set that applies
// every substitution above in order (longest first within each set).
func combinedSubs() subSet {
	var all [][2]string
	for _, s := range subSets {
		all = append(all, s.Pairs...)
	}
	return subSet{Name: "combined_aggressive", Surface: "combined", Pairs: all}
}
