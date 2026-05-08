// Package verbs is the registration seam for all ash verbs. Both the
// daemon and the client import this package; the daemon uses Runners()
// to dispatch and Truncated; both use PrettyHandlers() for rendering.
//
// Adding a new verb is now an import + one entry in two maps in this
// file. There is no longer a switch statement in cmd/ashd or cmd/ash to
// keep in sync.
package verbs

import (
	"github.com/stazelabs/ash/internal/ledger"
	"github.com/stazelabs/ash/internal/proto"
	"github.com/stazelabs/ash/internal/verbs/bench"
	"github.com/stazelabs/ash/internal/verbs/find"
	"github.com/stazelabs/ash/internal/verbs/git"
	"github.com/stazelabs/ash/internal/verbs/grep"
	"github.com/stazelabs/ash/internal/verbs/help"
	"github.com/stazelabs/ash/internal/verbs/metrics"
	"github.com/stazelabs/ash/internal/verbs/read"
	"github.com/stazelabs/ash/internal/verbs/report"
	"github.com/stazelabs/ash/internal/verbs/stat"
	"github.com/stazelabs/ash/internal/verbs/write"
)

// Pretty renders a single response. The signature is identical for every
// verb so daemon and client share one map.
type Pretty = func(req *proto.Request, rsp *proto.Response) string

// Runner is a verb's daemon-side execution. Run takes the loosely-typed
// args from the wire, parses them, and executes; the tracer accumulates
// sub-phase latency the verb chooses to instrument (nil-safe). Truncated
// extracts the truncation flag from the typed result so the wire metrics
// can record it. Truncated is optional; nil means "this verb has no
// truncation concept".
type Runner struct {
	Run       func(args map[string]any, tr *proto.Tracer) (any, *proto.Error)
	Truncated func(data any) bool
}

// PrettyHandlers returns the renderer for every live verb. Used by both
// the client (for terminal output) and the daemon (for token counting on
// the canonical pretty form).
func PrettyHandlers() map[string]Pretty {
	return map[string]Pretty{
		"read":    read.PrettyResponse,
		"find":    find.PrettyResponse,
		"grep":    grep.PrettyResponse,
		"git":     git.PrettyResponse,
		"metrics": metrics.PrettyResponse,
		"report":  report.PrettyResponse,
		"help":    help.PrettyResponse,
		"stat":    stat.PrettyResponse,
		"write":   write.PrettyResponse,
		"bench":   bench.PrettyResponse,
	}
}

// Runners returns the daemon execution registry. The ledger is captured
// by the metrics runner since metrics queries the ledger directly. Pass a
// real *ledger.Ledger; passing nil will panic when metrics fires.
//
// The bench runner closes over the registry itself + the pretty handlers
// so it can dispatch any verb in-process and tokenize its canonical
// response. The closure binds the maps by reference; by the time bench
// fires the maps are fully populated, so self-dispatch (`ash bench` →
// `ash bench`) works too — though it's a degenerate case.
func Runners(led *ledger.Ledger) map[string]Runner {
	pretty := PrettyHandlers()
	runners := map[string]Runner{
		"read": {
			Run: func(args map[string]any, tr *proto.Tracer) (any, *proto.Error) {
				a, perr := read.ParseArgs(args)
				if perr != nil {
					return nil, perr
				}
				return read.Run(a, tr)
			},
			Truncated: func(d any) bool {
				if r, ok := d.(*read.Result); ok {
					return r.Truncated
				}
				return false
			},
		},
		"find": {
			Run: func(args map[string]any, tr *proto.Tracer) (any, *proto.Error) {
				a, perr := find.ParseArgs(args)
				if perr != nil {
					return nil, perr
				}
				return find.Run(a, tr)
			},
			Truncated: func(d any) bool {
				if r, ok := d.(*find.Result); ok {
					return r.Truncated
				}
				return false
			},
		},
		"grep": {
			Run: func(args map[string]any, tr *proto.Tracer) (any, *proto.Error) {
				a, perr := grep.ParseArgs(args)
				if perr != nil {
					return nil, perr
				}
				return grep.Run(a, tr)
			},
			Truncated: func(d any) bool {
				if r, ok := d.(*grep.Result); ok {
					return r.Truncated
				}
				return false
			},
		},
		"git": {
			Run: func(args map[string]any, tr *proto.Tracer) (any, *proto.Error) {
				a, perr := git.ParseArgs(args)
				if perr != nil {
					return nil, perr
				}
				return git.Run(a, tr)
			},
		},
		"metrics": {
			Run: func(args map[string]any, _ *proto.Tracer) (any, *proto.Error) {
				a, perr := metrics.ParseArgs(args)
				if perr != nil {
					return nil, perr
				}
				return metrics.RunWithLedger(led, a)
			},
		},
		"report": {
			Run: func(args map[string]any, _ *proto.Tracer) (any, *proto.Error) {
				a, perr := report.ParseArgs(args)
				if perr != nil {
					return nil, perr
				}
				return report.RunWithLedger(led, a)
			},
		},
		"help": {
			Run: func(args map[string]any, tr *proto.Tracer) (any, *proto.Error) {
				a, perr := help.ParseArgs(args)
				if perr != nil {
					return nil, perr
				}
				return help.Run(a, tr)
			},
		},
		"stat": {
			Run: func(args map[string]any, tr *proto.Tracer) (any, *proto.Error) {
				a, perr := stat.ParseArgs(args)
				if perr != nil {
					return nil, perr
				}
				return stat.Run(a, tr)
			},
		},
		"write": {
			Run: func(args map[string]any, tr *proto.Tracer) (any, *proto.Error) {
				a, perr := write.ParseArgs(args)
				if perr != nil {
					return nil, perr
				}
				return write.Run(a, tr)
			},
		},
	}
	runners["bench"] = Runner{
		Run: func(args map[string]any, _ *proto.Tracer) (any, *proto.Error) {
			a, perr := bench.ParseArgs(args)
			if perr != nil {
				return nil, perr
			}
			deps := bench.Deps{
				Counter: led.Counter(),
				Run: func(verb string, vargs map[string]any) (any, *proto.Error) {
					r, ok := runners[verb]
					if !ok {
						return nil, &proto.Error{Code: "unknown_verb", Msg: "unknown verb: " + verb}
					}
					// In-process dispatch uses a throwaway tracer; bench
					// doesn't currently surface sub-phase data per case.
					return r.Run(vargs, &proto.Tracer{})
				},
				Pretty: func(verb string, req *proto.Request, rsp *proto.Response) string {
					if p, ok := pretty[verb]; ok {
						return p(req, rsp)
					}
					return proto.PrettyResponseHeader(rsp)
				},
			}
			return bench.RunWithDeps(deps, a)
		},
	}
	return runners
}
