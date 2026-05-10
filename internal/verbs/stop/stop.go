// Package stop implements the `ash stop` verb — a pure client-side verb that
// sends SIGTERM to the per-project ashd daemon and waits for clean exit.
//
// Unlike every other ash verb, stop never contacts the daemon: it is fully
// client-side bookkeeping. The client intercepts "stop" before dialOrStart
// (see cmd/ash/stop.go).
//
// Args: none. The project root is derived from the client's cwd.
//
// Result: {pid, signal_sent, exited_within_grace, elapsed_ms, status}
//   - status "stopped"         — SIGTERM delivered, process exited within grace.
//   - status "already_stopped" — no PID file, stale PID file, or process already gone.
//   - status "timeout"         — SIGTERM delivered but process did not exit within waitTimeout.
package stop

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/stazelabs/ash/internal/proto"
)

const (
	pollInterval = 50 * time.Millisecond
	// waitTimeout is DefaultShutdownGrace (5s) + 2s buffer.
	waitTimeout = 7 * time.Second
)

// Result is the structured output of ash stop.
type Result struct {
	PID               int    `msgpack:"pid"`
	SignalSent        bool   `msgpack:"signal_sent"`
	ExitedWithinGrace bool   `msgpack:"exited_within_grace"`
	ElapsedMs         int64  `msgpack:"elapsed_ms"`
	Status            string `msgpack:"status"` // "stopped" | "already_stopped" | "timeout"
}

// StopDaemon sends SIGTERM to the daemon at pidPath and waits for clean exit.
// Returns a non-nil error only for unexpected OS failures (e.g. permission
// denied reading the PID file). "No daemon running" is not an error — it
// returns status "already_stopped".
func StopDaemon(pidPath string) (*Result, error) {
	pidData, err := os.ReadFile(pidPath)
	if err != nil {
		if os.IsNotExist(err) {
			return &Result{Status: "already_stopped"}, nil
		}
		return nil, fmt.Errorf("reading PID file: %w", err)
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(pidData)))
	if err != nil || pid <= 0 {
		return &Result{Status: "already_stopped"}, nil
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		return &Result{PID: pid, Status: "already_stopped"}, nil
	}

	// Check liveness before signalling.
	if err := proc.Signal(syscall.Signal(0)); err != nil {
		return &Result{PID: pid, Status: "already_stopped"}, nil
	}

	start := time.Now()
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		// Died between liveness check and signal — treat as already stopped.
		return &Result{PID: pid, Status: "already_stopped", ElapsedMs: time.Since(start).Milliseconds()}, nil
	}

	// Poll until exit or timeout.
	deadline := time.Now().Add(waitTimeout)
	for time.Now().Before(deadline) {
		time.Sleep(pollInterval)
		if err := proc.Signal(syscall.Signal(0)); err != nil {
			return &Result{
				PID:               pid,
				SignalSent:        true,
				ExitedWithinGrace: true,
				ElapsedMs:         time.Since(start).Milliseconds(),
				Status:            "stopped",
			}, nil
		}
	}

	return &Result{
		PID:               pid,
		SignalSent:        true,
		ExitedWithinGrace: false,
		ElapsedMs:         time.Since(start).Milliseconds(),
		Status:            "timeout",
	}, nil
}

// Args is the (empty) daemon-side arg type. stop has no daemon-side args.
type Args struct{}

// ParseArgs satisfies the daemon runner interface. stop has no daemon-side args.
func ParseArgs(_ map[string]any) (*Args, *proto.Error) { return &Args{}, nil }

// Run is the daemon-side runner. stop is a client-only verb; the daemon
// returns client_only so the parity check and token-counting invariants hold.
func Run(_ *Args, _ *proto.Tracer) (*Result, *proto.Error) {
	return nil, &proto.Error{Code: "client_only", Msg: "verb is client-side only", Hint: "'ash stop' runs in the client; the daemon cannot receive it"}
}

// PrettyResponse renders a human-readable stop result. The rsp.Data field
// must contain a msgpack-encoded Result (as produced by proto.MustData).
func PrettyResponse(_ *proto.Request, rsp *proto.Response) string {
	if !rsp.OK {
		return proto.PrettyResponseHeader(rsp)
	}
	var r Result
	if err := proto.UnmarshalData(rsp, &r); err != nil {
		return "ok\n<unrecognized stop result>"
	}
	return PrettyResult(&r)
}

// PrettyResult renders a Result directly, without a proto.Response wrapper.
func PrettyResult(r *Result) string {
	var b strings.Builder
	switch r.Status {
	case "already_stopped":
		b.WriteString("=== ash stop: no daemon running ===\n")
		if r.PID > 0 {
			fmt.Fprintf(&b, "pid:     %d (already gone)\n", r.PID)
		}
	case "timeout":
		fmt.Fprintf(&b, "=== ash stop: timeout after %dms ===\n", r.ElapsedMs)
		fmt.Fprintf(&b, "pid:     %d\n", r.PID)
		b.WriteString("signal:  sent (SIGTERM)\n")
		b.WriteString("exited:  no — process still running after timeout\n")
	default: // "stopped"
		fmt.Fprintf(&b, "=== ash stop: stopped (%dms) ===\n", r.ElapsedMs)
		fmt.Fprintf(&b, "pid:     %d\n", r.PID)
		b.WriteString("signal:  sent (SIGTERM)\n")
		b.WriteString("exited:  yes\n")
	}
	return strings.TrimRight(b.String(), "\n")
}
