package grep

import (
	"context"
	"sync"
	"testing"

	"github.com/stazelabs/ash/internal/proto"
)

// streamCapture is a test double for proto.Emitter. It records every
// chunk grep yields so tests can compare the streamed sequence against
// the cumulative Result.Matches and prove they agree.
type streamCapture struct {
	mu     sync.Mutex
	items  []Match
	emits  int
	flush  int
}

func (s *streamCapture) Emit(c any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m, ok := c.(Match); ok {
		s.items = append(s.items, m)
	}
	s.emits++
	return nil
}

func (s *streamCapture) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flush++
	return nil
}

func TestRun_StreamingEmitsEveryAppendedMatch(t *testing.T) {
	// With a tracer that has an emitter attached, every match grep
	// records in Result.Matches is also pushed to the emitter in the
	// same order. This is the contract ashmcp relies on: chunks +
	// cumulative final agree.
	root := makeTree(t)
	cap := &streamCapture{}
	tr := &proto.Tracer{}
	tr.SetEmitter(cap)

	res, perr := Run(&Args{
		Pattern:          "Foo",
		Path:             root,
		Glob:             DefaultGlob,
		Case:             "sensitive",
		MaxMatches:       DefaultMaxMatches,
		RespectGitignore: true,
	}, tr)
	if perr != nil {
		t.Fatalf("Run: %+v", perr)
	}
	if len(cap.items) != len(res.Matches) {
		t.Fatalf("emit count %d != Result.Matches %d", len(cap.items), len(res.Matches))
	}
	for i, want := range res.Matches {
		got := cap.items[i]
		// Compare in original (pre-relativization) form: path strings on
		// the stream side are still absolute because Emit happens before
		// the post-walk relativize pass. We only assert on Line and Text
		// + Kind to verify ordering.
		if got.Line != want.Line || got.Kind != want.Kind {
			t.Errorf("emit[%d] = {line=%d kind=%q} mismatched Result[%d] = {line=%d kind=%q}",
				i, got.Line, got.Kind, i, want.Line, want.Kind)
		}
	}
}

func TestRun_NoEmitterIsNoop(t *testing.T) {
	// A tracer with no emitter (the non-streaming default) must not
	// fail or block. This is the path every non-streaming caller takes
	// today and exercises the nil-emitter guard in Tracer.Emit.
	root := makeTree(t)
	tr := &proto.Tracer{}
	if _, perr := Run(&Args{
		Pattern:          "Foo",
		Path:             root,
		Glob:             DefaultGlob,
		Case:             "sensitive",
		MaxMatches:       DefaultMaxMatches,
		RespectGitignore: true,
	}, tr); perr != nil {
		t.Fatalf("Run: %+v", perr)
	}
}

func TestRun_StreamingHonorsContextCancellation(t *testing.T) {
	// When the tracer's context is cancelled before Run starts, grep
	// must bail at the walker's first checkpoint and return a (possibly
	// empty) result without erroring. This is the wire-level cancel
	// path: ashmcp closes the conn → daemon watcher cancels → walker
	// sees ctx.Err() and returns Stop.
	root := makeTree(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before Run

	tr := &proto.Tracer{}
	tr.SetContext(ctx)

	res, perr := Run(&Args{
		Pattern:          "Foo",
		Path:             root,
		Glob:             DefaultGlob,
		Case:             "sensitive",
		MaxMatches:       DefaultMaxMatches,
		RespectGitignore: true,
	}, tr)
	if perr != nil {
		t.Fatalf("Run with cancelled ctx returned proto error: %+v", perr)
	}
	// We don't assert match count here — the walker may have already
	// scanned the single-file fast path (which doesn't check ctx). What
	// we DO assert: Run returned cleanly without surfacing a walk error.
	_ = res
}
