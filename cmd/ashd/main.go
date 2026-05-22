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
	"github.com/stazelabs/ash/internal/lsp"
	"github.com/stazelabs/ash/internal/lsp/cache"
	"github.com/stazelabs/ash/internal/proto"
	"github.com/stazelabs/ash/internal/session"
	"github.com/stazelabs/ash/internal/verbs"
	"github.com/stazelabs/ash/internal/verbs/git"
	"github.com/stazelabs/ash/internal/verbs/stop"
	"github.com/vmihailenco/msgpack/v5"
)

// applyEnforcementConfig (re-)applies the slice of cfg that is safe to
// swap mid-session without recycling subprocesses or rewiring listeners.
// Today: jail policy only. Called once at daemon startup and again from
// the per-request refresh closure (ASH-164) when ash.toml mtime/size
// changes. Subprocess-lifecycle config (LSP broker, git backend) and
// one-shot startup config (ledger cleanup, daemon concurrency cap)
// stay restart-required — see CLAUDE.md gotcha #1.
//
// jail.SetPolicy swaps a package-global policy pointer under an RWMutex
// and is goroutine-safe; concurrent handler goroutines see either the
// old or the new policy for any given verb call, matching the "next
// request sees new config" semantic the hot-reload contract promises.
func applyEnforcementConfig(rootFlag string, cfg *config.Config) {
	jail.SetPolicy(jail.FromConfig(cfg.Jail.Enabled, rootFlag, cfg.Jail.AllowPaths, cfg.Jail.DenyPaths))
}

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

	cfgWatcher, cfg, cfgSource, err := config.NewWatcher(rootFlag)
	if err != nil {
		log.Fatalf("ashd: config: %v", err)
	}
	applyEnforcementConfig(rootFlag, cfg)
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

	// ASH-136: LSP broker for the daemon. Disabled by default; when
	// [lsp].enabled=true in ash.toml, the broker spawns gopls lazily on
	// the first write/edit and writes a synthetic verb="lsp.init" row
	// to the ledger so init latency is queryable separately from
	// per-call latency (`ash report --verb lsp.init`).
	broker := lsp.New(
		lsp.Config{Enabled: cfg.LSP.Enabled, GoplsPath: cfg.LSP.GoplsPath, Root: rootFlag},
		lsp.WithInitCallback(func(d time.Duration, initErr error) {
			recordLSPInit(led, d, initErr)
		}),
	)
	defer broker.Close()

	// ASH-137: lang-cache opens alongside the broker. It is daemon-owned
	// so ash lang verbs (ASH-138+) can share one handle. We open it
	// whenever the broker is enabled even though no caller exists yet —
	// the write/edit invalidation hook still wants somewhere to send
	// its DELETE calls. A failed open is non-fatal: log and continue
	// uncached, since the broker itself can still answer requests.
	var langCache *cache.Cache
	if cfg.LSP.Enabled {
		var cerr error
		langCache, cerr = cache.Open(cache.Options{
			Path: session.LangCachePath(rootFlag),
			TTL:  cfg.LSP.CacheTTL.AsDuration(),
		})
		if cerr != nil {
			log.Printf("ashd: lang-cache open: %v (continuing uncached)", cerr)
		}
	}
	defer langCache.Close()

	if cfg.LSP.Enabled {
		// Compose broker.Notify + cache.Invalidate into the single sink
		// fired by write/edit. The broker call is async (NotifyBroker
		// spawns a goroutine) so the inline cache.Invalidate completes
		// in microseconds and write/edit latency stays unaffected.
		notifyBroker := lsp.NotifyBroker(broker)
		lsp.SetSink(func(path string) {
			notifyBroker(path)
			if langCache != nil {
				if _, err := langCache.Invalidate(path); err != nil {
					log.Printf("ashd: lang-cache invalidate %s: %v", path, err)
				}
				// ASH-157: bump the workspace watermark so workspace-
				// scoped lang cache rows (def/refs/callers/impl) are
				// invalidated by the next Go-relevant write. BumpWorkspace
				// is itself gated on .go/.mod/.sum suffixes — non-Go
				// writes do not move the watermark, so an agent editing
				// a README between two lang refs calls still hits.
				langCache.BumpWorkspace(path)
			}
		})
	}

	runners := verbs.Runners(led, cfg, daemonStart, rootFlag, broker, langCache)
	pretty := verbs.PrettyHandlers()

	// ASH-154: refuse to double-bind. The client-side sweep should
	// normally have cleared the socket before spawning us, but if
	// another ashd is still alive for this socket (a racing client,
	// or a sweep that missed an orphan) we exit non-zero with a
	// clear error rather than racing the survivor for the bind. The
	// FindAshdPIDs scan excludes our own PID, so a fresh startup
	// against a free socket passes through.
	if err := checkNoOtherAshd(sockFlag); err != nil {
		log.Fatalf("ashd: %v", err)
	}
	_ = os.Remove(sockFlag)
	// Create the socket 0600 from the start: net.Listen otherwise
	// applies the process umask, leaving a brief window where the
	// socket is world-connectable before the Chmod below. Umask is
	// process-global but daemon startup here is single-threaded.
	oldMask := syscall.Umask(0o177)
	listener, err := net.Listen("unix", sockFlag)
	syscall.Umask(oldMask)
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
		// Close the LSP broker BEFORE the listener so a slow gopls
		// shutdown does not extend the drain window for verb handlers
		// — Close has its own 2s grace and ashd's shutdown_grace
		// (default 5s) follows it. The lang-cache close is
		// near-instantaneous (just a SQLite handle drop).
		lsp.SetSink(nil)
		broker.Close()
		langCache.Close()
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
	// ASH-164: per-frame hot-reload check on ash.toml. Bounded to
	// enforcement-layer config (jail) — see applyEnforcementConfig and
	// CLAUDE.md gotcha #1 for what stays restart-required (subprocess
	// lifecycle: LSP, git backend, daemon caps, ledger cleanup).
	refresh := func() {
		_, _, changed, rerr := cfgWatcher.Refresh()
		if rerr != nil {
			log.Printf("ashd: config refresh: %v (keeping previous config)", rerr)
			return
		}
		if changed {
			applyEnforcementConfig(rootFlag, cfgWatcher.Config())
			log.Printf("ashd: config reloaded — jail policy refreshed")
		}
	}
	acceptLoop(listener, sem, &wg, func(conn net.Conn) {
		handle(conn, led, runners, pretty, readDeadline, refresh)
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

func handle(conn net.Conn, led *ledger.Ledger, runners map[string]verbs.Runner, pretty map[string]verbs.Pretty, readDeadline time.Duration, refresh func()) {
	defer conn.Close()
	for {
		parseStart := time.Now()
		// ASH-164: hot-reload check for enforcement-layer config (jail).
		// Microseconds when nothing changed; rebuilds jail policy when
		// ash.toml mtime/size changes. nil-tolerant so tests that
		// construct handle() inline can pass nil.
		if refresh != nil {
			refresh()
		}
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
		// ASH-132: forward the client's shell env to verbs that shell
		// out (today: only `test`). Nil-safe; legacy clients leave
		// req.Env empty and the subprocess falls back to os.Environ().
		tracer.SetEnv(req.Env)
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
			ArgsMsgpack:        argsBlob(req),
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
			// ASH-108 cache-aware envelope: the schema reserves
			// tokens_cache_hit / tokens_cache_miss for harness-reported
			// Anthropic prompt-cache accounting. Daemon-originated rows
			// leave both at zero — no current wire path populates them.
			// See docs/cache-shape.md.
		})
		if recordErr != nil {
			log.Printf("ashd: ledger record: %v", recordErr)
			metrics.LedgerError = recordErr.Error()
		}

		// ASH-110 session-graph linking. Best-effort: a failed link
		// insert does not propagate back to the client (the verb
		// itself succeeded). The link write is sub-millisecond on
		// typical sessions because Link bounds the lookback to the
		// last 16 calls within a 5-minute window.
		if recordErr == nil && rowID > 0 {
			if err := led.Link(rowID, req.Args); err != nil {
				log.Printf("ashd: ledger link: %v", err)
			}
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
			// ASH-123 / ASH-156: MCP-transport emit accounting. For
			// requests that arrived via ashmcp, the bytes the harness
			// actually consumes vary by emit shape:
			//
			//   - pretty mode (ASH-146): TextContent carries the
			//     daemon-pretty render — reuse prettyRsp (already
			//     tokenized above for tokens_out).
			//   - error envelope: TextContent carries "<code>: <msg>"
			//     from proto.MCPEnvelope; tokenize that.
			//   - json-mode success (post-ASH-156): no TextContent —
			//     the verb Result rides as StructuredContent only.
			//     emitBody stays empty; only the truncation sentinel
			//     (when present) costs tokens.
			//
			// We mirror exactly the one mutation ashmcp performs on
			// rsp before emit — rsp.Metrics.BytesOut = len(frame_payload)
			// — so the two byte counts agree by construction.
			// LatencySerializeUs and other post-encode stats stay 0
			// in both places: ashmcp never sees serUs, so neither do
			// we when modeling its emit.
			if req.Transport == proto.TransportMCP {
				if metrics != nil {
					metrics.BytesOut = len(final)
				}
				emitBody := ""
				emitErr := error(nil)
				switch {
				case req.EmitFormat == "pretty":
					emitBody = prettyRsp
				case !rsp.OK:
					env, eerr := proto.MCPEnvelope(rsp)
					if eerr == nil {
						emitBody = string(env)
					} else {
						emitErr = eerr
					}
				}
				// json-mode success leaves emitBody empty: ashmcp
				// emits no TextContent block for that case (ASH-156).
				if emitErr == nil {
					emitBytes := len(emitBody)
					emitTokens := led.Counter().Count(emitBody)
					// ashmcp prepends a sentinel TextContent block
					// when the response was truncated (ASH-127);
					// mirror its byte/token cost here so
					// tokens_out_emit still equals what the harness
					// actually consumed.
					if sentinel := proto.MCPTruncationSentinel(rsp); sentinel != "" {
						emitBytes += len(sentinel)
						emitTokens += led.Counter().Count(sentinel)
					}
					if err := led.UpdateMCPEmit(rowID, emitBytes, emitTokens); err != nil {
						log.Printf("ashd: ledger update mcp emit: %v", err)
					}
				} else {
					log.Printf("ashd: mcp envelope: %v", emitErr)
				}
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
// Env is intentionally excluded — secrets in req.Env (forwarded to test
// subprocesses, ASH-132) must not leak into args_msgpack.
// Returns nil when the request has no args.
func argsBlob(req *proto.Request) []byte {
	if req == nil || len(req.Args) == 0 {
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

// findAshdSocketPIDs is the test seam for checkNoOtherAshd. It defaults
// to stop.FindAshdPIDs and is overridden by tests that need to simulate
// a contested socket without forging argv in the live process table.
var findAshdSocketPIDs = stop.FindAshdPIDs

// checkNoOtherAshd returns a non-nil error when another ashd process is
// already bound to sockPath. ASH-154 — closes the narrow window between
// the client-side socket unlink in killStaleIfNeeded and the daemon's
// own net.Listen where two ashd processes could otherwise both decide
// they own the socket. The check is best-effort: a process that races
// past it before our own bind would still produce a dual-listener
// state, but the client sweep is the primary line of defense and the
// remaining race is dominated by `ps`-scan latency.
func checkNoOtherAshd(sockPath string) error {
	pids := findAshdSocketPIDs(sockPath)
	if len(pids) == 0 {
		return nil
	}
	return fmt.Errorf("another ashd is bound to this socket (pid=%v); refusing to double-bind — run `ash stop` to clean up", pids)
}

// recordLSPInit writes a synthetic ledger row for an LSP broker
// initialization (success or failure). The verb name "lsp.init" is
// outside the verb registry on purpose: it stays separate from per-call
// latency so `ash report --verb lsp.init` answers "how long did gopls
// take to come up?" without polluting verb p50/p95 aggregates.
func recordLSPInit(led *ledger.Ledger, d time.Duration, initErr error) {
	if led == nil {
		return
	}
	ok := initErr == nil
	errCode, errMsg := "", ""
	if !ok {
		errCode = "lsp_init"
		errMsg = initErr.Error()
		if lerr := (*lsp.Error)(nil); errors.As(initErr, &lerr) && lerr.Code != "" {
			errCode = lerr.Code
		}
	}
	if _, err := led.Record(&ledger.Call{
		RequestID:     0,
		Timestamp:     time.Now(),
		Verb:          "lsp.init",
		OK:            ok,
		ErrCode:       errCode,
		ErrMsg:        errMsg,
		LatencyExecUs: d.Microseconds(),
		TokensMethod:  ledger.TokensMethod,
	}); err != nil {
		log.Printf("ashd: ledger lsp.init record: %v", err)
	}
}

func writeErrFrame(conn net.Conn, id uint64, code, msg string) {
	rsp := &proto.Response{V: proto.ProtocolVersion, ID: id, OK: false, Err: &proto.Error{Code: code, Msg: msg}}
	out, err := proto.EncodeResponse(rsp)
	if err != nil {
		return
	}
	_ = proto.WriteFrame(conn, out)
}
