package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stazelabs/ash/internal/verbs/help"
)

// TestRenderUsage_Golden snapshots the rendered usage string and fails if it
// drifts from the golden file. Run UPDATE_GOLDEN=1 go test ./cmd/ash/ to
// regenerate after intentional changes.
//
// This is the protective test called out in ASH-42: adding a new verb or op
// touches only the registry; this test ensures usage stays in sync.
func TestRenderUsage_Golden(t *testing.T) {
	got := help.RenderUsage(100)

	goldenPath := filepath.Join("testdata", "usage_golden.txt")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatalf("mkdir testdata: %v", err)
		}
		if err := os.WriteFile(goldenPath, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		t.Logf("golden updated: %s", goldenPath)
		return
	}

	data, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden (%s): %v\n\nRun: UPDATE_GOLDEN=1 go test ./cmd/ash/ -run TestRenderUsage_Golden", goldenPath, err)
	}
	if string(data) != got {
		t.Errorf("usage output differs from golden.\n\nRun UPDATE_GOLDEN=1 go test ./cmd/ash/ -run TestRenderUsage_Golden to update.\n\ngot:\n%s\n\nwant:\n%s", got, string(data))
	}
}

// TestRenderUsage_AllVerbsPresent verifies every verb in the registry appears
// somewhere in the usage output. This catches the case where verbDisplayOrder
// in help.go is missing a newly registered verb.
func TestRenderUsage_AllVerbsPresent(t *testing.T) {
	got := help.RenderUsage(100)
	schemas := help.Registry()
	for _, vs := range schemas {
		if !strings.Contains(got, "  "+vs.Verb) {
			t.Errorf("verb %q not found in usage output", vs.Verb)
		}
	}
}
