package envprobe

import (
	"os"
	"testing"
)

// TestEnvProbe is the load-bearing assertion for the ASH-132 wire-level
// integration test. Behavior:
//
//   - ASH_132_PROBE unset  → t.Skip (default — keeps `go test ./...`
//     green for developers who don't know this fixture exists).
//   - ASH_132_PROBE=expected → t.Pass.
//   - ASH_132_PROBE=<other>  → t.Fatal.
//
// The asymmetric pass/fail by env value lets the integration test prove
// the *exact* string the client sent reaches the subprocess, not just
// "something env-shaped was forwarded".
func TestEnvProbe(t *testing.T) {
	v, ok := os.LookupEnv("ASH_132_PROBE")
	if !ok {
		t.Skip("ASH_132_PROBE not set; exercised only via ASH-132 integration test")
	}
	if v != "expected" {
		t.Fatalf("ASH_132_PROBE=%q, want %q (client env not forwarded correctly)", v, "expected")
	}
}
