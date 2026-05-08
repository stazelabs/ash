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
	"github.com/stazelabs/ash/internal/verbs/find"
	"github.com/stazelabs/ash/internal/verbs/git"
	"github.com/stazelabs/ash/internal/verbs/grep"
	"github.com/stazelabs/ash/internal/verbs/help"
	"github.com/stazelabs/ash/internal/verbs/metrics"
	"github.com/stazelabs/ash/internal/verbs/read"
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
		"help":    help.PrettyResponse,
	}
}

// Runners returns the daemon execution registry. The ledger is captured
// by the metrics runner since metrics queries the ledger directly. Pass a
// real *ledger.Ledger; passing nil will panic when metrics fires.
func Runners(led *ledger.Ledger) map[string]Runner {
	return map[string]Runner{
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
		"help": {
			Run: func(args map[string]any, tr *proto.Tracer) (any, *proto.Error) {
				a, perr := help.ParseArgs(args)
				if perr != nil {
					return nil, perr
				}
				return help.Run(a, tr)
			},
		},
	}
}
