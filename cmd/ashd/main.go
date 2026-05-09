// Command ashd is the ash daemon. It listens on a per-project Unix domain
// socket, dispatches verb requests, records every call to the SQLite ledger,
// and replies with the structured response envelope.
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/stazelabs/ash/internal/ledger"
	"github.com/stazelabs/ash/internal/proto"
	"github.com/stazelabs/ash/internal/session"
	"github.com/stazelabs/ash/internal/verbs"
	"github.com/vmihailenco/msgpack/v5"
)

func main() {
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

	led, err := ledger.Open(session.LedgerPath(rootFlag), rootFlag, "ashd/v0.1")
	if err != nil {
		log.Fatalf("ashd: ledger: %v", err)
	}
	defer led.Close()

	runners := verbs.Runners(led)
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

	log.Printf("ashd ready: root=%s socket=%s session=%s", rootFlag, sockFlag, led.SessionID())

	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		go handle(conn, led, runners, pretty)
	}
}

func handle(conn net.Conn, led *ledger.Ledger, runners map[string]verbs.Runner, pretty map[string]verbs.Pretty) {
	defer conn.Close()
	for {
		parseStart := time.Now()
		reqBuf, err := proto.ReadFrame(conn)
		if err != nil {
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

		// BytesOut and LatencySerializeUs cannot be known until after the wire
		// encode (circular dependency: both values are in the metrics envelope
		// that is itself encoded). We insert placeholder zeros here, then patch
		// the ledger row with accurate values via UpdateSerializeStats once the
		// encode is done. The record-before-encode ordering is preserved so a
		// ledger failure is still visible in rsp.Metrics.LedgerError.
		metrics := &proto.Metrics{
			LatencyParseUs:     parseUs,
			LatencyExecUs:      execUs,
			LatencySerializeUs: 0,
			LatencyDispatchUs:  dispatchUs,
			TokensIn:           tokensIn,
			TokensOut:          tokensOut,
			TokensMethod:       ledger.TokensMethod,
			BytesIn:            len(reqBuf),
			BytesOut:           0,
			Truncated:          truncatedFromTyped(rsp, runners[req.Verb], typedData),
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
			TokensMethod:       ledger.TokensMethod,
			BytesIn:            len(reqBuf),
			BytesOut:           0,
			Truncated:          metrics.Truncated,
			WalkUs:             phases.WalkUs,
			IOUs:               phases.IOUs,
			RegexUs:            phases.RegexUs,
			RegexCompileUs:     phases.RegexCompileUs,
			LatencyDispatchUs:  dispatchUs,
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

		if err := proto.WriteFrame(conn, final); err != nil {
			log.Printf("ashd: write: %v", err)
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
