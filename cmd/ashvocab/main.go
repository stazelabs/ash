// ashvocab regenerates ash's vocabulary inventory — every stable
// agent-facing string in the verb surface, with cl100k_base token
// counts and source-code locations. The output is the substrate for
// future tokenizer-aware passes (ASH-98, ASH-100, etc.).
//
//	ashvocab gen   [docs/vocab/]   regenerate inventory.{md,json}
//	ashvocab check [docs/vocab/]   regenerate to a temp dir and diff
//	                               against the checked-in artifact;
//	                               exit 1 on drift. Used by CI.
//
// Run from the repo root.
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "gen":
		os.Exit(runGen(os.Args[2:]))
	case "check":
		os.Exit(runCheck(os.Args[2:]))
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: ashvocab gen   [out-dir]   regenerate docs/vocab/{inventory.md,inventory.json}")
	fmt.Fprintln(os.Stderr, "       ashvocab check [out-dir]   regenerate to a temp dir, diff against checked-in artifact")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "default out-dir: docs/vocab/")
}
