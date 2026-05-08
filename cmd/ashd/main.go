// Command ashd is the ash daemon. It listens on a per-project Unix domain
// socket, dispatches verb requests, records every call to the SQLite ledger,
// and replies with the structured response envelope.
package main

import (
	"flag"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/stazelabs/ash/internal/ledger"
	"github.com/stazelabs/ash/internal/proto"
	"github.com/stazelabs/ash/internal/session"
	"github.com/stazelabs/ash/internal/verbs/find"
	"github.com/stazelabs/ash/internal/verbs/grep"
	"github.com/stazelabs/ash/internal/verbs/help"
	"github.com/stazelabs/ash/internal/verbs/metrics"
	"github.com/stazelabs/ash/internal/verbs/read"
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

	log.Printf("ashd ready: root=%s socket=%s session=%s", rootFlag, sockFlag, led.SessionID())

	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		go handle(conn, led)
	}
}

func handle(conn net.Conn, led *ledger.Ledger) {
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
		switch req.Verb {
		case "read":
			args, perr := read.ParseArgs(req.Args)
			if perr != nil {
				rsp.OK = false
				rsp.Err = perr
			} else {
				result, perr := read.Run(args)
				if perr != nil {
					rsp.OK = false
					rsp.Err = perr
				} else {
					rsp.OK = true
					rsp.Data = result
				}
			}
		case "find":
			args, perr := find.ParseArgs(req.Args)
			if perr != nil {
				rsp.OK = false
				rsp.Err = perr
			} else {
				result, perr := find.Run(args)
				if perr != nil {
					rsp.OK = false
					rsp.Err = perr
				} else {
					rsp.OK = true
					rsp.Data = result
				}
			}
		case "grep":
			args, perr := grep.ParseArgs(req.Args)
			if perr != nil {
				rsp.OK = false
				rsp.Err = perr
			} else {
				result, perr := grep.Run(args)
				if perr != nil {
					rsp.OK = false
					rsp.Err = perr
				} else {
					rsp.OK = true
					rsp.Data = result
				}
			}
		case "metrics":
			margs, perr := metrics.ParseArgs(req.Args)
			if perr != nil {
				rsp.OK = false
				rsp.Err = perr
			} else {
				calls, qerr := led.QueryRecent(margs.Last, margs.Verb)
				if qerr != nil {
					rsp.OK = false
					rsp.Err = &proto.Error{Code: "ledger", Msg: qerr.Error()}
				} else {
					rsp.OK = true
					rsp.Data = metrics.ResultFromCalls(calls)
				}
			}
		case "help":
			hargs, perr := help.ParseArgs(req.Args)
			if perr != nil {
				rsp.OK = false
				rsp.Err = perr
			} else {
				result, perr := help.Run(hargs)
				if perr != nil {
					rsp.OK = false
					rsp.Err = perr
				} else {
					rsp.OK = true
					rsp.Data = result
				}
			}
		default:
			rsp.OK = false
			rsp.Err = &proto.Error{Code: "unknown_verb", Msg: "unknown verb: " + req.Verb}
		}
		execUs := time.Since(execStart).Microseconds()

		// Pretty-rendered forms drive token counting. Both daemon and client
		// produce the same canonical text, so tokens_out reflects what the
		// agent will actually pay for.
		prettyReq := proto.PrettyRequest(req)
		var prettyRsp string
		switch req.Verb {
		case "read":
			prettyRsp = read.PrettyResponse(req, rsp)
		case "find":
			prettyRsp = find.PrettyResponse(req, rsp)
		case "grep":
			prettyRsp = grep.PrettyResponse(req, rsp)
		case "metrics":
			prettyRsp = metrics.PrettyResponse(req, rsp)
		case "help":
			prettyRsp = help.PrettyResponse(req, rsp)
		default:
			prettyRsp = proto.PrettyResponseHeader(rsp)
		}
		tokensIn := led.Counter().Count(prettyReq)
		tokensOut := led.Counter().Count(prettyRsp)

		// Encode once to learn bytes_out for the wire metrics, then build the
		// metrics, record to the ledger, then re-encode for the wire. The
		// record-before-send ordering means a ledger-write failure becomes a
		// signal the client sees in rsp.Metrics.LedgerError.
		serStart := time.Now()
		first, err := proto.EncodeResponse(rsp)
		if err != nil {
			log.Printf("ashd: encode: %v", err)
			return
		}
		serFirstUs := time.Since(serStart).Microseconds()
		bytesOut := len(first)

		// LatencySerializeUs is an estimate: the second encode hasn't happened
		// yet, but it costs roughly the same as the first. Close enough; the
		// alternative is encoding three times.
		metrics := &proto.Metrics{
			LatencyParseUs:     parseUs,
			LatencyExecUs:      execUs,
			LatencySerializeUs: serFirstUs * 2,
			TokensIn:           tokensIn,
			TokensOut:          tokensOut,
			TokensMethod:       ledger.TokensMethod,
			BytesIn:            len(reqBuf),
			BytesOut:           bytesOut,
			Truncated:          truncatedFromResult(rsp),
		}

		errCode, errMsg := "", ""
		if rsp.Err != nil {
			errCode = rsp.Err.Code
			errMsg = rsp.Err.Msg
		}
		recordErr := led.Record(&ledger.Call{
			RequestID:          req.ID,
			Timestamp:          time.Now(),
			Verb:               req.Verb,
			ArgsMsgpack:        argsBlob(reqBuf),
			OK:                 rsp.OK,
			ErrCode:            errCode,
			ErrMsg:             errMsg,
			LatencyParseUs:     parseUs,
			LatencyExecUs:      execUs,
			LatencySerializeUs: metrics.LatencySerializeUs,
			TokensIn:           tokensIn,
			TokensOut:          tokensOut,
			TokensMethod:       ledger.TokensMethod,
			BytesIn:            len(reqBuf),
			BytesOut:           bytesOut,
			Truncated:          metrics.Truncated,
		})
		if recordErr != nil {
			log.Printf("ashd: ledger record: %v", recordErr)
			metrics.LedgerError = recordErr.Error()
		}

		rsp.Metrics = metrics
		final, err := proto.EncodeResponse(rsp)
		if err != nil {
			log.Printf("ashd: encode (final): %v", err)
			return
		}

		if err := proto.WriteFrame(conn, final); err != nil {
			log.Printf("ashd: write: %v", err)
			return
		}
	}
}

func truncatedFromResult(rsp *proto.Response) bool {
	if !rsp.OK || rsp.Data == nil {
		return false
	}
	if r, ok := rsp.Data.(*read.Result); ok {
		return r.Truncated
	}
	if r, ok := rsp.Data.(*find.Result); ok {
		return r.Truncated
	}
	if r, ok := rsp.Data.(*grep.Result); ok {
		return r.Truncated
	}
	return false
}

// argsBlob persists the raw msgpack bytes of the request so the ledger can
// be re-decoded later for analysis without parsing or re-encoding.
func argsBlob(reqBuf []byte) []byte {
	out := make([]byte, len(reqBuf))
	copy(out, reqBuf)
	return out
}

func writeErrFrame(conn net.Conn, id uint64, code, msg string) {
	rsp := &proto.Response{V: proto.ProtocolVersion, ID: id, OK: false, Err: &proto.Error{Code: code, Msg: msg}}
	out, err := proto.EncodeResponse(rsp)
	if err != nil {
		return
	}
	_ = proto.WriteFrame(conn, out)
}
