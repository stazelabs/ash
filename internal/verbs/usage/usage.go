// Package usage implements the `ash usage` verb (ASH-134).
//
// `usage` is the populator for the Anthropic prompt-cache columns
// (tokens_cache_hit / tokens_cache_miss) reserved by ASH-108. The
// agent (or harness) calls it after observing cache numbers in the
// Claude API response and ash retroactively annotates the prior
// ledger row.
//
// Args:
//
//	hit  int    (optional) - cache_read_input_tokens from the API response.
//	miss int    (optional) - cache_creation_input_tokens from the API response.
//	for  uint64 (optional) - request_id of a specific prior call to annotate.
//	                         When omitted, the most recent non-usage call in
//	                         the current session is annotated.
//
// At least one of --hit or --miss must be > 0; a usage call with both at
// zero is rejected so we don't quietly write meaningless rows.
//
// Result reports the row that was updated (row id, request id, verb) and
// echoes back the values written, so the caller can confirm the right
// prior call landed the telemetry.
package usage

import (
	"github.com/stazelabs/ash/internal/ledger"
	"github.com/stazelabs/ash/internal/proto"
	"github.com/stazelabs/ash/internal/verbs/argutil"
)

// MaxCacheTokens caps either --hit or --miss. Anthropic prompt-cache
// numbers are per-message input-token counts; even a fully-packed 200k
// context message stays well under this. The cap is defense-in-depth
// against a misformatted call shoveling absurd values into the ledger.
const MaxCacheTokens = 10_000_000

type Args struct {
	Hit  int
	Miss int
	// For is the request_id of the call to annotate. Zero means "the
	// most recent non-usage call in the current session".
	For uint64
}

// Result is the structured response of `ash usage`.
type Result struct {
	RowID     int64  `msgpack:"row_id"`
	RequestID uint64 `msgpack:"request_id"`
	Verb      string `msgpack:"verb"`
	Hit       int    `msgpack:"hit"`
	Miss      int    `msgpack:"miss"`
}

func ParseArgs(in map[string]any) (*Args, *proto.Error) {
	a := &Args{}
	var perr *proto.Error
	if a.Hit, perr = argutil.OptionalNonNegInt(in, "hit", 0, MaxCacheTokens); perr != nil {
		return nil, perr
	}
	if a.Miss, perr = argutil.OptionalNonNegInt(in, "miss", 0, MaxCacheTokens); perr != nil {
		return nil, perr
	}
	if a.Hit == 0 && a.Miss == 0 {
		return nil, &proto.Error{Code: "args", Msg: "at least one of --hit or --miss must be > 0"}
	}
	if v, ok := in["for"]; ok && v != nil {
		n, ok := argutil.ToInt64(v)
		if !ok || n < 0 {
			return nil, &proto.Error{Code: "args", Msg: "for must be a non-negative integer (request_id)"}
		}
		a.For = uint64(n)
	}
	return a, nil
}

// RunWithLedger locates the target row (by --for or by "most recent
// non-usage call in session") and patches the cache columns in place.
// The usage call's own row hasn't been Record()ed yet — Record runs in
// the daemon after the verb returns — so no exclusion is required when
// scanning for the most-recent prior call.
func RunWithLedger(led *ledger.Ledger, a *Args) (*Result, *proto.Error) {
	var (
		rowID int64
		reqID uint64
		verb  string
		err   error
	)
	if a.For != 0 {
		rowID, verb, err = led.FindRowByRequestID(a.For)
		if err != nil {
			return nil, &proto.Error{Code: "ledger", Msg: err.Error()}
		}
		if rowID == 0 {
			return nil, &proto.Error{Code: "not_found", Msg: "no call with that request_id in this session"}
		}
		if verb == "usage" {
			return nil, &proto.Error{Code: "args", Msg: "cannot annotate a usage call itself"}
		}
		reqID = a.For
	} else {
		rowID, reqID, verb, err = led.FindMostRecentNonUsageRow(0)
		if err != nil {
			return nil, &proto.Error{Code: "ledger", Msg: err.Error()}
		}
		if rowID == 0 {
			return nil, &proto.Error{Code: "not_found", Msg: "no prior non-usage call in this session"}
		}
	}
	if err := led.UpdateCacheStats(rowID, a.Hit, a.Miss); err != nil {
		return nil, &proto.Error{Code: "ledger", Msg: err.Error()}
	}
	return &Result{
		RowID:     rowID,
		RequestID: reqID,
		Verb:      verb,
		Hit:       a.Hit,
		Miss:      a.Miss,
	}, nil
}

// PrettyResponse renders one line: "§usage: row=N verb=<v> req=<id> hit=H miss=M".
// The header is the only pretty surface; there's no result body since the
// agent already has the numbers it just wrote.
func PrettyResponse(_ *proto.Request, rsp *proto.Response) string {
	if !rsp.OK {
		return proto.PrettyResponseHeader(rsp)
	}
	var r Result
	if err := proto.UnmarshalData(rsp, &r); err != nil {
		return "ok\n<unrecognized usage result>"
	}
	return formatLine(r)
}

func formatLine(r Result) string {
	return "§usage: row=" + itoa64(r.RowID) +
		" verb=" + r.Verb +
		" req=" + itoa64(int64(r.RequestID)) +
		" hit=" + itoa(r.Hit) +
		" miss=" + itoa(r.Miss)
}

func itoa(n int) string {
	return itoa64(int64(n))
}

func itoa64(n int64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
