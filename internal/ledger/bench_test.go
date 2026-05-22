package ledger

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/vmihailenco/msgpack/v5"
)

// BenchmarkRecordCluster measures the ledger write cluster — Record (one
// INSERT) plus Link (one bounded SELECT over the last 16 calls, then up to
// 16 session_links INSERTs) — that ASH-214 moved off the synchronous
// response path. The call args share a --path so Link does its heaviest
// realistic work: every lookback candidate matches and yields a link row.
func BenchmarkRecordCluster(b *testing.B) {
	l := openBenchLedger(b)
	args := map[string]any{"path": "internal/walker/walker.go"}
	blob, err := msgpack.Marshal(args)
	if err != nil {
		b.Fatalf("marshal: %v", err)
	}
	call := &Call{Timestamp: time.Now(), Verb: "grep", ArgsMsgpack: blob, OK: true}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		id, err := l.Record(call)
		if err != nil {
			b.Fatalf("Record: %v", err)
		}
		if err := l.Link(id, args); err != nil {
			b.Fatalf("Link: %v", err)
		}
	}
}

// BenchmarkRecord isolates the single INSERT — the unavoidable per-call
// ledger write — without the session-graph Link step.
func BenchmarkRecord(b *testing.B) {
	l := openBenchLedger(b)
	call := &Call{Timestamp: time.Now(), Verb: "grep", OK: true}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := l.Record(call); err != nil {
			b.Fatalf("Record: %v", err)
		}
	}
}

func openBenchLedger(b *testing.B) *Ledger {
	b.Helper()
	l, err := Open(filepath.Join(b.TempDir(), "ledger.db"), "/test/root", "bench")
	if err != nil {
		b.Fatalf("Open: %v", err)
	}
	b.Cleanup(func() { l.Close() })
	return l
}
