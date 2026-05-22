package workspace

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
	if a.Since != 30*time.Minute {
		t.Errorf("since: got %v, want 30m", a.Since)
	}
	if a.Recent != DefaultRecent {
		t.Errorf("recent: got %d, want %d", a.Recent, DefaultRecent)
	}
}

// ASH-110: relevantFiles returns files in reverse-chronological order
// (most recent first), each appearing once even if touched multiple
// times in the window.
func TestRelevantFiles_DedupsAndOrders(t *testing.T) {
	now := time.Now()
	calls := []ledger.Call{
		{Verb: "read", ArgsMsgpack: mkArgs(t, map[string]any{"path": "newest.go"}), Timestamp: now},
		{Verb: "edit", ArgsMsgpack: mkArgs(t, map[string]any{"path": "middle.go"}), Timestamp: now.Add(-1 * time.Minute)},
		{Verb: "read", ArgsMsgpack: mkArgs(t, map[string]any{"path": "newest.go"}), Timestamp: now.Add(-2 * time.Minute)},
		{Verb: "read", ArgsMsgpack: mkArgs(t, map[string]any{"path": "oldest.go"}), Timestamp: now.Add(-5 * time.Minute)},
	}
	files := relevantFiles(calls, 10)
	if len(files) != 3 {
		t.Fatalf("want 3 unique files, got %d", len(files))
	}
	if files[0].Path != "newest.go" {
		t.Errorf("first: want newest.go, got %s", files[0].Path)
	}
	if files[1].Path != "middle.go" {
		t.Errorf("second: want middle.go, got %s", files[1].Path)
	}
	if files[2].Path != "oldest.go" {
		t.Errorf("third: want oldest.go, got %s", files[2].Path)
	}
	if files[0].LastVerb != "read" {
		t.Errorf("newest verb: want read, got %s", files[0].LastVerb)
	}
}

// ASH-110: recentSearches dedups grep calls by (pattern, path) and
// preserves recency order.
func TestRecentSearches_DedupsAndOrders(t *testing.T) {
	now := time.Now()
	calls := []ledger.Call{
		{Verb: "grep", ArgsMsgpack: mkArgs(t, map[string]any{"pattern": "TODO", "path": "internal/"}), Timestamp: now},
		{Verb: "grep", ArgsMsgpack: mkArgs(t, map[string]any{"pattern": "FIXME", "path": "internal/"}), Timestamp: now.Add(-1 * time.Minute)},
		{Verb: "grep", ArgsMsgpack: mkArgs(t, map[string]any{"pattern": "TODO", "path": "internal/"}), Timestamp: now.Add(-2 * time.Minute)},
		{Verb: "find", ArgsMsgpack: mkArgs(t, map[string]any{"glob": "**/*.go", "path": "."}), Timestamp: now.Add(-3 * time.Minute)},
	}
	out := recentSearches(calls, 10)
	if len(out) != 3 {
		t.Fatalf("want 3 unique searches, got %d", len(out))
	}
	if out[0].Pattern != "TODO" {
		t.Errorf("first: want TODO, got %q", out[0].Pattern)
	}
	if out[1].Pattern != "FIXME" {
		t.Errorf("second: want FIXME, got %q", out[1].Pattern)
	}
	if out[2].Pattern != "**/*.go" {
		t.Errorf("third: want **/*.go (find glob), got %q", out[2].Pattern)
	}
}

// ASH-110: mostRecentError returns the first failing call, or nil.
func TestMostRecentError(t *testing.T) {
	now := time.Now()
	calls := []ledger.Call{
		{Verb: "read", OK: true, Timestamp: now},
		{Verb: "edit", OK: false, ErrCode: "not_found", ErrMsg: "no such file", Timestamp: now.Add(-1 * time.Minute)},
		{Verb: "read", OK: true, Timestamp: now.Add(-2 * time.Minute)},
	}
	e := mostRecentError(calls)
	if e == nil {
		t.Fatal("want error, got nil")
	}
	if e.Verb != "edit" || e.Code != "not_found" {
		t.Errorf("got %+v, want verb=edit code=not_found", e)
	}

	if e := mostRecentError(nil); e != nil {
		t.Errorf("empty calls: want nil, got %+v", e)
	}
	if e := mostRecentError([]ledger.Call{{OK: true}}); e != nil {
		t.Errorf("all ok: want nil, got %+v", e)
	}
}

// ASH-110: recent cap clips both files and searches.
func TestRecent_Caps(t *testing.T) {
	now := time.Now()
	var calls []ledger.Call
	for i := 0; i < 10; i++ {
		calls = append(calls, ledger.Call{
			Verb:        "read",
			ArgsMsgpack: mkArgs(t, map[string]any{"path": "f" + string(rune('a'+i)) + ".go"}),
			Timestamp:   now.Add(time.Duration(-i) * time.Second),
		})
	}
	files := relevantFiles(calls, 3)
	if len(files) != 3 {
		t.Errorf("files: want 3, got %d", len(files))
	}
}
