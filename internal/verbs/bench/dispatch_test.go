package bench

import (
	"strings"
	"testing"
)

// RunWithDeps switches on Args fields. With an empty Deps, each branch
// returns its own config error — that's enough to verify routing
// without standing up a real ledger or registry. Each case asserts the
// error MESSAGE contains the verb-flag's specific config message, so a
// future regression that flipped the switch order would surface.
func TestRunWithDeps_DispatchErrors(t *testing.T) {
	cases := []struct {
		name string
		args Args
		want string
	}{
		{name: "list_needs_ledger", args: Args{List: true}, want: "bench --list: ledger not wired"},
		{name: "compare_needs_ledger", args: Args{CompareA: "x", CompareB: "y"}, want: "bench --compare: ledger not wired"},
		{name: "record_baseline_needs_project_root", args: Args{RecordBaseline: true}, want: "record_baseline: project_root not wired"},
		{name: "export_md_needs_ledger", args: Args{ExportMd: true}, want: "export_md: ledger not wired"},
		{name: "record_micro_needs_project_root", args: Args{RecordMicro: true}, want: "record_micro: project_root not wired"},
		{name: "diff_micro_needs_project_root", args: Args{DiffMicro: true}, want: "diff_micro: project_root not wired"},
		{name: "micro_needs_deps", args: Args{Micro: true}, want: "bench --micro: deps not wired"},
		{name: "standard_needs_deps", args: Args{}, want: "bench: deps not wired"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, perr := RunWithDeps(Deps{}, &c.args)
			if perr == nil {
				t.Fatal("expected config error")
			}
			if !strings.Contains(perr.Msg, c.want) {
				t.Errorf("error msg: got %q, want substring %q", perr.Msg, c.want)
			}
			if perr.Code != "config" {
				t.Errorf("expected Code=config, got %q", perr.Code)
			}
		})
	}
}

// runStandard's only error path with empty Deps is "deps not wired".
// Already covered by TestRunWithDeps_DispatchErrors above, but call it
// directly so the test file documents both entry points.
func TestRunStandard_RejectsEmptyDeps(t *testing.T) {
	_, perr := runStandard(Deps{}, &Args{})
	if perr == nil || perr.Code != "config" {
		t.Errorf("expected config error, got %v", perr)
	}
}

func TestRunRecordBaseline_RejectsEmptyProjectRoot(t *testing.T) {
	_, perr := runRecordBaseline(Deps{}, &Args{})
	if perr == nil || perr.Code != "config" {
		t.Errorf("expected config error, got %v", perr)
	}
}
