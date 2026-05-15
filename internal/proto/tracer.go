package proto

import (
	"context"
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
//
// Emit (ASH-106) is the streaming hook: a verb that knows how to yield
// partial results calls Tracer.Emit(chunk) at each natural boundary
// (per-match for grep/find, per-package for test). When the daemon set an
// Emitter on the tracer (Request.Stream==true), the chunk lands on the
// wire as a Chunk frame; when the emitter is nil the call is a no-op, so
// non-streaming clients pay nothing.
type Tracer struct {
	walkUs         atomic.Int64
	ioUs           atomic.Int64
	regexUs        atomic.Int64
	regexCompileUs atomic.Int64
	emitter        Emitter
	ctx            context.Context
}

// Emitter is the daemon-side streaming sink. Implementations buffer
// chunks and flush as Chunk frames on the conn. Emit must be safe to
// call from the verb's goroutine; the daemon's frameEmitter serializes
// writes internally.
type Emitter interface {
	Emit(chunk any) error
	Flush() error
}

// SetEmitter attaches a streaming sink to the tracer. Called by the
// daemon's request handler when Request.Stream is true; non-streaming
// callers leave it nil and Emit is a no-op.
func (t *Tracer) SetEmitter(e Emitter) {
	if t != nil {
		t.emitter = e
	}
}

// SetContext attaches a request-scoped context to the tracer. The daemon
// sets this on every dispatched request so streaming verbs (grep, find,
// test) can poll t.Context().Err() at their walker / event-loop
// checkpoints and abort cleanly when the client cancels mid-stream
// (ASH-106). Non-streaming verbs leave it alone — Context() falls back to
// context.Background() when no ctx is attached.
func (t *Tracer) SetContext(ctx context.Context) {
	if t != nil {
		t.ctx = ctx
	}
}

// Context returns the request-scoped context, or context.Background() if
// none was attached. Safe on a nil receiver. Verbs that need to honor
// cancellation call t.Context().Err() periodically.
func (t *Tracer) Context() context.Context {
	if t == nil || t.ctx == nil {
		return context.Background()
	}
	return t.ctx
}

// Emit forwards chunk to the attached emitter, if any. Safe on a nil
// receiver and on a tracer with no emitter attached — both cases drop
// silently, which is exactly what a non-streaming verb wants.
func (t *Tracer) Emit(chunk any) {
	if t == nil || t.emitter == nil {
		return
	}
	_ = t.emitter.Emit(chunk)
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
