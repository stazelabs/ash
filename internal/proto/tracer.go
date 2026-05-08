package proto

import (
	"sync/atomic"
	"time"
)

// Tracer accumulates sub-execution latency from a verb's Run. The daemon
// allocates one per request, passes it through the verb's Run signature,
// and snapshots the result into Metrics.Phases after the call returns.
//
// A nil *Tracer is valid and turns every Add* method into a no-op. This
// means tests can call verb.Run(args, nil) without setting up timing and
// non-instrumented verbs (help, metrics) cost nothing.
//
// Add* are atomic so a verb can fan out internally without losing counts;
// none of today's verbs do, but it makes the contract worry-free.
type Tracer struct {
	walkUs         atomic.Int64
	ioUs           atomic.Int64
	regexUs        atomic.Int64
	regexCompileUs atomic.Int64
}

func (t *Tracer) AddWalk(d time.Duration) {
	if t != nil {
		t.walkUs.Add(d.Microseconds())
	}
}

func (t *Tracer) AddIO(d time.Duration) {
	if t != nil {
		t.ioUs.Add(d.Microseconds())
	}
}

func (t *Tracer) AddRegex(d time.Duration) {
	if t != nil {
		t.regexUs.Add(d.Microseconds())
	}
}

func (t *Tracer) AddRegexCompile(d time.Duration) {
	if t != nil {
		t.regexCompileUs.Add(d.Microseconds())
	}
}

// Snapshot returns the accumulated phases. Safe on a nil receiver.
func (t *Tracer) Snapshot() Phases {
	if t == nil {
		return Phases{}
	}
	return Phases{
		WalkUs:         t.walkUs.Load(),
		IOUs:           t.ioUs.Load(),
		RegexUs:        t.regexUs.Load(),
		RegexCompileUs: t.regexCompileUs.Load(),
	}
}
