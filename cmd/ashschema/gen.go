package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/stazelabs/ash/internal/mcpschema"
	"github.com/stazelabs/ash/internal/verbs/help"
)

const (
	defaultOutDir = "docs/mcp"
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
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "mkdir %s: %v\n", outDir, err)
		return 1
	}
	path := filepath.Join(outDir, artifactName)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "write %s: %v\n", path, err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "wrote %s\n", path)
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
	path := filepath.Join(outDir, artifactName)
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
	fmt.Fprintln(os.Stderr, "schema check: ok")
	return 0
}

func generate() ([]byte, error) {
	tl, err := mcpschema.Generate(help.Registry())
	if err != nil {
		return nil, fmt.Errorf("mcpschema.Generate: %w", err)
	}
	body, err := tl.Marshal()
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}
	return body, nil
}
