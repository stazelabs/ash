// Package runner provides a shared subprocess helper for verbs that exec
// system tools. It handles binary lookup, stdout/stderr capture, and tracer
// integration. Non-zero exits are returned in Result.ExitCode — callers
// produce domain-specific proto.Errors (e.g. "not_a_repo" vs "git_failed").
package runner

import (
	"bytes"
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
	// Tracer receives an AddIO call with the subprocess wall time. nil is OK.
	Tracer *proto.Tracer
	// MaxStdout caps stdout bytes. 0 = no cap (uses a plain bytes.Buffer).
	MaxStdout int
}

// Run looks up prog on PATH, executes it with args, and captures stdout and
// stderr. A *proto.Error is returned only for binary-not-found or exec/setup
// failures. Non-zero exits populate Result.ExitCode without returning an
// error, so callers can map exit codes to domain-specific proto.Errors.
func Run(prog string, args []string, opts Opts) (*Result, *proto.Error) {
	binPath, err := exec.LookPath(prog)
	if err != nil {
		return nil, &proto.Error{
			Code: prog + "_not_found",
			Msg:  fmt.Sprintf("%s binary not on PATH", prog),
		}
	}

	cmd := exec.Command(binPath, args...)
	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	if opts.MaxStdout > 0 {
		return runCapped(cmd, prog, &stderrBuf, opts)
	}
	return runDirect(cmd, prog, &stderrBuf, opts)
}

// runDirect captures stdout into a plain buffer — no size cap.
func runDirect(cmd *exec.Cmd, prog string, stderrBuf *bytes.Buffer, opts Opts) (*Result, *proto.Error) {
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
func runCapped(cmd *exec.Cmd, prog string, stderrBuf *bytes.Buffer, opts Opts) (*Result, *proto.Error) {
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
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			res.ExitCode = exitErr.ExitCode()
			return res, nil
		}
		return nil, &proto.Error{Code: prog + "_failed", Msg: "wait: " + waitErr.Error()}
	}
	return res, nil
}
