// encexplore is a throwaway tool that supports the encoding-substitution
// exploration documented in docs/encodings.md. It has three modes:
//
//	encexplore atlas [out-path]      build the single-token Unicode atlas
//	encexplore probe '<text>'        print token counts for a string
//	encexplore corpus [out-dir]      capture a workload of ash pretty responses
//	encexplore measure [corpus-dir]  apply substitution tables and print deltas
//
// All measurement uses internal/ledger.NewCounter (cl100k_base) directly.
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
	case "atlas":
		runAtlas(os.Args[2:])
	case "probe":
		runProbe(os.Args[2:])
	case "corpus":
		runCorpus(os.Args[2:])
	case "measure":
		runMeasure(os.Args[2:])
	case "validate":
		runValidate(os.Args[2:])
	case "glyphsweep":
		runGlyphSweep(os.Args[2:])
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: encexplore <atlas|probe|corpus|measure|validate|glyphsweep> [args...]")
}
