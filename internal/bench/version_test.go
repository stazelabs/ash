package bench

import (
	"regexp"
	"testing"
)

// TestCaseSetVersion_FormatAndStable pins the "cs-" + 16-hex-char
// format and verifies the same call returns the same value (sync.Once
// caching path). Format breakage would silently desync persisted
// bench rows from the case list they were measured against.
func TestCaseSetVersion_FormatAndStable(t *testing.T) {
	v := CaseSetVersion()
	re := regexp.MustCompile(`^cs-[0-9a-f]{16}$`)
	if !re.MatchString(v) {
		t.Errorf("CaseSetVersion format: got %q, want cs-XXXXXXXXXXXXXXXX", v)
	}
	if got := CaseSetVersion(); got != v {
		t.Errorf("CaseSetVersion not stable across calls: %q vs %q", v, got)
	}
}

// TestComputeCaseSetVersion_DistinctForDifferentCases exercises the
// computation path directly (sidestepping sync.Once) by calling
// computeCaseSetVersion against a mutated Cases slice. Pinning here
// guards against an aliasing bug where two different case lists hash
// to the same value.
func TestComputeCaseSetVersion_DistinctForDifferentCases(t *testing.T) {
	orig := append([]Case(nil), Cases...)
	defer func() { Cases = orig }()

	// Call computeCaseSetVersion() directly — sync.Once.Do has already
	// fired in the other test, so going through CaseSetVersion() would
	// return the cached value instead of recomputing.
	cachedCaseSetVersion = ""
	computeCaseSetVersion()
	baseline := cachedCaseSetVersion
	if baseline == "" {
		t.Fatal("computeCaseSetVersion did not populate the cache")
	}

	// Mutate: rename the first case. The hash must change.
	Cases = append([]Case(nil), orig...)
	Cases[0].Name = orig[0].Name + "_mutated"
	cachedCaseSetVersion = ""
	computeCaseSetVersion()
	if cachedCaseSetVersion == baseline {
		t.Errorf("renaming a case should change the version hash; both = %q", baseline)
	}

	// Restore Cases; recompute. Must round-trip back to the baseline.
	Cases = orig
	cachedCaseSetVersion = ""
	computeCaseSetVersion()
	if cachedCaseSetVersion != baseline {
		t.Errorf("restoring Cases should restore the hash: got %q, want %q", cachedCaseSetVersion, baseline)
	}
}
