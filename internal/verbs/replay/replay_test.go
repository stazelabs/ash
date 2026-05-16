package replay

import (
	"testing"

	"github.com/stazelabs/ash/internal/ledger"
	"github.com/stazelabs/ash/internal/proto"
	"github.com/vmihailenco/msgpack/v5"
)

func TestParseArgs_WireShape(t *testing.T) {
	a, perr := ParseArgs(map[string]any{
		"session":        "current",
		"since":          "1h",
		"verb":           "grep",
		"limit":          "25",
		"regress_tokens": "15",
		"top":            "5",
	})
	if perr != nil {
		t.Fatalf("parse: %v", perr)
	}
	if a.Session != "current" {
		t.Errorf("Session: got %q, want current", a.Session)
	}
	if a.Since.String() != "1h0m0s" {
		t.Errorf("Since: got %s, want 1h0m0s", a.Since)
	}
	if a.Verb != "grep" {
		t.Errorf("Verb: got %q", a.Verb)
	}
	if a.Limit != 25 {
		t.Errorf("Limit: got %d, want 25", a.Limit)
	}
	if a.RegressTokPct != 15 {
		t.Errorf("RegressTokPct: got %d, want 15", a.RegressTokPct)
	}
	if a.Top != 5 {
		t.Errorf("Top: got %d, want 5", a.Top)
	}
}

func TestParseArgs_Defaults(t *testing.T) {
	a, perr := ParseArgs(map[string]any{})
	if perr != nil {
		t.Fatalf("parse: %v", perr)
	}
	if a.Session != "current" {
		t.Errorf("default Session: got %q, want current", a.Session)
	}
	if a.RegressTokPct != DefaultRegressTokPct {
		t.Errorf("default RegressTokPct: got %d, want %d", a.RegressTokPct, DefaultRegressTokPct)
	}
	if a.Top != DefaultTop {
		t.Errorf("default Top: got %d, want %d", a.Top, DefaultTop)
	}
}

func TestParseArgs_BadSince(t *testing.T) {
	_, perr := ParseArgs(map[string]any{"since": "garbage"})
	if perr == nil {
		t.Fatal("expected error for since=garbage")
	}
}

func TestParseArgs_DayDuration(t *testing.T) {
	a, perr := ParseArgs(map[string]any{"since": "7d"})
	if perr != nil {
		t.Fatalf("parse: %v", perr)
	}
	want := 7 * 24 * 60 * 60
	if int(a.Since.Seconds()) != want {
		t.Errorf("7d: got %vs, want %ds", a.Since.Seconds(), want)
	}
}

func TestPreDispatchSkip(t *testing.T) {
	cases := []struct {
		verb string
		want string
	}{
		{"write", "mutating"},
		{"edit", "mutating"},
		{"init", "mutating"},
		{"uninit", "mutating"},
		{"stop", "mutating"},
		{"test", "heavy"},
		{"bench", "heavy"},
		{"replay", "recursive"},
		{"read", ""},
		{"grep", ""},
		{"find", ""},
		{"git", ""},
	}
	for _, c := range cases {
		got := preDispatchSkip(ledger.Call{Verb: c.verb})
		if got != c.want {
			t.Errorf("verb=%s: got %q, want %q", c.verb, got, c.want)
		}
	}
}

func TestHasTruncatedArg(t *testing.T) {
	if !hasTruncatedArg(map[string]any{"content": "<truncated:4096>"}) {
		t.Error("expected truncated sentinel to be detected")
	}
	if hasTruncatedArg(map[string]any{"path": "internal/verbs"}) {
		t.Error("plain path should not match truncated sentinel")
	}
	if hasTruncatedArg(map[string]any{"content": "<truncated:4096"}) {
		t.Error("partial sentinel without close bracket should not match")
	}
	if hasTruncatedArg(map[string]any{}) {
		t.Error("empty args should not match")
	}
}

