// Command ashd-clean discovers and optionally reaps abandoned ashd
// daemons across every project on the current host. Per-project ash is
// correctness-safe (each project owns its own socket, ledger, and
// pidfile), but `ash stop` only knows about the current project — over
// weeks of working across many repos, abandoned daemons accumulate.
// Each holds ~10–20 MB RSS, a tokenizer, and a SQLite handle on its
// ledger. When the daemon's project directory has been deleted, the
// open file blocks reclamation entirely.
//
// Usage:
//
//	ashd-clean                report-only — list every ashd and its status
//	ashd-clean --cleanup      SIGTERM zombies (root missing or socket unreachable)
//	ashd-clean --format json  machine-readable output
//
// Healthy daemons are never signalled by this tool — to stop a healthy
// daemon, use `ash stop` from inside that project. Unknown daemons
// (couldn't parse argv) are reported but never signalled.
//
// Safe to run as a cron job or login hook: report-only by default,
// classifier never false-positives on a busy-but-healthy daemon (a
// reachable socket is enough to mark alive).
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/stazelabs/ash/internal/daemoncleanup"
	"github.com/stazelabs/ash/internal/verbs/stop"
)

// output is the JSON envelope. Field order is fixed so machine readers
// don't have to handle drift between runs.
type output struct {
	Daemons []daemoncleanup.Daemon `json:"daemons"`
	Killed  []stop.Orphan          `json:"killed,omitempty"`
}

func main() {
	var (
		doClean = flag.Bool("cleanup", false,
			"SIGTERM zombie daemons (root missing or socket unreachable)")
		format = flag.String("format", "pretty",
			"output format: pretty|json")
	)
	flag.Usage = usage
	flag.Parse()
	if flag.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "ashd-clean: unexpected positional argument(s): %v\n", flag.Args())
		usage()
		os.Exit(2)
	}

	daemons := daemoncleanup.Scan()
	var killed []stop.Orphan
	if *doClean {
		killed = daemoncleanup.Cleanup(daemons)
	}

	switch *format {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(output{Daemons: daemons, Killed: killed}); err != nil {
			fmt.Fprintln(os.Stderr, "ashd-clean: json encode:", err)
			os.Exit(1)
		}
	case "pretty":
		renderPretty(daemons, killed, *doClean)
	default:
		fmt.Fprintf(os.Stderr, "ashd-clean: unknown --format %q (want pretty|json)\n", *format)
		os.Exit(2)
	}
}

func renderPretty(daemons []daemoncleanup.Daemon, killed []stop.Orphan, didClean bool) {
	if len(daemons) == 0 {
		fmt.Println("§ashd-clean: no ashd processes found")
		return
	}
	var alive, zombie, unknown int
	for _, d := range daemons {
		switch d.Status {
		case daemoncleanup.StatusAlive:
			alive++
		case daemoncleanup.StatusZombie:
			zombie++
		case daemoncleanup.StatusUnknown:
			unknown++
		}
	}
	fmt.Printf("§ashd-clean: %d ashd process(es): %d alive, %d zombie, %d unknown\n",
		len(daemons), alive, zombie, unknown)

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "PID\tSTATUS\tROOT\tSOCKET\tNOTE")
	for _, d := range daemons {
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\n",
			d.PID, d.Status, dashIfEmpty(d.Root), dashIfEmpty(d.Socket), d.Reason)
	}
	_ = tw.Flush()

	if didClean {
		if len(killed) == 0 {
			fmt.Println("cleanup: no zombies to signal")
			return
		}
		fmt.Printf("cleanup: signalled %d zombie(s)\n", len(killed))
		for _, o := range killed {
			exited := "no"
			if o.Exited {
				exited = "yes"
			}
			fmt.Printf("  pid=%d signal=%s exited=%s elapsed=%dms\n",
				o.PID, o.Signal, exited, o.ElapsedMs)
		}
		return
	}
	if zombie > 0 {
		fmt.Println("\nrun `ashd-clean --cleanup` to SIGTERM the zombie(s)")
	}
}

func dashIfEmpty(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: ashd-clean [--cleanup] [--format pretty|json]")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Discovers all ashd processes on this host and classifies each as")
	fmt.Fprintln(os.Stderr, "alive (root exists AND socket dialable), zombie (root missing or")
	fmt.Fprintln(os.Stderr, "socket unreachable), or unknown (couldn't parse --root/--socket).")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Default behavior is report-only. Pass --cleanup to SIGTERM zombies.")
	fmt.Fprintln(os.Stderr, "Healthy daemons are never touched; use `ash stop` per-project.")
}
