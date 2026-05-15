// Command ashd is the ash daemon. It listens on a per-project Unix domain
// socket, dispatches verb requests, records every call to the SQLite ledger,
// and replies with the structured response envelope.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/stazelabs/ash/internal/config"
	"github.com/stazelabs/ash/internal/jail"
	"github.com/stazelabs/ash/internal/ledger"
	"github.com/stazelabs/ash/internal/proto"
	"github.com/stazelabs/ash/internal/session"
	"github.com/stazelabs/ash/internal/verbs"
	"github.com/stazelabs/ash/internal/verbs/git"
	"github.com/vmihailenco/msgpack/v5"
)

func main() {
	daemonStart := time.Now()
	var rootFlag, sockFlag, logFlag string
	flag.StringVar(&rootFlag, "root", "", "project root (required)")
	flag.StringVar(&sockFlag, "socket", "", "unix socket path (required)")
	flag.StringVar(&logFlag, "log", "", "log file path (optional, default <root>/.ash/ashd.log)")
	flag.Parse()

	if rootFlag == "" || sockFlag == "" {
		log.Fatal("ashd: --root and --socket are required")
	}

	if err := os.Chdir(rootFlag); err != nil {
		log.Fatalf("ashd: chdir: %v", err)
	}
	if err := session.EnsureRuntimeDirs(rootFlag); err != nil {
		log.Fatalf("ashd: runtime dirs: %v", err)
	}

	if logFlag == "" {
		logFlag = session.LogPath(rootFlag)
	}
	if logF, err := os.OpenFile(logFlag, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
		// stderr is already redirected to this same file by the client when ashd
		// is auto-started, so writing to both produces duplicate lines.
		log.SetOutput(logF)
	}
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	cfg, cfgSource, err := config.Load(rootFlag)
	if err != nil {
		log.Fatalf("ashd: config: %v", err)
	}
	jail.SetPolicy(jail.FromConfig(cfg.Jail.Enabled, rootFlag, cfg.Jail.AllowPaths, cfg.Jail.DenyPaths))
	if err := git.SetBackend(cfg.Git.Backend); err != nil {
		log.Fatalf("ashd: git backend: %v", err)
	}

	led, err := ledger.Open(session.LedgerPath(rootFlag), rootFlag, "ashd/v0.1")
	if err != nil {
		log.Fatalf("ashd: ledger: %v", err)
	}
	defer led.Close()

	cleanCfg := ledger.CleanupCfg{
		MaxAge:  cfg.Ledger.MaxAge.AsDuration(),
		MaxRows: cfg.Ledger.MaxRows,
		Vacuum:  cfg.Ledger.Vacuum,
	}
	if cr, err := led.Cleanup(cleanCfg); err != nil {
		log.Printf("ashd: ledger cleanup: %v", err)
	} else if cr.DeletedCalls > 0 || cr.DeletedSessions > 0 {
		log.Printf("ashd: ledger cleanup: deleted %d calls, %d sessions", cr.DeletedCalls, cr.DeletedSessions)
	}

	runners := verbs.Runners(led, cfg, daemonStart, rootFlag)
	pretty := verbs.PrettyHandlers()

	_ = os.Remove(sockFlag)
	listener, err := net.Listen("unix", sockFlag)
	if err != nil {
		log.Fatalf("ashd: listen %s: %v", sockFlag, err)
	}
	defer listener.Close()
	if err := os.Chmod(sockFlag, 0o600); err != nil {
		log.Printf("ashd: chmod socket: %v", err)
	}

	// Concurrency cap: 0 means unlimited (preserves the pre-ASH-49
	// behavior). When set, the accept loop blocks before spawning a
	// handler goroutine once the cap is in flight, so backpressure
	// manifests as queued connections at the OS layer rather than
	// runaway goroutine growth.
	var sem chan struct{}
	if n := cfg.Daemon.MaxConcurrentHandlers; n > 0 {
		sem = make(chan struct{}, n)
	}
	readDeadline := cfg.Daemon.ReadDeadline.AsDuration()
	shutdownGrace := cfg.Daemon.ShutdownGrace.AsDuration()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Println("ashd: shutting down")
		listener.Close()
	}()

	pidPath := session.PIDPath(rootFlag)
	if err := os.WriteFile(pidPath, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o644); err != nil {
		log.Printf("ashd: pid file: %v", err)
	} else {
		defer os.Remove(pidPath)
	}

	log.Printf("ashd ready: root=%s socket=%s session=%s config=%s", rootFlag, sockFlag, led.SessionID(), cfgSource)

	var wg sync.WaitGroup
	acceptLoop(listener, sem, &wg, func(conn net.Conn) {
		handle(conn, led, runners, pretty, readDeadline)
	})

	// Listener closed (signal or fatal accept error). Wait bounded for
	// in-flight handlers so their ledger rows commit before exit. If
	// grace expires we log loudly (losing rows is the project foundational
	// claim) but we still exit so tests / supervisors can move on.
	if !drainHandlers(&wg, shutdownGrace) {
		log.Printf("ashd: shutdown grace %v exceeded; in-flight handlers abandoned", shutdownGrace)
	} else {
		log.Println("ashd: clean shutdown")
	}
}

