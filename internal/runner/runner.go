// Package runner provides a shared subprocess helper for verbs that exec
// system tools. It handles binary lookup, stdout/stderr capture, and tracer
// integration. Non-zero exits are returned in Result.ExitCode — callers
// produce domain-specific proto.Errors (e.g. "not_a_repo" vs "git_failed").
package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"time"

	"github.com/stazelabs/ash/internal/proto"
)

// Result holds the output of one subprocess invocation.
type Result struct {
	Stdout    []byte
	Stderr    []byte
	ExitCode  int
	Latency   time.Duration
	Truncated bool // true when stdout was capped at opts.MaxStdout
}

// Opts configures a Run call.
type Opts struct {
	// Tracer receives an AddIO call with the subprocess wall time, and
	// supplies the parent context so a cancelled request (conn close /
	// KindCancel) kills the subprocess instead of leaking a wedged
	// handler goroutine. nil is OK.
	Tracer *proto.Tracer
	// MaxStdout caps stdout bytes. 0 = no cap (uses a plain bytes.Buffer).
	MaxStdout int
	// Timeout, when > 0, kills the subprocess if it runs longer and the
	// call returns a "<prog>_timeout" proto.Error. 0 = no timeout
	// (ASH-215).
	Timeout time.Duration
}

// Run looks up prog on PATH, executes it with args, and captures stdout and
// stderr. A *proto.Error is returned only for binary-not-found, a timeout, or
// exec/setup failures. Non-zero exits populate Result.ExitCode without
// returning an error, so callers can map exit codes to domain-specific
// proto.Errors.
func Run(prog string, args []string, opts Opts) (*Result, *proto.Error) {
	binPath, err := exec.LookPath(prog)
	if err != nil {
		return nil, &proto.Error{
			Code: prog + "_not_found",
			Msg:  fmt.Sprintf("%s binary not on PATH", prog),
		}
	}

	// Derive the parent context from the tracer so request cancellation
	// reaches the subprocess; opts.Timeout adds a hard ceiling on top.
	ctx := context.Background()
	if opts.Tracer != nil {
		if c := opts.Tracer.Context(); c != nil {
			ctx = c
		}
	}
	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, binPath, args...)
	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	if opts.MaxStdout > 0 {
		return runCapped(ctx, cmd, prog, &stderrBuf, opts)
	}
	return runDirect(ctx, cmd, prog, &stderrBuf, opts)
}

// timeoutError reports whether ctx ended in a deadline, and builds the
// corresponding proto.Error. It is checked after a non-nil run error so a
// killed-by-timeout subprocess is not misreported as a normal failure.
func timeoutError(ctx context.Context, prog string, d time.Duration) *proto.Error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return &proto.Error{
			Code: prog + "_timeout",
			Msg:  fmt.Sprintf("%s exceeded its %s timeout", prog, d),
		}
	}
	return nil
}

// runDirect captures stdout into a plain buffer — no size cap.
func runDirect(ctx context.Context, cmd *exec.Cmd, prog string, stderrBuf *bytes.Buffer, opts Opts) (*Result, *proto.Error) {
	var stdoutBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf

	start := time.Now()
	runErr := cmd.Run()
	elapsed := time.Since(start)
	if opts.Tracer != nil {
		opts.Tracer.AddIO(elapsed)
	}

	res := &Result{
		Stdout:  stdoutBuf.Bytes(),
		Stderr:  stderrBuf.Bytes(),
		Latency: elapsed,
	}
	if runErr != nil {
		if te := timeoutError(ctx, prog, opts.Timeout); te != nil {
			return nil, te
		}
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			res.ExitCode = exitErr.ExitCode()
			return res, nil
		}
		return nil, &proto.Error{Code: prog + "_failed", Msg: runErr.Error()}
	}
	return res, nil
}

// runCapped pipes stdout through a LimitedReader so we never buffer more than
// opts.MaxStdout bytes. Used by callers that may receive large outputs.
func runCapped(ctx context.Context, cmd *exec.Cmd, prog string, stderrBuf *bytes.Buffer, opts Opts) (*Result, *proto.Error) {
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, &proto.Error{Code: prog + "_failed", Msg: "stdout pipe: " + err.Error()}
	}

	start := time.Now()
	if err := cmd.Start(); err != nil {
		return nil, &proto.Error{Code: prog + "_failed", Msg: "start: " + err.Error()}
	}

	var stdoutBuf bytes.Buffer
	limited := &io.LimitedReader{R: pipe, N: int64(opts.MaxStdout) + 1}
	_, _ = io.Copy(&stdoutBuf, limited)

	truncated := stdoutBuf.Len() > opts.MaxStdout
	if truncated {
		stdoutBuf.Truncate(opts.MaxStdout)
		_, _ = io.Copy(io.Discard, pipe)
	}

	waitErr := cmd.Wait()
	elapsed := time.Since(start)
	if opts.Tracer != nil {
		opts.Tracer.AddIO(elapsed)
	}

	res := &Result{
		Stdout:    stdoutBuf.Bytes(),
		Stderr:    stderrBuf.Bytes(),
		Latency:   elapsed,
		Truncated: truncated,
	}
	if waitErr != nil {
		if te := timeoutError(ctx, prog, opts.Timeout); te != nil {
			return nil, te
		}
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			res.ExitCode = exitErr.ExitCode()
			return res, nil
		}
		return nil, &proto.Error{Code: prog + "_failed", Msg: "wait: " + waitErr.Error()}
	}
	return res, nil
}
