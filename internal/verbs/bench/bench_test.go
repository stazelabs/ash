package bench

import "testing"

// TestParseArgs_WireShape verifies that the limit int arg accepts a
// string-typed value (the wire shape from CLI parseFlags) and rejects
// garbage. Guards against a future implementation skipping argutil and
// silently breaking the string→int coercion path.
func TestParseArgs_WireShape(t *testing.T) {
	a, perr := ParseArgs(map[string]any{"limit": "5"})
	if perr != nil {
		t.Fatalf("string limit rejected: %v", perr)
	}
	if a.Limit != 5 {
		t.Errorf("limit: got %d, want 5", a.Limit)
	}
	_, perr = ParseArgs(map[string]any{"limit": "abc"})
	if perr == nil {
		t.Error("expected error for limit=abc")
	}
}
