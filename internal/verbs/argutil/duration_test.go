package argutil

import (
	"testing"
	"time"
)

func TestParseDuration_GoUnits(t *testing.T) {
	cases := map[string]time.Duration{
		"100ns": 100 * time.Nanosecond,
		"5ms":   5 * time.Millisecond,
		"15m":   15 * time.Minute,
		"1h":    time.Hour,
		"2h30m": 2*time.Hour + 30*time.Minute,
	}
	for in, want := range cases {
		got, err := ParseDuration(in)
		if err != nil {
			t.Errorf("ParseDuration(%q): unexpected error: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ParseDuration(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestParseDuration_CustomUnits(t *testing.T) {
	cases := map[string]time.Duration{
		"1d":   24 * time.Hour,
		"7d":   7 * 24 * time.Hour,
		"1w":   7 * 24 * time.Hour,
		"2w":   2 * 7 * 24 * time.Hour,
		"1mo":  30 * 24 * time.Hour,
		"3mo":  3 * 30 * 24 * time.Hour,
		"30d":  30 * 24 * time.Hour, // d=24h, so 30d == 1mo (the calendar approximation)
	}
	for in, want := range cases {
		got, err := ParseDuration(in)
		if err != nil {
			t.Errorf("ParseDuration(%q): unexpected error: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ParseDuration(%q) = %v, want %v", in, got, want)
		}
	}
}

// TestParseDuration_MoBeforeM guards the longest-suffix-first invariant:
// "1mo" must NOT be misparsed as "1m" (one minute) plus a leftover "o".
func TestParseDuration_MoBeforeM(t *testing.T) {
	got, err := ParseDuration("1mo")
	if err != nil {
		t.Fatalf("ParseDuration(\"1mo\"): %v", err)
	}
	if got != 30*24*time.Hour {
		t.Errorf("\"1mo\" = %v, want 720h (30 days)", got)
	}
	// And the single-letter version still resolves as Go minutes.
	got, err = ParseDuration("1m")
	if err != nil {
		t.Fatalf("ParseDuration(\"1m\"): %v", err)
	}
	if got != time.Minute {
		t.Errorf("\"1m\" = %v, want 1 minute", got)
	}
}

func TestParseDuration_Errors(t *testing.T) {
	cases := []string{
		"",        // empty
		"banana",  // no unit, no integer prefix
		"0d",      // zero rejected (preserves prior per-verb behavior)
		"-1d",     // negative rejected
		"1.5d",    // decimal rejected by strconv.Atoi
		"d",       // no integer
		"mow",     // not a known suffix in any branch (mo matches, prefix "" Atoi fails)
		"abc7d",   // garbage prefix on a real suffix
	}
	for _, in := range cases {
		if _, err := ParseDuration(in); err == nil {
			t.Errorf("ParseDuration(%q): expected error, got nil", in)
		}
	}
}
