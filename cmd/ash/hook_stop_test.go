package main

import (
	"strings"
	"testing"
)

// TestFindLastAssistant_PicksLastAssistantSkippingOthers verifies the
// scanner walks back-to-front and ignores user/attachment entries to
// find the trailing assistant turn.
func TestFindLastAssistant_PicksLastAssistantSkippingOthers(t *testing.T) {
	lines := []string{
		`{"type":"user","message":{"role":"user","content":"hi"}}`,
		`{"type":"assistant","sessionId":"sess1","timestamp":"2026-05-18T04:05:00.000Z","message":{"id":"msg_first","model":"claude-opus-4-7","usage":{"input_tokens":1,"output_tokens":2,"cache_read_input_tokens":3,"cache_creation_input_tokens":4}}}`,
		`{"type":"user","message":{"role":"user","content":"more"}}`,
		`{"type":"attachment","attachment":{"type":"todo_reminder"}}`,
		`{"type":"assistant","sessionId":"sess1","timestamp":"2026-05-18T04:06:00.000Z","message":{"id":"msg_last","model":"claude-opus-4-7","usage":{"input_tokens":10,"output_tokens":200,"cache_read_input_tokens":50000,"cache_creation_input_tokens":1500}}}`,
	}
	data := []byte(strings.Join(lines, "\n") + "\n")
	entry, ok := findLastAssistant(data)
	if !ok {
		t.Fatal("findLastAssistant: want ok=true")
	}
	if entry.Message.ID != "msg_last" {
		t.Errorf("picked wrong entry: got message.id=%q, want msg_last", entry.Message.ID)
	}
	if entry.Message.Usage.CacheReadInputTokens != 50000 {
		t.Errorf("cache_read_input_tokens: got %d, want 50000", entry.Message.Usage.CacheReadInputTokens)
	}
	if entry.SessionID != "sess1" {
		t.Errorf("sessionId: got %q, want sess1", entry.SessionID)
	}
}

// TestFindLastAssistant_NoAssistant returns false when the tail
// contains only user/attachment entries.
func TestFindLastAssistant_NoAssistant(t *testing.T) {
	data := []byte("{\"type\":\"user\"}\n{\"type\":\"attachment\"}\n")
	if _, ok := findLastAssistant(data); ok {
		t.Fatal("want ok=false when no assistant entry in tail")
	}
}

// TestFindLastAssistant_SkipsAssistantWithoutMessageID guards against
// counting a stub entry without a populated message.id (could happen
// mid-stream or for an error response).
func TestFindLastAssistant_SkipsAssistantWithoutMessageID(t *testing.T) {
	data := []byte(`{"type":"assistant","message":{"id":""}}` + "\n")
	if _, ok := findLastAssistant(data); ok {
		t.Fatal("empty message.id: want ok=false")
	}
}

// TestParseTranscriptTimestamp_RFC3339 covers the happy path and the
// soft-fail behavior (returns 0 on parse failure so the verb falls back
// to time.Now()).
func TestParseTranscriptTimestamp_RFC3339(t *testing.T) {
	got := parseTranscriptTimestamp("2026-05-18T04:05:59.253Z")
	if got <= 0 {
		t.Fatalf("RFC3339 parse: got %d, want positive", got)
	}
	if n := parseTranscriptTimestamp(""); n != 0 {
		t.Errorf("empty input: got %d, want 0", n)
	}
	if n := parseTranscriptTimestamp("yesterday"); n != 0 {
		t.Errorf("garbage input: got %d, want 0", n)
	}
}

// TestStopArgvHasEvent covers both --event stop forms plus the
// negative case.
func TestStopArgvHasEvent(t *testing.T) {
	cases := []struct {
		argv []string
		want bool
	}{
		{[]string{"--event", "stop"}, true},
		{[]string{"--event=stop"}, true},
		{[]string{"--event", "other"}, false},
		{[]string{"--event=other"}, false},
		{[]string{}, false},
		{[]string{"--event"}, false}, // dangling flag, no value
	}
	for _, c := range cases {
		if got := stopArgvHasEvent(c.argv); got != c.want {
			t.Errorf("stopArgvHasEvent(%v): got %v, want %v", c.argv, got, c.want)
		}
	}
}
