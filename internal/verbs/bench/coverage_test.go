package bench_test

import (
	"testing"

	"github.com/stazelabs/ash/internal/bench"
	"github.com/stazelabs/ash/internal/verbs"
)

// TestEveryMeasuredVerbHasACase fails when bench.MeasuredVerbs lists a
// verb with no entry in bench.Cases. Forces case coverage to grow with
// the verb surface.
func TestEveryMeasuredVerbHasACase(t *testing.T) {
	counts := map[string]int{}
	for _, c := range bench.Cases {
		counts[c.Verb]++
	}
	for _, v := range bench.MeasuredVerbs {
		if counts[v] == 0 {
			t.Errorf("verb %q is in MeasuredVerbs but has no case in bench.Cases — add one", v)
		}
	}
}

// TestEveryRegisteredVerbIsClassified makes sure every live verb is
// either bench-meaningful (MeasuredVerbs) or has an explicit exemption
// (ExemptVerbs). New verbs that ship without one of these classifications
// trip this test, prompting the author to decide which list they belong in.
func TestEveryRegisteredVerbIsClassified(t *testing.T) {
	measured := map[string]bool{}
	for _, v := range bench.MeasuredVerbs {
		measured[v] = true
	}
	for verbName := range verbs.PrettyHandlers() {
		if measured[verbName] {
			continue
		}
		if _, ok := bench.ExemptVerbs[verbName]; ok {
			continue
		}
		t.Errorf("verb %q is registered but appears in neither MeasuredVerbs nor ExemptVerbs", verbName)
	}
}

// TestNoStaleExemptions catches ExemptVerbs entries that point at
// verbs which no longer exist (e.g. removed during a refactor).
func TestNoStaleExemptions(t *testing.T) {
	registered := verbs.PrettyHandlers()
	for verbName := range bench.ExemptVerbs {
		if _, ok := registered[verbName]; !ok {
			t.Errorf("ExemptVerbs entry %q references a non-existent verb", verbName)
		}
	}
}
