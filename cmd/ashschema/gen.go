package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/stazelabs/ash/internal/mcpschema"
	"github.com/stazelabs/ash/internal/verbs"
	"github.com/stazelabs/ash/internal/verbs/help"
)

// The schema artifact is checked in at two paths:
//
//   - docs/mcp/tools.json    — canonical, human-facing artifact
//   - cmd/ashmcp/tools.json  — //go:embed source for ASH-104 (ashmcp); must
//     live inside the package directory because Go embed cannot reach across
//     package boundaries.
//
// Both are regenerated together by `make schema` and gated by
// `make schema-check`, so the duplication cannot silently drift.
const (
	defaultOutDir = "docs/mcp"
	embedOutDir   = "cmd/ashmcp"
	artifactName  = "tools.json"
)

func runGen(args []string) int {
	outDir := defaultOutDir
	if len(args) > 0 {
		outDir = args[0]
	}
	body, err := generate()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	for _, d := range outDirs(outDir) {
		if err := writeArtifact(d, body); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	return 0
}

func runCheck(args []string) int {
	outDir := defaultOutDir
	if len(args) > 0 {
		outDir = args[0]
	}
	body, err := generate()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	for _, d := range outDirs(outDir) {
		path := filepath.Join(d, artifactName)
		got, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "schema check: %s missing or unreadable: %v\n", path, err)
			fmt.Fprintln(os.Stderr, "fix: run `make schema` and commit the result.")
			return 1
		}
		if !bytes.Equal(body, got) {
			fmt.Fprintf(os.Stderr, "schema check: %s is out of date\n", path)
			fmt.Fprintln(os.Stderr, "fix: run `make schema` and commit the result.")
			return 1
		}
	}
	fmt.Fprintln(os.Stderr, "schema check: ok")
	return 0
}

// outDirs returns every directory the artifact must be written to. When the
// caller overrides the canonical out-dir (test fixture, ad-hoc run) we
// honor that and skip the embed sibling — the override path is for one-off
// inspection, not for replacing the build-time embed source.
func outDirs(canonical string) []string {
	if canonical != defaultOutDir {
		return []string{canonical}
	}
	return []string{defaultOutDir, embedOutDir}
}

func writeArtifact(dir string, body []byte) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	path := filepath.Join(dir, artifactName)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	fmt.Fprintf(os.Stderr, "wrote %s\n", path)
	return nil
}

func generate() ([]byte, error) {
	// repoRoot anchors the AST walker for OutputSchema generation
	// (ASH-124). Both 'make schema' and 'make schema-check' invoke this
	// binary from the repo root, so os.Getwd() is the canonical anchor.
	root, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("getwd: %w", err)
	}
	compactVerbs := map[string]bool{}
	for v := range verbs.CompactHandlers() {
		compactVerbs[v] = true
	}
	tl, err := mcpschema.Generate(root, help.Registry(), compactVerbs)
	if err != nil {
		return nil, fmt.Errorf("mcpschema.Generate: %w", err)
	}
	body, err := tl.Marshal()
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}
	return body, nil
}
