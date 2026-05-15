package ledger

import (
	"testing"
	"time"

	"github.com/vmihailenco/msgpack/v5"
)

// recordWithArgs is the test analogue of the daemon's Record+Link path:
// inserts a call row, then runs Link on the resulting RowID. Returns the
// new row's id.
func recordWithArgs(t *testing.T, l *Ledger, verb string, args map[string]any) int64 {
	t.Helper()
	blob, err := msgpack.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	id, err := l.Record(&Call{
		Timestamp:   time.Now(),
		Verb:        verb,
		ArgsMsgpack: blob,
		OK:          true,
	})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if err := l.Link(id, args); err != nil {
		t.Fatalf("Link: %v", err)
	}
	return id
}

// ASH-110: a child call sharing a --path with a recent parent must
// produce one session_links row with kind=path_share and reason=path.
func TestLink_PathSharePopulatesSessionLinks(t *testing.T) {
	l := openTestLedger(t)
	parent := recordWithArgs(t, l, "read", map[string]any{"path": "internal/foo.go"})
	child := recordWithArgs(t, l, "edit", map[string]any{"path": "internal/foo.go", "old": "x", "new": "y"})

	links, err := l.QuerySessionLinks("current", time.Time{}, 0)
	if err != nil {
		t.Fatalf("QuerySessionLinks: %v", err)
	}
	if len(links) != 1 {
		t.Fatalf("links: want 1, got %d", len(links))
	}
	got := links[0]
	if got.ParentID != parent || got.ChildID != child {
		t.Errorf("link edge: want parent=%d child=%d, got parent=%d child=%d",
			parent, child, got.ParentID, got.ChildID)
	}
	if got.Kind != "path_share" {
		t.Errorf("kind: want path_share, got %q", got.Kind)
	}
	if got.Reason != "internal/foo.go" {
		t.Errorf("reason: want internal/foo.go, got %q", got.Reason)
	}
}

// ASH-110: calls with disjoint paths must not link.
func TestLink_DisjointPathsNoLink(t *testing.T) {
	l := openTestLedger(t)
	recordWithArgs(t, l, "read", map[string]any{"path": "a.go"})
	recordWithArgs(t, l, "read", map[string]any{"path": "b.go"})

	links, err := l.QuerySessionLinks("current", time.Time{}, 0)
	if err != nil {
		t.Fatalf("QuerySessionLinks: %v", err)
	}
	if len(links) != 0 {
		t.Errorf("links: want 0, got %d", len(links))
	}
}

// ASH-110: the "." path is the project root and must not produce a
// pseudo-link to every other call (that would defeat the heuristic).
func TestLink_DotPathSentinelIgnored(t *testing.T) {
	l := openTestLedger(t)
	recordWithArgs(t, l, "find", map[string]any{"path": "."})
	recordWithArgs(t, l, "grep", map[string]any{"path": ".", "pattern": "TODO"})

	links, err := l.QuerySessionLinks("current", time.Time{}, 0)
	if err != nil {
		t.Fatalf("QuerySessionLinks: %v", err)
	}
	if len(links) != 0 {
		t.Errorf("links: want 0 (. should be ignored), got %d", len(links))
	}
}

// ASH-110: RowID must round-trip through QueryWindow so callers can
// join with session_links. Regression test for the ledger schema/INSERT/
// SELECT triple staying in sync after the id-column SELECT addition.
func TestQueryWindow_PopulatesRowID(t *testing.T) {
	l := openTestLedger(t)
	id, err := l.Record(&Call{
		Timestamp: time.Now(),
		Verb:      "read",
		OK:        true,
	})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	calls, err := l.QueryWindow(QueryOpts{})
	if err != nil {
		t.Fatalf("QueryWindow: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("calls: want 1, got %d", len(calls))
	}
	if calls[0].RowID != id {
		t.Errorf("RowID: want %d, got %d", id, calls[0].RowID)
	}
}

// ASH-110: the linker must skip parents older than LinkWindow even if
// they share a path with the child — the agent's attention has moved on.
func TestLink_OutsideWindowNoLink(t *testing.T) {
	l := openTestLedger(t)
	// Insert an old parent directly via Record with a backdated
	// timestamp; Link is then called on a fresh child with the same
	// path. The stale parent must not produce an edge.
	old := time.Now().Add(-LinkWindow - time.Minute)
	blob, _ := msgpack.Marshal(map[string]any{"path": "stale.go"})
	if _, err := l.Record(&Call{
		Timestamp:   old,
		Verb:        "read",
		ArgsMsgpack: blob,
		OK:          true,
	}); err != nil {
		t.Fatalf("Record old: %v", err)
	}
	recordWithArgs(t, l, "edit", map[string]any{"path": "stale.go", "old": "x", "new": "y"})

	links, err := l.QuerySessionLinks("current", time.Time{}, 0)
	if err != nil {
		t.Fatalf("QuerySessionLinks: %v", err)
	}
	if len(links) != 0 {
		t.Errorf("links: want 0 (parent outside window), got %d", len(links))
	}
}
