// Package verbs is the registration seam for all ash verbs. Both the
// daemon and the client import this package; the daemon uses Runners()
// to dispatch and Truncated; both use PrettyHandlers() for rendering.
//
// Adding a new verb is now an import + one entry in two maps in this
// file. There is no longer a switch statement in cmd/ashd or cmd/ash to
// keep in sync.
package verbs

import (
	"time"

	"github.com/stazelabs/ash/internal/config"
	"github.com/stazelabs/ash/internal/ledger"
	"github.com/stazelabs/ash/internal/proto"
	"github.com/stazelabs/ash/internal/verbs/bench"
	"github.com/stazelabs/ash/internal/verbs/diff"
	"github.com/stazelabs/ash/internal/verbs/edit"
	"github.com/stazelabs/ash/internal/verbs/find"
	"github.com/stazelabs/ash/internal/verbs/git"
	"github.com/stazelabs/ash/internal/verbs/grep"
	"github.com/stazelabs/ash/internal/verbs/help"
	"github.com/stazelabs/ash/internal/verbs/hook"
	"github.com/stazelabs/ash/internal/verbs/initverb"
	"github.com/stazelabs/ash/internal/verbs/metrics"
	"github.com/stazelabs/ash/internal/verbs/read"
	"github.com/stazelabs/ash/internal/verbs/report"
	"github.com/stazelabs/ash/internal/verbs/stat"
	"github.com/stazelabs/ash/internal/verbs/test"
	"github.com/stazelabs/ash/internal/verbs/stop"
	"github.com/stazelabs/ash/internal/verbs/uninit"
	"github.com/stazelabs/ash/internal/verbs/write"
)

// Pretty renders a single response. The signature is identical for every
// verb so daemon and client share one map.
type Pretty = func(req *proto.Request, rsp *proto.Response) string

// Compact renders a compact (array-of-arrays) response for row-shaped verbs.
// Returns nil, nil for ops that are not row-shaped (e.g. git status); the
// caller falls back to json-decoded output in that case.
type Compact = func(rsp *proto.Response) (any, error)

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
		"hook":    hook.PrettyResponse,
		"stat":    stat.PrettyResponse,
		"write":   write.PrettyResponse,
		"edit":    edit.PrettyResponse,
		"diff":    diff.PrettyResponse,
		"bench":   bench.PrettyResponse,
		"test":    test.PrettyResponse,
		"init":    initverb.PrettyResponse,
		"uninit":  uninit.PrettyResponse,
		"stop":    stop.PrettyResponse,
	}
}

// CompactHandlers returns the compact (array-of-arrays) renderer for the 7
// row-shaped verbs. Verbs not in this map have no compact form; callers
// fall back to json-decoded output for those verbs.
func CompactHandlers() map[string]Compact {
	return map[string]Compact{
		"metrics": metrics.CompactResponse,
		"report":  report.CompactResponse,
		"find":    find.CompactResponse,
		"grep":    grep.CompactResponse,
		"stat":    stat.CompactResponse,
		"git":     git.CompactResponse,
		"test":    test.CompactResponse,
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
func Runners(led *ledger.Ledger, cfg *config.Config, daemonStart time.Time, projectRoot string) map[string]Runner {
	if cfg == nil {
		cfg = config.Defaults()
	}
	_ = cfg // future-proof: ASH-35 git backend selection will read cfg.Git.Backend here.

	pretty := PrettyHandlers()
	runners := map[string]Runner{
		"read":    wrap(read.ParseArgs, read.Run, func(r *read.Result) bool { return r.Truncated }),
		"find":    wrap(find.ParseArgs, find.Run, func(r *find.Result) bool { return r.Truncated }),
		"grep":    wrap(grep.ParseArgs, grep.Run, func(r *grep.Result) bool { return r.Truncated }),
		"git":     wrap(git.ParseArgs, git.Run, nil),
		"metrics": wrapLedger(led, metrics.ParseArgs, metrics.RunWithLedger),
		"report":  wrapLedger(led, report.ParseArgs, report.RunWithLedger),
		"help":    wrap(help.ParseArgs, help.Run, nil),
		"hook":    wrap(hook.ParseArgs, hook.Run, nil),
		"stat":    wrap(stat.ParseArgs, stat.Run, nil),
		"write":   wrap(write.ParseArgs, write.Run, nil),
		"edit":    wrap(edit.ParseArgs, edit.Run, nil),
		"diff":    wrap(diff.ParseArgs, diff.Run, nil),
		"test":    wrap(test.ParseArgs, test.Run, func(r *test.Result) bool { return r.Truncated }),
		"init":    wrap(initverb.ParseArgs, initverb.Run, nil),
		"uninit":  wrap(uninit.ParseArgs, uninit.Run, nil),
		"stop":    wrap(stop.ParseArgs, stop.Run, nil),
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
				Ledger:      led,
				DaemonStart: daemonStart,
				ProjectRoot: projectRoot,
			}
			return bench.RunWithDeps(deps, a)
		},
	}
	return runners
}

// wrap builds a Runner from a typed parse+run pair. truncated is optional;
// pass nil for verbs that have no truncation concept.
func wrap[A any, R any](
	parse func(map[string]any) (*A, *proto.Error),
	run func(*A, *proto.Tracer) (R, *proto.Error),
	truncated func(R) bool,
) Runner {
	r := Runner{
		Run: func(args map[string]any, tr *proto.Tracer) (any, *proto.Error) {
			a, perr := parse(args)
			if perr != nil {
				return nil, perr
			}
			return run(a, tr)
		},
	}
	if truncated != nil {
		r.Truncated = func(d any) bool {
			if v, ok := d.(R); ok {
				return truncated(v)
			}
			return false
		}
	}
	return r
}

// wrapLedger builds a Runner for verbs whose Run takes a *ledger.Ledger
// instead of a *proto.Tracer (metrics, report).
func wrapLedger[A any, R any](
	led *ledger.Ledger,
	parse func(map[string]any) (*A, *proto.Error),
	run func(*ledger.Ledger, *A) (R, *proto.Error),
) Runner {
	return Runner{
		Run: func(args map[string]any, _ *proto.Tracer) (any, *proto.Error) {
			a, perr := parse(args)
			if perr != nil {
				return nil, perr
			}
			return run(led, a)
		},
	}
}