// acceptLoop accepts connections from ln and dispatches each through
// handler in a goroutine. Tracks in-flight handlers via wg so the
// caller can drain them on shutdown. When sem is non-nil, the loop
// blocks on it before spawning a handler  this is the ASH-49
// concurrency cap.
//
// Returns when ln.Accept() errors, which happens when the listener is
// closed (the canonical shutdown path) or for unrecoverable network
// errors.
func acceptLoop(ln net.Listener, sem chan struct{}, wg *sync.WaitGroup, handler func(net.Conn)) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		// Acquire sem BEFORE incrementing wg so the cap is enforced
		// even if many connections arrive in a burst. The accept itself
		// queues at the kernel level (UDS listen backlog) while we wait
		// for a slot.
		if sem != nil {
			sem <- struct{}{}
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if sem != nil {
				defer func() { <-sem }()
			}
			handler(conn)
		}()
	}
}

// drainHandlers waits up to grace for wg to reach zero. Returns true
// on clean drain, false if grace was exceeded.
func drainHandlers(wg *sync.WaitGroup, grace time.Duration) bool {
	if grace <= 0 {
		// No grace configured: return immediately. wg may still have
		// outstanding handlers; the caller has already accepted the risk.
		return false
	}
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(grace):
		return false
	}
}

func handle(conn net.Conn, led *ledger.Ledger, runners map[string]verbs.Runner, pretty map[string]verbs.Pretty, readDeadline time.Duration) {
	defer conn.Close()
	for {
		parseStart := time.Now()
		// Per-frame read deadline (ASH-49). When > 0, a connection that
		// goes silent between frames is timed out and the goroutine
		// returns instead of pinning forever. The deadline is reset on
		// every successful ReadFrame, so a long-lived client making
		// regular requests is unaffected.
		if readDeadline > 0 {
			_ = conn.SetReadDeadline(time.Now().Add(readDeadline))
		}
		reqBuf, err := proto.ReadFrame(conn)
		if err != nil {
			// Idle-timeout on the deadline is the expected close-path for
			// abandoned connections; do not log it as an error.
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				return
			}
			return
		}
		req, err := proto.DecodeRequest(reqBuf)
		if err != nil {
			writeErrFrame(conn, 0, "decode", err.Error())
			return
		}
		parseUs := time.Since(parseStart).Microseconds()

		rsp := &proto.Response{V: proto.ProtocolVersion, ID: req.ID}

		execStart := time.Now()
		tracer := &proto.Tracer{}
		runner, ok := runners[req.Verb]
		var dispatchUs int64
		// typedData holds the verb's in-process result for the truncation
		// check and pretty rendering. The wire response carries the
		// already-encoded RawMessage in rsp.Data.
		var typedData any

		// Streaming surface (ASH-106). The Stream flag opts the request
		// into kind-tagged Chunk frames + a Final frame; legacy requests
		// (Stream==false) keep today's single-frame response shape with
		// no kind tag, so v1 clients are unaffected.
		streaming := ok && req.Stream
		var writeMu sync.Mutex
		var emitter *frameEmitter
		var ctxCancel context.CancelFunc
		var watcherDone chan struct{}
		var verbDone chan struct{}
		if streaming {
			var sctx context.Context
			sctx, ctxCancel = context.WithCancel(context.Background())
			tracer.SetContext(sctx)
			emitter = newFrameEmitter(conn, &writeMu, req.ID, execStart)
			tracer.SetEmitter(emitter)
			watcherDone = make(chan struct{})
			verbDone = make(chan struct{})
			// Watcher: blocks on a single inbound kinded frame. Any frame
			// (Cancel) OR EOF before the verb signals done cancels ctx.
			// After the verb finishes we SetReadDeadline to time.Now() to
			// unblock the pending Read; the watcher then sees verbDone and
			// exits without cancelling.
			go func() {
				defer close(watcherDone)
				_, _, rerr := proto.ReadKinded(conn)
				select {
				case <-verbDone:
					return
				default:
				}
				if rerr != nil {
					// EOF / read error mid-stream = client cancellation.
					ctxCancel()
					return
				}
				// Any kinded frame from the client during a stream means
				// cancel; only KindCancel is formally defined, but anything
				// else is unexpected and we err on the side of stopping.
				ctxCancel()
			}()
		}

		if !ok {
			rsp.OK = false
			rsp.Err = &proto.Error{Code: "unknown_verb", Msg: "unknown verb: " + req.Verb}
		} else {
			dispatchUs = time.Since(execStart).Microseconds()
			data, perr := runner.Run(req.Args, tracer)
			if perr != nil {
				rsp.OK = false
				rsp.Err = perr
			} else {
				rsp.OK = true
				typedData = data
				rsp.Data = proto.MustData(data)
			}
		}
		execUs := time.Since(execStart).Microseconds()
		phases := tracer.Snapshot()

		// Streaming cleanup: stop the watcher (no-op if it already exited
		// because the client closed), flush any trailing chunks, and if
		// the request was cancelled mid-stream overwrite the response with
		// a cancelled error. The chunks already on the wire convey what
		// the verb produced before the abort.
		if streaming {
			close(verbDone)
			// Unblock the watcher's pending ReadKinded so we can join.
			_ = conn.SetReadDeadline(time.Now())
			<-watcherDone
			_ = conn.SetReadDeadline(time.Time{})
			if err := emitter.Flush(); err != nil {
				log.Printf("ashd: stream flush: %v", err)
			}
			if tracer.Context().Err() != nil {
				rsp.OK = false
				rsp.Data = nil
				rsp.Err = &proto.Error{Code: "cancelled", Msg: "request cancelled"}
				typedData = nil
			}
			ctxCancel()
		}

		// Pretty-rendered forms drive token counting. Both daemon and client
		// produce the same canonical text, so tokens_out reflects what the
		// agent will actually pay for. Use the literal client argv when
		// available (honest input-side cost) and fall back to the
		// reconstructed canonical for older clients without Argv.
		var prettyReq string
		if len(req.Argv) > 0 {
			prettyReq = proto.PrettyRequestArgv(req.Argv)
		} else {
			prettyReq = proto.PrettyRequest(req)
		}
		prettyRsp := proto.PrettyResponseHeader(rsp)
		if p, ok := pretty[req.Verb]; ok {
			prettyRsp = p(req, rsp)
		}
		tokensIn := led.Counter().Count(prettyReq)
		tokensOut := led.Counter().Count(prettyRsp)
		// ASH-71: measure the path-prefix tax by retokenizing the
		// pretty response with known prefixes stripped. Cheap (in-
		// memory tiktoken) and keeps the rendered output untouched.
		tokensOutNoPrefix := led.Counter().Count(
			ledger.StripPrefixes(prettyRsp, jail.PathPrefixes()),
		)

		// BytesOut and LatencySerializeUs cannot be known until after the wire
		// encode (circular dependency: both values are in the metrics envelope
		// that is itself encoded). We insert placeholder zeros here, then patch
		// the ledger row with accurate values via UpdateSerializeStats once the
		// encode is done. The record-before-encode ordering is preserved so a
		// ledger failure is still visible in rsp.Metrics.LedgerError.
		metrics := &proto.Metrics{
			LatencyParseUs:    parseUs,
			LatencyExecUs:     execUs,
			LatencyDispatchUs: dispatchUs,
			TokensIn:          tokensIn,
			TokensOut:         tokensOut,
			BytesIn:           len(reqBuf),
			Truncated:         truncatedFromTyped(rsp, runners[req.Verb], typedData),
		}
		// Phases is attached only when the verb actually instrumented
		// something. Verbs that don't (help, metrics) leave it nil so the
		// field omits from the wire entirely.
		if !phases.IsZero() {
			ph := phases
			metrics.Phases = &ph
		}

		errCode, errMsg := "", ""
		if rsp.Err != nil {
			errCode = rsp.Err.Code
			errMsg = rsp.Err.Msg
		}
		var chunksOut int
		var ttfcUs int64
		if streaming {
			chunksOut = emitter.ChunkCount()
			ttfcUs = emitter.FirstChunkLatency().Microseconds()
		}
		rowID, recordErr := led.Record(&ledger.Call{
			RequestID:          req.ID,
			Timestamp:          time.Now(),
			Verb:               req.Verb,
			ArgsMsgpack:        argsBlob(reqBuf),
			OK:                 rsp.OK,
			ErrCode:            errCode,
			ErrMsg:             errMsg,
			LatencyParseUs:     parseUs,
			LatencyExecUs:      execUs,
			LatencySerializeUs: 0,
			TokensIn:           tokensIn,
			TokensOut:          tokensOut,
			TokensOutNoPrefix:  tokensOutNoPrefix,
			TokensMethod:       ledger.TokensMethod,
			BytesIn:            len(reqBuf),
			BytesOut:           0,
			Truncated:          metrics.Truncated,
			WalkUs:             phases.WalkUs,
			IOUs:               phases.IOUs,
			RegexUs:            phases.RegexUs,
			RegexCompileUs:     phases.RegexCompileUs,
			LatencyDispatchUs:  dispatchUs,
			Streaming:          streaming,
			ChunksOut:          chunksOut,
			TimeToFirstChunkUs: ttfcUs,
		})
		if recordErr != nil {
			log.Printf("ashd: ledger record: %v", recordErr)
			metrics.LedgerError = recordErr.Error()
		}

		rsp.Metrics = metrics
		serStart := time.Now()
		final, err := proto.EncodeResponse(rsp)
		if err != nil {
			log.Printf("ashd: encode: %v", err)
			return
		}
		serUs := time.Since(serStart).Microseconds()

		if recordErr == nil {
			if err := led.UpdateSerializeStats(rowID, len(final), serUs); err != nil {
				log.Printf("ashd: ledger update serialize: %v", err)
			}
		}

		// Streaming runs write Final under the kind-tag scheme so the
		// client knows the stream is over; legacy runs write a plain
		// frame, byte-identical to v1.
		var werr error
		if streaming {
			writeMu.Lock()
			werr = proto.WriteKinded(conn, proto.KindFinal, final)
			writeMu.Unlock()
		} else {
			werr = proto.WriteFrame(conn, final)
		}
		if werr != nil {
			log.Printf("ashd: write: %v", werr)
			return
		}
	}
}

