package help

import (
	"strings"
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

// TestVerboseSurfacesLong guards against the msgpack:"-" regression that
// silently stripped Long off the wire (ASH-144). Verifies that for at least
// one well-known Long-only string ("@PATH" — appears in edit --old/--new
// Long bodies but not in their concise Description), verbose mode includes
// it and default mode does not.
//
// Post-ASH-147: Long is gated on Args.Verbose at Run, not at PrettyResponse,
// so the test runs Run twice and verifies both the wire-level strip (no
// Long bytes in the encoded default-mode Data) and the pretty render.
func TestVerboseSurfacesLong(t *testing.T) {
	conciseResult, perr := Run(&Args{Verb: "edit", Verbose: false}, nil)
	if perr != nil {
		t.Fatalf("Run concise: %v", perr)
	}
	for _, vs := range conciseResult.Verbs {
		for _, a := range vs.Args {
			if a.Long != "" {
				t.Errorf("Verbose=false Run left Long=%q on arg %s/%s", a.Long, vs.Verb, a.Name)
			}
		}
	}
	conciseRsp := &proto.Response{OK: true, Data: proto.MustData(conciseResult)}
	concise := PrettyResponse(&proto.Request{Verb: "help", Args: map[string]any{"verb": "edit"}}, conciseRsp)
	if strings.Contains(concise, "@PATH") {
		t.Errorf("default (verbose=false) help leaked Long marker @PATH:\n%s", concise)
	}

	verboseResult, perr := Run(&Args{Verb: "edit", Verbose: true}, nil)
	if perr != nil {
		t.Fatalf("Run verbose: %v", perr)
	}
	sawLong := false
	for _, vs := range verboseResult.Verbs {
		for _, a := range vs.Args {
			if a.Long != "" {
				sawLong = true
				break
			}
		}
	}
	if !sawLong {
		t.Fatalf("Verbose=true Run returned no Long descriptions; expected at least one")
	}
	verboseRsp := &proto.Response{OK: true, Data: proto.MustData(verboseResult)}
	verbose := PrettyResponse(&proto.Request{Verb: "help", Args: map[string]any{"verb": "edit", "verbose": true}}, verboseRsp)
	if !strings.Contains(verbose, "@PATH") {
		t.Errorf("verbose=true help missing Long marker @PATH:\n%s", verbose)
	}
}

