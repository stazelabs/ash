package recap

import (
	"testing"
	"time"

	"github.com/stazelabs/ash/internal/ledger"
	"github.com/vmihailenco/msgpack/v5"
)

func mkArgs(t *testing.T, m map[string]any) []byte {
	t.Helper()
	b, err := msgpack.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func TestParseArgs_Defaults(t *testing.T) {
	a, perr := ParseArgs(map[string]any{})
	if perr != nil {
		t.Fatalf("unexpected: %+v", perr)
	}
	if a.Since != time.Hour {
		t.Errorf("since default: got %v, want 1h", a.Since)
	}
	if a.Top != DefaultTop {
		t.Errorf("top default: got %d, want %d", a.Top, DefaultTop)
	}
}

func TestParseArgs_Since(t *testing.T) {
	a, perr := ParseArgs(map[string]any{"since": "2h"})
	if perr != nil {
		t.Fatalf("unexpected: %+v", perr)
	}
	if a.Since != 2*time.Hour {
		t.Errorf("since: got %v, want 2h", a.Since)
	}
	a, perr = ParseArgs(map[string]any{"since": "3d"})
	if perr != nil {
		t.Fatalf("3d: %+v", perr)
	}
	if a.Since != 72*time.Hour {
		t.Errorf("3d: got %v, want 72h", a.Since)
	}
	if _, perr := ParseArgs(map[string]any{"since": "garbage"}); perr == nil {
		t.Error("garbage since should error")
	}
}

// ASH-110: aggregate must split read/write/grep into the right buckets
// per file and surface the most-active file first.
func TestAggregate_FileBuckets(t *testing.T) {
	calls := []ledger.Call{
		{Verb: "read", OK: true, ArgsMsgpack: mkArgs(t, map[string]any{"path": "a.go"}), Timestamp: time.Now()},
		{Verb: "read", OK: true, ArgsMsgpack: mkArgs(t, map[string]any{"path": "a.go"}), Timestamp: time.Now()},
		{Verb: "edit", OK: true, ArgsMsgpack: mkArgs(t, map[string]any{"path": "a.go"}), Timestamp: time.Now()},
		{Verb: "grep", OK: true, ArgsMsgpack: mkArgs(t, map[string]any{"path": "internal/", "pattern": "TODO"}), Timestamp: time.Now()},
		{Verb: "grep", OK: true, ArgsMsgpack: mkArgs(t, map[string]any{"path": "internal/", "pattern": "TODO"}), Timestamp: time.Now()},
		{Verb: "grep", OK: true, ArgsMsgpack: mkArgs(t, map[string]any{"path": "internal/", "pattern": "FIXME"}), Timestamp: time.Now()},
	}
	r := aggregate(calls, 10)
	if r.Totals.Calls != 6 || r.Totals.OK != 6 {
		t.Errorf("totals: got calls=%d ok=%d, want 6/6", r.Totals.Calls, r.Totals.OK)
	}
	if len(r.Files) != 2 {
		t.Fatalf("files: want 2, got %d", len(r.Files))
	}
	if r.Files[0].Path != "a.go" {
		t.Errorf("first file: want a.go, got %s", r.Files[0].Path)
	}
	if r.Files[0].Reads != 2 || r.Files[0].Edits != 1 {
		t.Errorf("a.go bucket: reads=%d edits=%d, want 2/1", r.Files[0].Reads, r.Files[0].Edits)
	}
	if len(r.Patterns) != 2 {
		t.Fatalf("patterns: want 2, got %d", len(r.Patterns))
	}
	if r.Patterns[0].Pattern != "TODO" || r.Patterns[0].Calls != 2 {
		t.Errorf("top pattern: got %q×%d, want TODO×2", r.Patterns[0].Pattern, r.Patterns[0].Calls)
	}
	if len(r.Edits) != 1 || r.Edits[0].Path != "a.go" {
		t.Errorf("edits: want 1 a.go, got %+v", r.Edits)
	}
}

// ASH-110: top cap clips file/pattern/edit lists.
func TestAggregate_TopCap(t *testing.T) {
	var calls []ledger.Call
	for i, p := range []string{"a", "b", "c", "d", "e"} {
		calls = append(calls, ledger.Call{
			Verb: "read", OK: true,
			ArgsMsgpack: mkArgs(t, map[string]any{"path": p + ".go"}),
			Timestamp:   time.Now().Add(time.Duration(-i) * time.Second),
		})
	}
	r := aggregate(calls, 3)
	if len(r.Files) != 3 {
		t.Errorf("files: want 3 (top cap), got %d", len(r.Files))
	}
}

// ASH-110: an empty session aggregates to a "no activity" result.
func TestAggregate_Empty(t *testing.T) {
	r := aggregate(nil, 10)
	if r.Totals.Calls != 0 {
		t.Errorf("calls: want 0, got %d", r.Totals.Calls)
	}
	if len(r.Files) != 0 || len(r.Patterns) != 0 || len(r.Edits) != 0 {
		t.Errorf("empty: want all sections empty, got files=%d patterns=%d edits=%d",
			len(r.Files), len(r.Patterns), len(r.Edits))
	}
}
