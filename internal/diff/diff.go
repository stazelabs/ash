// Package diff provides a line-level LCS diff and unified-diff formatter.
//
// Algorithm: standard DP LCS, O(n*m) time and O(n*m) space.
// Both inputs are capped at MaxLines (4000) lines each; callers should check
// the error return and surface the cap to users rather than silently truncating.
package diff

import (
	"fmt"
	"strings"
)

const (
	MaxLines       = 4000 // per input side (ASH-32: profiled — ~30 MiB / 40ms worst case at 4000)
	DefaultContext = 3
)

// Edit is a single line in an edit script.
type Edit struct {
	Op   byte   // ' ' context, '+' insert, '-' delete
	Line string // raw line content (without trailing newline)
}

// Lines computes the minimal edit script from a to b using LCS.
// Returns an error if either slice exceeds MaxLines.
func Lines(a, b []string) ([]Edit, error) {
	if len(a) > MaxLines || len(b) > MaxLines {
		return nil, fmt.Errorf("diff inputs too large: a=%d, b=%d (max %d lines/side); reduce input size or split the file", len(a), len(b), MaxLines)
	}
	return linesLCS(a, b), nil
}

// linesLCS is the cap-free LCS implementation, separated from Lines so the
// public guard is one line and easy to audit. Useful for profiling beyond
// MaxLines without poking holes in the exported surface.
func linesLCS(a, b []string) []Edit {
	m, n := len(a), len(b)
	if m == 0 && n == 0 {
		return nil
	}

	// Build flat DP table. dp[i*(n+1)+j] = LCS length of a[:i] and b[:j].
	// Using uint16: values ≤ min(m,n) ≤ MaxLines = 4000 << 65535.
	size := (m + 1) * (n + 1)
	dp := make([]uint16, size)
	stride := n + 1

	for i := 1; i <= m; i++ {
		for j := 1; j <= n; j++ {
			if a[i-1] == b[j-1] {
				dp[i*stride+j] = dp[(i-1)*stride+(j-1)] + 1
			} else if dp[(i-1)*stride+j] >= dp[i*stride+(j-1)] {
				dp[i*stride+j] = dp[(i-1)*stride+j]
			} else {
				dp[i*stride+j] = dp[i*stride+(j-1)]
			}
		}
	}

	// Backtrack iteratively (bottom-right to top-left); edits collected in reverse.
	// Tie-break: prefer insert over delete so that after reversal, deletes
	// precede inserts in the forward script (the conventional "-+" order).
	edits := make([]Edit, 0, m+n)
	i, j := m, n
	for i > 0 || j > 0 {
		if i > 0 && j > 0 && a[i-1] == b[j-1] &&
			dp[i*stride+j] == dp[(i-1)*stride+(j-1)]+1 {
			edits = append(edits, Edit{' ', a[i-1]})
			i--
			j--
		} else if j > 0 && (i == 0 || dp[i*stride+(j-1)] >= dp[(i-1)*stride+j]) {
			edits = append(edits, Edit{'+', b[j-1]})
			j--
		} else {
			edits = append(edits, Edit{'-', a[i-1]})
			i--
		}
	}

	// Reverse to get chronological order.
	for lo, hi := 0, len(edits)-1; lo < hi; lo, hi = lo+1, hi-1 {
		edits[lo], edits[hi] = edits[hi], edits[lo]
	}
	return edits
}

// Stats returns the number of added and deleted lines in an edit script.
func Stats(edits []Edit) (additions, deletions int) {
	for _, e := range edits {
		switch e.Op {
		case '+':
			additions++
		case '-':
			deletions++
		}
	}
	return
}

// Unified formats edits as a unified diff with context lines of surrounding
// context per hunk. pathA and pathB are used in the --- / +++ header.
// Returns an empty string if there are no changes.
func Unified(edits []Edit, pathA, pathB string, context int) string {
	if context < 0 {
		context = DefaultContext
	}
	if !hasChanges(edits) {
		return ""
	}

	hunks := buildHunks(edits, context)
	var b strings.Builder
	fmt.Fprintf(&b, "--- %s\n+++ %s\n", pathA, pathB)
	for _, h := range hunks {
		writeHunk(&b, h, edits)
	}
	return b.String()
}

// SplitLines splits s into lines, stripping the trailing newline from each.
// A trailing empty line (from a file ending with \n) is omitted so that the
// caller's line count matches what users expect.
func SplitLines(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	// If the string ended with \n, strings.Split produces a trailing empty element.
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// JoinLines re-assembles lines into file content (each line gets a \n).
func JoinLines(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}

// ---- internal helpers ---------------------------------------------------

func hasChanges(edits []Edit) bool {
	for _, e := range edits {
		if e.Op != ' ' {
			return true
		}
	}
	return false
}

// hunk is a contiguous range of edits to render together.
type hunk struct {
	start, end int // indices into edits
}

func buildHunks(edits []Edit, ctx int) []hunk {
	n := len(edits)
	type changeRange struct{ lo, hi int }
	var changes []changeRange

	for i := 0; i < n; {
		if edits[i].Op == ' ' {
			i++
			continue
		}
		lo := i
		for i < n && edits[i].Op != ' ' {
			i++
		}
		changes = append(changes, changeRange{lo, i})
	}
	if len(changes) == 0 {
		return nil
	}

	// Expand each change range by ctx lines on each side.
	type expanded struct{ lo, hi int }
	exps := make([]expanded, len(changes))
	for k, c := range changes {
		lo := c.lo - ctx
		if lo < 0 {
			lo = 0
		}
		hi := c.hi + ctx
		if hi > n {
			hi = n
		}
		exps[k] = expanded{lo, hi}
	}

	// Merge overlapping expanded ranges into hunks.
	var hunks []hunk
	cur := exps[0]
	for _, e := range exps[1:] {
		if e.lo <= cur.hi {
			if e.hi > cur.hi {
				cur.hi = e.hi
			}
		} else {
			hunks = append(hunks, hunk{cur.lo, cur.hi})
			cur = e
		}
	}
	hunks = append(hunks, hunk{cur.lo, cur.hi})
	return hunks
}

func writeHunk(b *strings.Builder, h hunk, edits []Edit) {
	// Compute a-side and b-side line numbers (1-based).
	aStart, bStart := 1, 1
	aCount, bCount := 0, 0
	for i := 0; i < h.start; i++ {
		switch edits[i].Op {
		case ' ':
			aStart++
			bStart++
		case '-':
			aStart++
		case '+':
			bStart++
		}
	}
	for i := h.start; i < h.end; i++ {
		switch edits[i].Op {
		case ' ':
			aCount++
			bCount++
		case '-':
			aCount++
		case '+':
			bCount++
		}
	}
	fmt.Fprintf(b, "@@ -%d,%d +%d,%d @@\n", aStart, aCount, bStart, bCount)
	for i := h.start; i < h.end; i++ {
		b.WriteByte(edits[i].Op)
		b.WriteString(edits[i].Line)
		b.WriteByte('\n')
	}
}