func TestNeedsArgs(t *testing.T) {
	if needsArgs("help") {
		t.Error("help should not require args")
	}
	if needsArgs("stop") {
		t.Error("stop should not require args")
	}
	if needsArgs("report") {
		t.Error("report should not require args")
	}
	if !needsArgs("read") {
		t.Error("read should require args")
	}
	if !needsArgs("grep") {
		t.Error("grep should require args")
	}
}

func TestReplayOne_DeltaMath(t *testing.T) {
	// Counter is set up so any pretty body of length N returns ~N/4
	// tokens (cl100k_base ish). We construct a fake Deps where the
	// replayed run returns a known pretty body and check the math.
	counter, err := ledger.NewCounter()
	if err != nil {
		t.Fatalf("ledger.NewCounter: %v", err)
	}
	calledVerb := ""
	deps := Deps{
		Counter: counter,
		Run: func(verb string, args map[string]any) (any, *proto.Error) {
			calledVerb = verb
			return map[string]any{"ok": true}, nil
		},
		Pretty: func(verb string, req *proto.Request, rsp *proto.Response) string {
			return "ok\nfresh body that replaces the original"
		},
	}
	c := ledger.Call{
		Verb:       "grep",
		OK:         true,
		TokensOut:  10,
	}
	row, _, skip := replayOne(deps, c, map[string]any{"pattern": "x", "path": "."}, 10)
	if skip != "" {
		t.Fatalf("unexpected skip: %q", skip)
	}
	if calledVerb != "grep" {
		t.Errorf("dispatch verb: got %q, want grep", calledVerb)
	}
	if row.OriginalTokens != 10 {
		t.Errorf("OriginalTokens: got %d, want 10", row.OriginalTokens)
	}
	if row.ReplayTokens <= 0 {
		t.Errorf("ReplayTokens should be > 0, got %d", row.ReplayTokens)
	}
	expectedDelta := row.ReplayTokens - row.OriginalTokens
	if row.DeltaTokens != expectedDelta {
		t.Errorf("DeltaTokens: got %d, want %d", row.DeltaTokens, expectedDelta)
	}
	if !row.OriginalOK || !row.ReplayOK {
		t.Errorf("ok flags: orig=%v new=%v", row.OriginalOK, row.ReplayOK)
	}
}

func TestReplayOne_UnknownVerbSkips(t *testing.T) {
	counter, err := ledger.NewCounter()
	if err != nil {
		t.Fatalf("ledger.NewCounter: %v", err)
	}
	deps := Deps{
		Counter: counter,
		Run: func(verb string, args map[string]any) (any, *proto.Error) {
			return nil, &proto.Error{Code: "unknown_verb", Msg: "removed"}
		},
		Pretty: func(verb string, req *proto.Request, rsp *proto.Response) string {
			return ""
		},
	}
	_, _, skip := replayOne(deps, ledger.Call{Verb: "obsolete"}, map[string]any{}, 10)
	if skip != "unknown_verb" {
		t.Errorf("skip reason: got %q, want unknown_verb", skip)
	}
}

func TestReplayOne_OKMismatch(t *testing.T) {
	counter, err := ledger.NewCounter()
	if err != nil {
		t.Fatalf("ledger.NewCounter: %v", err)
	}
	deps := Deps{
		Counter: counter,
		Run: func(verb string, args map[string]any) (any, *proto.Error) {
			return nil, &proto.Error{Code: "path_denied", Msg: "outside jail"}
		},
		Pretty: func(verb string, req *proto.Request, rsp *proto.Response) string {
			return "err path_denied"
		},
	}
	c := ledger.Call{Verb: "read", OK: true, TokensOut: 100}
	row, _, skip := replayOne(deps, c, map[string]any{"path": "/etc/passwd"}, 10)
	if skip != "" {
		t.Fatalf("unexpected skip: %q", skip)
	}
	if row.OriginalOK == row.ReplayOK {
		t.Error("OK should mismatch: orig=true new=false")
	}
	if row.ReplayErr != "path_denied" {
		t.Errorf("ReplayErr: got %q, want path_denied", row.ReplayErr)
	}
}

