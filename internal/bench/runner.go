package bench

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"time"
)

// MaxBashStdoutBytes caps how much stdout we read from the bash
// equivalent. 16 MiB matches the per-file cap ash grep enforces and
// keeps a runaway query (e.g. grep on a deep tree with no skipping)
// from inflating the daemon's RSS.
const MaxBashStdoutBytes = 16 << 20

// DefaultBashTimeout bounds a single bash invocation. Cases should
// finish in well under this; the timeout exists to keep a hung process
// from stalling the bench run.
const DefaultBashTimeout = 30 * time.Second

// BashResult is the captured output of one bash invocation.
type BashResult struct {
	Argv     []string      // exactly what was executed
	Stdout   []byte        // captured stdout, capped at MaxBashStdoutBytes
	Stderr   []byte        // captured stderr; small, kept for diagnosis
	ExitCode int           // process exit code; non-zero is informational, not a bench failure
	Latency  time.Duration // wall-clock duration of the subprocess
	Truncate bool          // true if stdout hit the cap
	RunErr   string        // error from running (timeout, exec failure); "" on normal completion
}

// RunBash executes argv with a timeout, captures stdout (capped) and
// stderr, returns a BashResult. argv[0] is the program; PATH is used.
//
// The function does not return an error for non-zero exit — bash tools
// like grep return 1 on no-match, which is a normal outcome we want
// the bench to record, not abort on. Only setup/exec/timeout failures
// are signalled via RunErr.
func RunBash(ctx context.Context, argv []string) BashResult {
	if len(argv) == 0 {
		return BashResult{RunErr: "empty argv"}
	}
	res := BashResult{Argv: argv}

	if _, lookErr := exec.LookPath(argv[0]); lookErr != nil {
		res.RunErr = fmt.Sprintf("lookup %s: %v", argv[0], lookErr)
		return res
	}

	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultBashTimeout)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		res.RunErr = "stdout pipe: " + err.Error()
		return res
	}
	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	start := time.Now()
	if err := cmd.Start(); err != nil {
		res.RunErr = "start: " + err.Error()
		return res
	}

	stdoutBuf := &bytes.Buffer{}
	limited := &io.LimitedReader{R: stdoutPipe, N: MaxBashStdoutBytes + 1}
	if _, err := io.Copy(stdoutBuf, limited); err != nil {
		// Reader errors are usually benign here (pipe closed). Record but
		// keep going to drain Wait.
		res.RunErr = "read stdout: " + err.Error()
	}
	if stdoutBuf.Len() > MaxBashStdoutBytes {
		res.Truncate = true
		stdoutBuf.Truncate(MaxBashStdoutBytes)
		// Drain remainder so the child can exit cleanly.
		_, _ = io.Copy(io.Discard, stdoutPipe)
	}

	waitErr := cmd.Wait()
	res.Latency = time.Since(start)
	res.Stdout = stdoutBuf.Bytes()
	res.Stderr = stderrBuf.Bytes()

	if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			res.ExitCode = exitErr.ExitCode()
			// Non-zero exit is informational (grep returns 1 on no
			// matches; that's not a bench failure).
		} else if ctx.Err() != nil {
			res.RunErr = "timeout: " + ctx.Err().Error()
		} else {
			res.RunErr = "wait: " + waitErr.Error()
		}
	}

	return res
}
