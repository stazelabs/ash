package help

import (
	"testing"

	"github.com/stazelabs/ash/internal/ledger"
	"github.com/stazelabs/ash/internal/proto"
)

// TestNoArgTokenBudget guards against PrettyResponse regressing to full-schema
// output for the no-arg form. ASH-73 reduced ash help (no args) from ~3700
// tokens to ~700 by emitting one-liner per verb instead of full arg schemas.
// Budget is set at 1500 to absorb future verb additions while catching any
// revert to full-schema output.
func TestNoArgTokenBudget(t *testing.T) {
	result, perr := Run(&Args{Verb: ""}, nil)
	if perr != nil {
		t.Fatalf("Run: %v", perr)
	}
	rsp := &proto.Response{OK: true, Data: proto.MustData(result)}
	pretty := PrettyResponse(nil, rsp)

	counter, err := ledger.NewCounter()
	if err != nil {
		t.Fatalf("NewCounter: %v", err)
	}
	got := counter.Count(pretty)
	const budget = 1500
	if got > budget {
		t.Errorf("ash help (no args) = %d tokens, want <= %d\n\noutput:\n%s", got, budget, pretty)
	}
}
