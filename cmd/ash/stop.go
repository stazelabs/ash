// Stop handling for `ash stop` — a client-side verb that sends SIGTERM to
// the per-project ashd daemon and waits for clean exit.
//
// Flow:
//  1. Derive root from cwd (same as the main dispatch path).
//  2. Call stop.Run with the PID file path.
//  3. Render and print the result (respecting --format).
//  4. Exit 0 on success or already_stopped; exit 1 on OS error or timeout.
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/stazelabs/ash/internal/proto"
	"github.com/stazelabs/ash/internal/session"
	"github.com/stazelabs/ash/internal/verbs/stop"
)

func runStop(root, format string) {
	result, err := stop.StopDaemon(session.PIDPath(root))
	if err != nil {
		fmt.Fprintln(os.Stderr, "ash stop:", err)
		os.Exit(1)
	}

	rsp := &proto.Response{
		V:    proto.ProtocolVersion,
		OK:   result.Status != "timeout",
		Data: proto.MustData(result),
	}
	if result.Status == "timeout" {
		rsp.Err = &proto.Error{
			Code: "timeout",
			Msg:  fmt.Sprintf("daemon (pid %d) did not exit within %dms", result.PID, result.ElapsedMs),
		}
	}

	switch format {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rsp); err != nil {
			fmt.Fprintln(os.Stderr, "ash stop: json encode:", err)
			os.Exit(1)
		}
	case "msgpack":
		b, err := proto.EncodeResponse(rsp)
		if err != nil {
			fmt.Fprintln(os.Stderr, "ash stop: msgpack encode:", err)
			os.Exit(1)
		}
		if _, err := os.Stdout.Write(b); err != nil {
			fmt.Fprintln(os.Stderr, "ash stop: write:", err)
			os.Exit(1)
		}
	default: // "pretty"
		fmt.Println(stop.PrettyResult(result))
	}

	if !rsp.OK {
		os.Exit(1)
	}
}
