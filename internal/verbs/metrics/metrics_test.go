package metrics

import "testing"

// TestParseArgs_WireShape verifies that the last int arg accepts a
// string-typed value (the wire shape from CLI parseFlags) and rejects
// garbage. Guards against a future implementation skipping argutil and
// silently breaking the string→int coercion path.
func TestParseArgs_WireShape(t *testing.T) {
	a, perr := ParseArgs(map[string]any{"last": "10"})
	if perr != nil {
		t.Fatalf("string last rejected: %v", perr)
	}
	if a.Last != 10 {
		t.Errorf("last: got %d, want 10", a.Last)
	}
	_, perr = ParseArgs(map[string]any{"last": "abc"})
	if perr == nil {
		t.Error("expected error for last=abc")
	}
}
