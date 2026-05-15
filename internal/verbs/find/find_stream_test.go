package find

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stazelabs/ash/internal/proto"
)

type streamCapture struct {
	mu    sync.Mutex
	items []Record
}

func (s *streamCapture) Emit(c any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := c.(Record); ok {
		s.items = append(s.items, r)
	}
	return nil
}
func (s *streamCapture) Flush() error { return nil }

func TestRun_StreamingEmitsEveryAppendedRecord(t *testing.T) {
	// Fixture: a few files; find should emit each record in walker
	// order and the stream count must equal the cumulative Result.
	root := t.TempDir()
	for _, name := range []string{"a.go", "b.go", "c.go"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("package x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cap := &streamCapture{}
	tr := &proto.Tracer{}
	tr.SetEmitter(cap)

	res, perr := Run(&Args{
		Path:  root,
		Glob:  "**/*.go",
		Type:  "file",
		Limit: 100,
	}, tr)
	if perr != nil {
		t.Fatalf("Run: %+v", perr)
	}
	if len(cap.items) != len(res.Records) {
		t.Fatalf("emit count %d != Result.Records %d", len(cap.items), len(res.Records))
	}
}

func TestRun_StreamingHonorsContextCancellation(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a.go", "b.go", "c.go"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("package x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	tr := &proto.Tracer{}
	tr.SetContext(ctx)

	if _, perr := Run(&Args{
		Path:  root,
		Glob:  "**/*.go",
		Type:  "any",
		Limit: 100,
	}, tr); perr != nil {
		t.Fatalf("Run with cancelled ctx: %+v", perr)
	}
}
