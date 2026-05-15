package proto

import (
	"context"
	"errors"
	"testing"
)

func TestTracer_NilEmitterIsNoop(t *testing.T) {
	// A bare *Tracer (no emitter attached) must accept Emit without
	// panicking or escalating. This is the path every non-streaming
	// verb takes today.
	var tr *Tracer = &Tracer{}
	tr.Emit(struct{ X int }{1}) // must not panic
	var nilTr *Tracer
	nilTr.Emit("anything") // nil receiver also tolerated
}

func TestTracer_EmitForwardsToAttachedEmitter(t *testing.T) {
	rec := &recordingEmitter{}
	tr := &Tracer{}
	tr.SetEmitter(rec)
	tr.Emit("first")
	tr.Emit("second")
	if got, want := len(rec.items), 2; got != want {
		t.Fatalf("emit count: got %d, want %d", got, want)
	}
	if rec.items[0].(string) != "first" || rec.items[1].(string) != "second" {
		t.Errorf("items wrong: %+v", rec.items)
	}
}

func TestTracer_ContextDefaultsToBackground(t *testing.T) {
	// A bare *Tracer returns context.Background() so streaming verbs
	// can call tr.Context().Err() unconditionally. A nil *Tracer is
	// treated the same.
	var tr *Tracer
	if tr.Context().Err() != nil {
		t.Errorf("nil Tracer Context should be Background, not cancelled")
	}
	tr = &Tracer{}
	if tr.Context().Err() != nil {
		t.Errorf("zero Tracer Context should be Background, not cancelled")
	}
}

func TestTracer_ContextHonorsCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	tr := &Tracer{}
	tr.SetContext(ctx)
	if tr.Context().Err() != nil {
		t.Fatal("context should be live before cancel")
	}
	cancel()
	if !errors.Is(tr.Context().Err(), context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", tr.Context().Err())
	}
}

// recordingEmitter is a test double that captures every Emit call. Used
// across proto tests and (in commit 2) verb-level streaming tests.
type recordingEmitter struct {
	items   []any
	flushes int
}

func (r *recordingEmitter) Emit(chunk any) error {
	r.items = append(r.items, chunk)
	return nil
}

func (r *recordingEmitter) Flush() error {
	r.flushes++
	return nil
}
