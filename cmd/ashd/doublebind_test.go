package main

// ASH-154 — daemon-side double-bind refusal. The client-side sweep in
// killStaleIfNeeded normally clears the socket before a new ashd starts,
// but if another ashd is still alive for the socket (a racing client, a
// missed orphan) we exit non-zero rather than racing it for the bind.
// checkNoOtherAshd is the predicate that runs before net.Listen; these
// tests inject a fake findAshdSocketPIDs so the predicate is exercised
// without spawning real ashd processes.

import (
	"strings"
	"testing"
)

// withFakeFindAshdSocketPIDs swaps findAshdSocketPIDs for the duration
// of a test and restores it on Cleanup. Mirrors the test seam pattern
// used in internal/verbs/stop for the process lister.
func withFakeFindAshdSocketPIDs(t *testing.T, fn func(string) []int) {
	t.Helper()
	prev := findAshdSocketPIDs
	findAshdSocketPIDs = fn
	t.Cleanup(func() { findAshdSocketPIDs = prev })
}

// TestCheckNoOtherAshd_EmptyPasses is the happy path: no other ashd is
// alive on the socket, so the bind may proceed.
func TestCheckNoOtherAshd_EmptyPasses(t *testing.T) {
	withFakeFindAshdSocketPIDs(t, func(string) []int { return nil })
	if err := checkNoOtherAshd("/tmp/whatever.sock"); err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

// TestCheckNoOtherAshd_OneAlivePIDRefuses covers the rebuild-race case:
// the previous daemon survived the client sweep (or no sweep ran), so
// the new daemon refuses to bind. The error must name the surviving
// PID so the operator can `ash stop` the right process.
func TestCheckNoOtherAshd_OneAlivePIDRefuses(t *testing.T) {
	withFakeFindAshdSocketPIDs(t, func(string) []int { return []int{4242} })
	err := checkNoOtherAshd("/tmp/contested.sock")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "another ashd is bound") {
		t.Errorf("error should mention double-bind refusal: %v", err)
	}
	if !strings.Contains(err.Error(), "4242") {
		t.Errorf("error should name the surviving PID 4242: %v", err)
	}
	if !strings.Contains(err.Error(), "ash stop") {
		t.Errorf("error should suggest `ash stop`: %v", err)
	}
}

// TestCheckNoOtherAshd_MultiplePIDsAllReported covers the "stop didn't
// fully clean up" scenario from the ticket: multiple ashd processes
// share the socket and the new daemon must name them all.
func TestCheckNoOtherAshd_MultiplePIDsAllReported(t *testing.T) {
	withFakeFindAshdSocketPIDs(t, func(string) []int { return []int{1111, 2222} })
	err := checkNoOtherAshd("/tmp/triple.sock")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	for _, want := range []string{"1111", "2222"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should list pid %s: %v", want, err)
		}
	}
}
