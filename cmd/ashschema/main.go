// ashschema regenerates ash's MCP tool-schema artifact — JSON Schema
// draft 2020-12 tool definitions derived from internal/verbs/help.Registry,
// the same source-of-truth that produces `ash help` text and the vocab
// inventory in docs/vocab/. Output: docs/mcp/tools.json.
//
// ASH-105 closed the one-source-of-truth pivot: three artifacts, one
// registry, drift caught at CI time.
//
//	ashschema gen   [docs/mcp/]   regenerate tools.json
//	ashschema check [docs/mcp/]   regenerate to a temp dir and diff
//	                              against the checked-in artifact;
//	                              exit 1 on drift. Used by CI.
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
	fmt.Fprintln(os.Stderr, "usage: ashschema gen   [out-dir]   regenerate docs/mcp/tools.json")
	fmt.Fprintln(os.Stderr, "       ashschema check [out-dir]   regenerate, diff against checked-in artifact")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "default out-dir: docs/mcp/")
}
