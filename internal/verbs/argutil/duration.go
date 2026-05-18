package argutil

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ParseDuration extends time.ParseDuration with three convenience
// units that the standard library rejects (ASH-171):
//
//   - "d"  — days (24h)
//   - "w"  — weeks (7×24h)
//   - "mo" — months (30×24h, approximate)
//
// All time.ParseDuration units (ns, µs, ms, s, m, h) still work. The
// month value is the calendar approximation Linux's at(1) uses; an
// honest calendar-aware duration (variable-length months, DST-aware)
// is its own project and out of scope.
//
// Suffix matching is longest-first so "1mo" parses as 1 month, not as
// "1m" (1 minute) + leftover "o". Custom units accept only positive
// integer counts (no decimals) — same constraint the previous per-verb
// helpers enforced.
func ParseDuration(s string) (time.Duration, error) {
	if s == "" {
		return 0, errors.New("empty duration")
	}
	if v, ok, err := tryCustom(s, "mo", 30*24*time.Hour); ok {
		return v, err
	}
	if v, ok, err := tryCustom(s, "w", 7*24*time.Hour); ok {
		return v, err
	}
	if v, ok, err := tryCustom(s, "d", 24*time.Hour); ok {
		return v, err
	}
	return time.ParseDuration(s)
}

// tryCustom returns (value, true, nil) when s has the given suffix and
// parses cleanly; (0, true, err) when the suffix matches but the
// integer prefix is malformed; (0, false, nil) when the suffix doesn't
// apply at all — caller falls through to the next attempt.
func tryCustom(s, suffix string, unit time.Duration) (time.Duration, bool, error) {
	if !strings.HasSuffix(s, suffix) {
		return 0, false, nil
	}
	n, err := strconv.Atoi(s[:len(s)-len(suffix)])
	if err != nil || n <= 0 {
		return 0, true, fmt.Errorf("invalid %s value %q", suffix, s)
	}
	return time.Duration(n) * unit, true, nil
}
