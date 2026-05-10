// Package metrics implements the `metrics` verb.
//
// Args:
//
//	last   int     (optional) - number of recent calls to return, default 20, max 200
//	verb   string  (optional) - filter to calls for a specific verb
//
// The daemon executes the ledger query itself (it holds the open *ledger.Ledger)
// and passes the result to ResultFromCalls before storing it in rsp.Data.
package metrics

import (
	"fmt"
	"strings"
	"time"

	"github.com/stazelabs/ash/internal/ledger"
	"github.com/stazelabs/ash/internal/proto"
	"github.com/stazelabs/ash/internal/verbs/argutil"
)

const (
	DefaultLast = 20
	MaxLast     = 200
)

type Args struct {
	Last int
	Verb string
}

type Row struct {
	Timestamp     int64  `msgpack:"ts"`
	Verb          string `msgpack:"verb"`
	OK            bool   `msgpack:"ok"`
	ErrCode       string `msgpack:"err_code,omitempty"`
	TokensIn      int    `msgpack:"tokens_in"`
	TokensOut     int    `msgpack:"tokens_out"`
	LatencyExecUs int64  `msgpack:"latency_exec_us"`
	BytesIn       int    `msgpack:"bytes_in"`
	BytesOut      int    `msgpack:"bytes_out"`
	Truncated     bool   `msgpack:"truncated,omitempty"`
	WalkUs            int64  `msgpack:"walk_us,omitempty"`
	IOUs              int64  `msgpack:"io_us,omitempty"`
	RegexUs           int64  `msgpack:"regex_us,omitempty"`
	RegexCompileUs    int64  `msgpack:"regex_compile_us,omitempty"`
	LatencyDispatchUs int64  `msgpack:"latency_dispatch_us,omitempty"`
}

type Result struct {
	Rows  []Row `msgpack:"rows"`
	Count int   `msgpack:"count"`
}

func ParseArgs(in map[string]any) (*Args, *proto.Error) {
	a := &Args{}
	var perr *proto.Error
	if a.Last, perr = argutil.OptionalPosInt(in, "last", DefaultLast, MaxLast); perr != nil {
		return nil, perr
	}
	if a.Verb, perr = argutil.OptionalString(in, "verb", ""); perr != nil {
		return nil, perr
	}
	return a, nil
}

// RunWithLedger executes a metrics query against the open ledger. The
// daemon is the only caller — the ledger is daemon-owned — so this lives
// here rather than as a free Run because it can't operate without one.
func RunWithLedger(led *ledger.Ledger, a *Args) (*Result, *proto.Error) {
	calls, qerr := led.QueryRecent(a.Last, a.Verb)
	if qerr != nil {
		return nil, &proto.Error{Code: "ledger", Msg: qerr.Error()}
	}
	return ResultFromCalls(calls), nil
}

func ResultFromCalls(calls []ledger.Call) *Result {
	rows := make([]Row, 0, len(calls))
	for _, c := range calls {
		rows = append(rows, Row{
			Timestamp:     c.Timestamp.UnixNano(),
			Verb:          c.Verb,
			OK:            c.OK,
			ErrCode:       c.ErrCode,
			TokensIn:      c.TokensIn,
			TokensOut:     c.TokensOut,
			LatencyExecUs: c.LatencyExecUs,
			BytesIn:       c.BytesIn,
			BytesOut:      c.BytesOut,
			Truncated:     c.Truncated,
			WalkUs:            c.WalkUs,
			IOUs:              c.IOUs,
			RegexUs:           c.RegexUs,
			RegexCompileUs:    c.RegexCompileUs,
			LatencyDispatchUs: c.LatencyDispatchUs,
		})
	}
	return &Result{Rows: rows, Count: len(rows)}
}

func PrettyResponse(req *proto.Request, rsp *proto.Response) string {
	if !rsp.OK {
		return proto.PrettyResponseHeader(rsp)
	}
	var r Result
	if err := proto.UnmarshalData(rsp, &r); err != nil {
		return "ok\n<unrecognized metrics result>"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "=== ash metrics: %d calls", r.Count)
	if scope := scopeFromArgs(req); scope != "" {
		fmt.Fprintf(&b, " [%s]", scope)
	}
	b.WriteString(" ===\n")
	for _, row := range r.Rows {
		writeRow(&b, row)
	}
	return strings.TrimRight(b.String(), "\n")
}

func writeRow(b *strings.Builder, r Row) {
	ts := time.Unix(0, r.Timestamp).UTC().Format("2006-01-02T15:04:05Z")
	status := "ok "
	if !r.OK {
		status = "ERR"
	}
	fmt.Fprintf(b, "%s  %-8s  %s  in=%-5d  out=%-5d  exec_us=%-8d",
		ts, r.Verb, status, r.TokensIn, r.TokensOut, r.LatencyExecUs)
	// Sub-phase breakdown — only printed when the verb instrumented at
	// least one phase. Keeps the row short for help/metrics, surfaces
	// detail for find/grep/read.
	if r.WalkUs > 0 {
		fmt.Fprintf(b, "  walk_us=%-7d", r.WalkUs)
	}
	if r.IOUs > 0 {
		fmt.Fprintf(b, "  io_us=%-7d", r.IOUs)
	}
	if r.RegexUs > 0 {
		fmt.Fprintf(b, "  regex_us=%-5d", r.RegexUs)
	}
	if r.RegexCompileUs > 0 {
		fmt.Fprintf(b, "  regex_compile_us=%-5d", r.RegexCompileUs)
	}
	if r.LatencyDispatchUs > 0 {
		fmt.Fprintf(b, "  dispatch_us=%-5d", r.LatencyDispatchUs)
	}
	if r.ErrCode != "" {
		fmt.Fprintf(b, "  err=%s", r.ErrCode)
	}
	if r.Truncated {
		b.WriteString("  truncated")
	}
	b.WriteByte('\n')
}

func scopeFromArgs(req *proto.Request) string {
	if req == nil || req.Args == nil {
		return ""
	}
	var parts []string
	if v, ok := req.Args["last"]; ok {
		if n, ok := argutil.ToInt(v); ok && n != DefaultLast {
			parts = append(parts, fmt.Sprintf("last=%d", n))
		}
	}
	if v, ok := req.Args["verb"].(string); ok && v != "" {
		parts = append(parts, "verb="+v)
	}
	return strings.Join(parts, ", ")
}

func CompactResponse(rsp *proto.Response) (any, error) {
	if !rsp.OK {
		return nil, nil
	}
	var r Result
	if err := proto.UnmarshalData(rsp, &r); err != nil {
		return nil, err
	}
	cd := proto.CompactData{
		K: []string{"ts", "verb", "ok", "err", "ti", "to", "ex_us", "bi", "bo", "trunc", "walk", "io", "re", "recp", "disp"},
		R: make([][]any, len(r.Rows)),
	}
	for i, row := range r.Rows {
		cd.R[i] = []any{
			row.Timestamp, row.Verb, row.OK, row.ErrCode,
			row.TokensIn, row.TokensOut, row.LatencyExecUs,
			row.BytesIn, row.BytesOut, row.Truncated,
			row.WalkUs, row.IOUs, row.RegexUs, row.RegexCompileUs, row.LatencyDispatchUs,
		}
	}
	return cd, nil
}
