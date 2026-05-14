package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/stazelabs/ash/internal/ledger"
	"github.com/stazelabs/ash/internal/vocab"
)

const (
	defaultOutDir = "docs/vocab"
	mdName        = "inventory.md"
	jsonName      = "inventory.json"
)

func runGen(args []string) int {
	outDir := defaultOutDir
	if len(args) > 0 {
		outDir = args[0]
	}
	repoRoot, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "getwd: %v\n", err)
		return 1
	}
	md, jsn, err := generate(repoRoot)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "mkdir %s: %v\n", outDir, err)
		return 1
	}
	mdPath := filepath.Join(outDir, mdName)
	jsonPath := filepath.Join(outDir, jsonName)
	if err := os.WriteFile(mdPath, md, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "write %s: %v\n", mdPath, err)
		return 1
	}
	if err := os.WriteFile(jsonPath, jsn, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "write %s: %v\n", jsonPath, err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "wrote %s\nwrote %s\n", mdPath, jsonPath)
	return 0
}

func runCheck(args []string) int {
	outDir := defaultOutDir
	if len(args) > 0 {
		outDir = args[0]
	}
	repoRoot, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "getwd: %v\n", err)
		return 1
	}
	md, jsn, err := generate(repoRoot)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	mdPath := filepath.Join(outDir, mdName)
	jsonPath := filepath.Join(outDir, jsonName)
	gotMd, err := os.ReadFile(mdPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "vocab check: %s missing or unreadable: %v\n", mdPath, err)
		fmt.Fprintln(os.Stderr, "fix: run `make vocab` and commit the result.")
		return 1
	}
	gotJSON, err := os.ReadFile(jsonPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "vocab check: %s missing or unreadable: %v\n", jsonPath, err)
		fmt.Fprintln(os.Stderr, "fix: run `make vocab` and commit the result.")
		return 1
	}
	mdDrift := !bytes.Equal(md, gotMd)
	jsonDrift := !bytes.Equal(jsn, gotJSON)
	if !mdDrift && !jsonDrift {
		fmt.Fprintln(os.Stderr, "vocab check: ok")
		return 0
	}
	if mdDrift {
		fmt.Fprintf(os.Stderr, "vocab check: %s is out of date\n", mdPath)
	}
	if jsonDrift {
		fmt.Fprintf(os.Stderr, "vocab check: %s is out of date\n", jsonPath)
	}
	fmt.Fprintln(os.Stderr, "fix: run `make vocab` and commit the result.")
	return 1
}

func generate(repoRoot string) (md []byte, jsn []byte, err error) {
	counter, err := ledger.NewCounter()
	if err != nil {
		return nil, nil, fmt.Errorf("ledger.NewCounter: %w", err)
	}
	inv, err := vocab.Generate(repoRoot, counter)
	if err != nil {
		return nil, nil, fmt.Errorf("vocab.Generate: %w", err)
	}
	jsn, err = inv.MarshalJSONIndent()
	if err != nil {
		return nil, nil, fmt.Errorf("marshal json: %w", err)
	}
	jsn = append(jsn, '\n')
	md = []byte(inv.Markdown())
	return md, jsn, nil
}