func truncatedFromTyped(rsp *proto.Response, runner verbs.Runner, typedData any) bool {
	if !rsp.OK || typedData == nil || runner.Truncated == nil {
		return false
	}
	return runner.Truncated(typedData)
}

// argsMaxStrBytes is the per-value cap for string args stored in the ledger.
// Values longer than this are replaced with a "<truncated:N>" sentinel so that
// large --content payloads (files, base64 blobs) do not inflate ledger.db.
const argsMaxStrBytes = 1024

// argsBlob encodes just the sanitized Args map from the request into msgpack
// for ledger storage. Argv is dropped (it duplicates Args and can be large).
// String values longer than argsMaxStrBytes are replaced with "<truncated:N>".
// Returns nil when the request has no args or cannot be decoded.
func argsBlob(reqBuf []byte) []byte {
	req, err := proto.DecodeRequest(reqBuf)
	if err != nil || len(req.Args) == 0 {
		return nil
	}
	sanitized := make(map[string]any, len(req.Args))
	for k, v := range req.Args {
		if s, ok := v.(string); ok && len(s) > argsMaxStrBytes {
			sanitized[k] = fmt.Sprintf("<truncated:%d>", len(s))
		} else {
			sanitized[k] = v
		}
	}
	b, err := msgpack.Marshal(sanitized)
	if err != nil {
		return nil
	}
	return b
}

func writeErrFrame(conn net.Conn, id uint64, code, msg string) {
	rsp := &proto.Response{V: proto.ProtocolVersion, ID: id, OK: false, Err: &proto.Error{Code: code, Msg: msg}}
	out, err := proto.EncodeResponse(rsp)
	if err != nil {
		return
	}
	_ = proto.WriteFrame(conn, out)
}
