package main

import (
	"fmt"
	"os"

	"github.com/stazelabs/ash/internal/ledger"
)

// runProbe prints the cl100k_base token count for each arg.
// Useful for verification spot-checks.
func runProbe(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: encexplore probe <text>...")
		os.Exit(2)
	}
	counter, err := ledger.NewCounter()
	if err != nil {
		die("counter: %v", err)
	}
	for _, s := range args {
		fmt.Printf("%4d  %q\n", counter.Count(s), s)
	}
}
