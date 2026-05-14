package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// corpusCase is one row in the workload. Name becomes the corpus filename.
type corpusCase struct {
	Name string
	Argv []string
}

var corpusCases = []corpusCase{
	{"read-small", []string{"read", "--path", "go.mod"}},
	{"read-medium", []string{"read", "--path", "docs/cli-tokens.md"}},
	{"read-large", []string{"read", "--path", "docs/encodings.md"}},
	{"find-shallow", []string{"find", "--path", "cmd"}},
	{"find-deep-glob", []string{"find", "--path", ".", "--glob", "**/*.go", "--limit", "200"}},
	{"grep-common", []string{"grep", "--path", "internal/verbs", "--pattern", "func "}},
	{"grep-error-code", []string{"grep", "--path", "internal/verbs", "--pattern", "ErrCode"}},
	{"git-log", []string{"git", "--op", "log", "--limit", "20"}},
	{"git-status", []string{"git", "--op", "status"}},
	{"metrics-last", []string{"metrics", "--last", "20"}},
	{"report-session", []string{"report"}},
	{"help-all", []string{"help"}},
	{"help-find", []string{"help", "--verb", "find"}},
	{"help-grep", []string{"help", "--verb", "grep"}},
	{"err-not-found", []string{"read", "--path", "definitely_not_a_file_xyzzy.txt"}},
	{"err-bad-range", []string{"read", "--path", "go.mod", "--range", "999999:1000000"}},
}

func runCorpus(args []string) {
	fs := flag.NewFlagSet("corpus", flag.ExitOnError)
	outDir := fs.String("out", "testdata/corpus", "output directory")
	ashBin := fs.String("ash", "bin/ash", "path to ash binary")
	_ = fs.Parse(args)

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		die("mkdir %s: %v", *outDir, err)
	}

	for _, c := range corpusCases {
		path := filepath.Join(*outDir, c.Name+".txt")
		cmd := exec.Command(*ashBin, c.Argv...)
		// Always pretty-format; that is what the ledger tokenizes.
		cmd.Env = append(os.Environ(), "ASH_FORMAT=pretty")
		// Capture stdout only — that's the body the agent reads.
		out, _ := cmd.Output()
		// On error verbs (err-* cases) the daemon exits non-zero but stdout
		// still contains the pretty error envelope; keep whatever was emitted.
		if err := os.WriteFile(path, out, 0o644); err != nil {
			die("write %s: %v", path, err)
		}
		fmt.Fprintf(os.Stderr, "corpus: %-24s -> %s  (%d B)\n", c.Name, path, len(out))
	}

	// Write a manifest so measure mode can iterate deterministically.
	manifest := filepath.Join(*outDir, "manifest.txt")
	var lines []string
	for _, c := range corpusCases {
		lines = append(lines, c.Name+".txt")
	}
	if err := os.WriteFile(manifest, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		die("write manifest: %v", err)
	}
}
