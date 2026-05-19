package turn

import (
	"path/filepath"
	"testing"

	"github.com/stazelabs/ash/internal/ledger"
)

func openTestLedger(t *testing.T) *ledger.Ledger {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ledger.db")
	l, err := ledger.Open(path, "/test/root", "test")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { l.Close() })
	return l
}

func TestParseArgs_RequiresTurnID(t *testing.T) {
	if _, perr := ParseArgs(map[string]any{}); perr == nil {
		t.Fatal("missing turn_id: want error")
	} else if perr.Code != "args" {
		t.Errorf("wrong error code: got %q, want args", perr.Code)
	}
}

func TestParseArgs_AllFields(t *testing.T) {
	a, perr := ParseArgs(map[string]any{
		"turn_id":               "msg_01",
		"harness_session_id":    "sess_abc",
		"model":                 "claude-opus-4-7",
		"input_tokens":          int64(10),
		"output_tokens":         int64(200),
		"cache_read_tokens":     int64(50_000),
		"cache_creation_tokens": int64(1_500),
		"timestamp_nanos":       int64(1_700_000_000_000_000_000),
	})
	if perr != nil {
		t.Fatalf("ParseArgs: %+v", perr)
	}
	want := Args{
		TurnID: "msg_01", HarnessSessionID: "sess_abc", Model: "claude-opus-4-7",
		InputTokens: 10, OutputTokens: 200,
		CacheReadTokens: 50_000, CacheCreationTokens: 1_500,
		TimestampNanos: 1_700_000_000_000_000_000,
	}
	if *a != want {
		t.Errorf("ParseArgs:\nwant %+v\ngot  %+v", want, *a)
	}
}

func TestParseArgs_RejectsBadTimestamp(t *testing.T) {
	if _, perr := ParseArgs(map[string]any{
		"turn_id":         "msg_01",
		"timestamp_nanos": "not-a-number",
	}); perr == nil {
		t.Fatal("bad timestamp: want error")
	}
}

// TestRunWithLedger_InsertAndIdempotent covers the verb's contract: a
// fresh turn_id inserts (Result.Inserted=true); a duplicate is dropped
// (Result.Inserted=false) without overwriting the prior row.
func TestRunWithLedger_InsertAndIdempotent(t *testing.T) {
	l := openTestLedger(t)
	a := &Args{TurnID: "msg_x", CacheReadTokens: 42, TimestampNanos: 1_700_000_000_000_000_000}
	r, perr := RunWithLedger(l, a)
	if perr != nil {
		t.Fatalf("RunWithLedger first: %+v", perr)
	}
	if !r.Inserted {
		t.Fatal("first call: want Inserted=true")
	}

	a.CacheReadTokens = 999
	r2, perr := RunWithLedger(l, a)
	if perr != nil {
		t.Fatalf("RunWithLedger second: %+v", perr)
	}
	if r2.Inserted {
		t.Fatal("duplicate turn_id: want Inserted=false")
	}

	got, err := l.QueryTurns(ledger.QueryOpts{SessionID: "current"})
	if err != nil {
		t.Fatalf("QueryTurns: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d rows after duplicate insert, want 1", len(got))
	}
	if got[0].CacheReadTokens != 42 {
		t.Errorf("idempotency: cache_read_tokens=%d, want 42", got[0].CacheReadTokens)
	}
}
