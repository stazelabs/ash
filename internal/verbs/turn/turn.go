// Package turn implements the `turn` verb: ingest Anthropic prompt-cache
// accounting reported by the harness after each assistant message.
//
// Background: ASH-188 / ASH-185 Option A. The Claude Code Stop hook
// fires after every assistant turn ends. cmd/ash/hook_stop.go scrapes
// the transcript JSONL for the message just emitted, extracts the
// `usage` block (input_tokens / output_tokens / cache_read_input_tokens
// / cache_creation_input_tokens), and fires this verb fire-and-forget
// to the daemon. The verb upserts a row into the ledger `turns` table,
// idempotent on the Anthropic message.id.
//
// `ash usage` reads from `turns` to surface real cache hit rate
// alongside the existing arg-repetition proxy. See docs/cache-telemetry.md.
//
// Result body is intentionally tiny — the verb is meta-instrumentation,
// not agent-facing.
package turn

import (
	"fmt"
	"strings"
	"time"

	"github.com/stazelabs/ash/internal/ledger"
	"github.com/stazelabs/ash/internal/proto"
	"github.com/stazelabs/ash/internal/verbs/argutil"
)

type Args struct {
	TurnID              string
	HarnessSessionID    string
	Model               string
	InputTokens         int
	OutputTokens        int
	CacheReadTokens     int
	CacheCreationTokens int
	TimestampNanos      int64
}

type Result struct {
	TurnID   string `msgpack:"turn_id"`
	Inserted bool   `msgpack:"inserted"`
}

const maxTokens = 1 << 30 // generous upper bound; rejects garbage without bounding real values

func ParseArgs(in map[string]any) (*Args, *proto.Error) {
	a := &Args{}
	turnID, perr := argutil.RequireString(in, "turn_id")
	if perr != nil {
		return nil, perr
	}
	a.TurnID = turnID
	if s, perr := argutil.OptionalString(in, "harness_session_id", ""); perr != nil {
		return nil, perr
	} else {
		a.HarnessSessionID = s
	}
	if s, perr := argutil.OptionalString(in, "model", ""); perr != nil {
		return nil, perr
	} else {
		a.Model = s
	}
	if n, perr := argutil.OptionalNonNegInt(in, "input_tokens", 0, maxTokens); perr != nil {
		return nil, perr
	} else {
		a.InputTokens = n
	}
	if n, perr := argutil.OptionalNonNegInt(in, "output_tokens", 0, maxTokens); perr != nil {
		return nil, perr
	} else {
		a.OutputTokens = n
	}
	if n, perr := argutil.OptionalNonNegInt(in, "cache_read_tokens", 0, maxTokens); perr != nil {
		return nil, perr
	} else {
		a.CacheReadTokens = n
	}
	if n, perr := argutil.OptionalNonNegInt(in, "cache_creation_tokens", 0, maxTokens); perr != nil {
		return nil, perr
	} else {
		a.CacheCreationTokens = n
	}
	if v, ok := in["timestamp_nanos"]; ok && v != nil {
		ns, ok := argutil.ToInt64(v)
		if !ok {
			return nil, &proto.Error{Code: "args", Msg: "timestamp_nanos must be an integer"}
		}
		a.TimestampNanos = ns
	}
	return a, nil
}

func RunWithLedger(led *ledger.Ledger, a *Args) (*Result, *proto.Error) {
	ts := time.Now()
	if a.TimestampNanos > 0 {
		ts = time.Unix(0, a.TimestampNanos)
	}
	_, inserted := led.InsertTurn(&ledger.Turn{
		TurnID:              a.TurnID,
		HarnessSessionID:    a.HarnessSessionID,
		Model:               a.Model,
		Timestamp:           ts,
		InputTokens:         a.InputTokens,
		OutputTokens:        a.OutputTokens,
		CacheReadTokens:     a.CacheReadTokens,
		CacheCreationTokens: a.CacheCreationTokens,
	})
	return &Result{TurnID: a.TurnID, Inserted: inserted}, nil
}

// PrettyResponse renders a one-line ack. Hook callers don't read the
// response (fire-and-forget), so this surface is for the rare CLI test
// invocation only.
func PrettyResponse(_ *proto.Request, rsp *proto.Response) string {
	if !rsp.OK {
		return proto.PrettyResponseHeader(rsp)
	}
	var r Result
	if err := proto.UnmarshalData(rsp, &r); err != nil {
		return "ok\n<unrecognized turn result>"
	}
	var b strings.Builder
	state := "duplicate"
	if r.Inserted {
		state = "inserted"
	}
	fmt.Fprintf(&b, "§turn: %s — %s\n", r.TurnID, state)
	return b.String()
}