func TestRegressDetection(t *testing.T) {
	counter, err := ledger.NewCounter()
	if err != nil {
		t.Fatalf("ledger.NewCounter: %v", err)
	}
	// Build a pretty response that tokenizes to substantially more
	// than the original — should trip the regression flag at default
	// regress=10%.
	largeBody := "ok\n"
	for i := 0; i < 200; i++ {
		largeBody += "match line " + string(rune('a'+i%26)) + "\n"
	}
	deps := Deps{
		Counter: counter,
		Run: func(verb string, args map[string]any) (any, *proto.Error) {
			return map[string]any{}, nil
		},
		Pretty: func(verb string, req *proto.Request, rsp *proto.Response) string {
			return largeBody
		},
	}
	c := ledger.Call{Verb: "grep", OK: true, TokensOut: 5}
	row, _, _ := replayOne(deps, c, map[string]any{"pattern": "x", "path": "."}, 10)
	if !row.Regress {
		t.Errorf("expected regression flag: orig=%d new=%d delta=%d (%.1f%%)",
			row.OriginalTokens, row.ReplayTokens, row.DeltaTokens, row.DeltaPct)
	}
}

func TestArgsSummary_OrderAndTrunc(t *testing.T) {
	got := argsSummary(map[string]any{
		"path":    "internal/verbs/grep",
		"pattern": "TODO",
		"long":    "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	})
	// Keys sorted alphabetically: long, path, pattern
	wantPrefix := "long=aaaaaaaaaaaaaaaaaaaaaaaaaaaaa..."
	if got[:len(wantPrefix)] != wantPrefix {
		t.Errorf("expected truncated long= first; got %q", got)
	}
}

func TestDecodeArgsMap_Roundtrip(t *testing.T) {
	in := map[string]any{"path": ".", "limit": int64(10)}
	blob, err := msgpack.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out := decodeArgsMap(blob)
	if out["path"] != "." {
		t.Errorf("path: got %v", out["path"])
	}
	if v, _ := out["limit"].(int64); v != 10 {
		t.Errorf("limit: got %v", out["limit"])
	}
}

func TestDecodeArgsMap_EmptyAndGarbage(t *testing.T) {
	if decodeArgsMap(nil) != nil {
		t.Error("nil blob should decode to nil")
	}
	if decodeArgsMap([]byte{}) != nil {
		t.Error("empty blob should decode to nil")
	}
	if decodeArgsMap([]byte{0xff, 0xff, 0xff}) != nil {
		t.Error("garbage blob should decode to nil (not panic)")
	}
}

func TestRunWithDeps_NoLedgerCallsIsEmpty(t *testing.T) {
	// Open an in-memory ledger so QueryWindow returns no calls.
	led, err := ledger.Open(t.TempDir()+"/ledger.db", t.TempDir(), "test")
	if err != nil {
		t.Fatalf("open ledger: %v", err)
	}
	defer led.Close()
	counter, err := ledger.NewCounter()
	if err != nil {
		t.Fatalf("counter: %v", err)
	}
	deps := Deps{
		Counter: counter,
		Ledger:  led,
		Run: func(verb string, args map[string]any) (any, *proto.Error) {
			t.Fatalf("Run should not be called with empty ledger")
			return nil, nil
		},
		Pretty: func(verb string, req *proto.Request, rsp *proto.Response) string {
			return ""
		},
	}
	a, _ := ParseArgs(map[string]any{"session": "all"})
	r, perr := RunWithDeps(deps, a)
	if perr != nil {
		t.Fatalf("RunWithDeps: %v", perr)
	}
	if r.Replayed != 0 || r.Skipped != 0 {
		t.Errorf("replayed=%d skipped=%d; want both zero", r.Replayed, r.Skipped)
	}
}

func TestRunWithDeps_MissingDeps(t *testing.T) {
	a, _ := ParseArgs(map[string]any{})
	_, perr := RunWithDeps(Deps{}, a)
	if perr == nil || perr.Code != "config" {
		t.Fatalf("expected config error, got %v", perr)
	}
}
