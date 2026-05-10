package ledger

import "testing"

func TestStripPrefixes_Empty(t *testing.T) {
	if got := StripPrefixes("", nil); got != "" {
		t.Errorf("empty input: got %q", got)
	}
	if got := StripPrefixes("hello", nil); got != "hello" {
		t.Errorf("nil prefixes: got %q", got)
	}
	if got := StripPrefixes("hello", []string{}); got != "hello" {
		t.Errorf("empty prefix slice: got %q", got)
	}
}

func TestStripPrefixes_Basic(t *testing.T) {
	in := "/Users/me/proj/a.go\n/Users/me/proj/b.go"
	want := "a.go\nb.go"
	got := StripPrefixes(in, []string{"/Users/me/proj"})
	if got != want {
		t.Errorf("basic: got %q want %q", got, want)
	}
}

func TestStripPrefixes_LongestFirst(t *testing.T) {
	// "/a/b" must strip before "/a" — otherwise "/a/b/c" becomes "b/c"
	// instead of the expected "c".
	in := "/a/b/c\n/a/x"
	want := "c\nx"
	got := StripPrefixes(in, []string{"/a", "/a/b"})
	if got != want {
		t.Errorf("longest-first: got %q want %q", got, want)
	}
}

func TestStripPrefixes_NotPresent(t *testing.T) {
	in := "hello world"
	got := StripPrefixes(in, []string{"/nowhere"})
	if got != in {
		t.Errorf("absent prefix should leave input unchanged: got %q", got)
	}
}

func TestStripPrefixes_SkipsEmptyAndRoot(t *testing.T) {
	// Empty strings and "/" alone are skipped — stripping every "/" would
	// shred the response. The function should treat them as no-ops.
	in := "/Users/me/proj/a.go"
	got := StripPrefixes(in, []string{"", "/", "/Users/me/proj"})
	if got != "a.go" {
		t.Errorf("skip empty/root: got %q", got)
	}
}

func TestStripPrefixes_TokenCountDrops(t *testing.T) {
	// Cross-check that the same Counter sees fewer tokens after stripping,
	// because that is the actual contract ASH-71 cares about. Use a
	// real-ish path-heavy string so the assertion is meaningful.
	c, err := NewCounter()
	if err != nil {
		t.Fatalf("NewCounter: %v", err)
	}
	in := "/Users/me/proj/a.go\n/Users/me/proj/b.go\n/Users/me/proj/c.go"
	before := c.Count(in)
	after := c.Count(StripPrefixes(in, []string{"/Users/me/proj"}))
	if after >= before {
		t.Errorf("expected token count to drop, got before=%d after=%d", before, after)
	}
}
