// Cache-prefix metric for `ash replay` (ASH-135).
//
// ASH-108 reordered proto.Response so the cache-stable prefix (V, OK,
// Data, Err) precedes the volatile suffix (ID, Metrics). The structural
// claim was pinned by synthetic tests in internal/proto/proto_test.go
// but had no empirical scoreboard against real prior sessions. This
// file replays consecutive same-verb calls and measures the matching
// byte-prefix between their encoded responses — once with today's
// cache-aware envelope and once with a struct that mirrors the
// pre-ASH-108 ordering. The delta is how many bytes the reorder
// bought a hypothetical Anthropic prompt cache.

package replay

import (
	"github.com/stazelabs/ash/internal/proto"
	"github.com/vmihailenco/msgpack/v5"
)

// legacyResponse mirrors the pre-ASH-108 proto.Response field order
// (V, ID, OK, Data, Err, Metrics). Used only by cache_prefix.go for
// A/B encoding — never on the wire. Reordering proto.Response itself
// would break the cache contract and trip
// TestResponse_VolatileSuffixOrdering; the test-time A/B has to live
// in its own struct.
type legacyResponse struct {
	V       int                `msgpack:"v"`
	ID      uint64             `msgpack:"id"`
	OK      bool               `msgpack:"ok"`
	Data    msgpack.RawMessage `msgpack:"data,omitempty"`
	Err     *proto.Error       `msgpack:"err,omitempty"`
	Metrics *proto.Metrics     `msgpack:"metrics,omitempty"`
}

func encodeLegacy(rsp *proto.Response) ([]byte, error) {
	return msgpack.Marshal(&legacyResponse{
		V:       rsp.V,
		ID:      rsp.ID,
		OK:      rsp.OK,
		Data:    rsp.Data,
		Err:     rsp.Err,
		Metrics: rsp.Metrics,
	})
}

// commonPrefixLen returns the length of the longest byte prefix a and
// b share.
func commonPrefixLen(a, b []byte) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}

// CachePrefixSummary is the per-verb (or overall) result of the
// cache-prefix A/B.
//
// Pairs counts the consecutive same-verb pairs considered.
// StablePairs is the subset where both responses had byte-identical
// Data — the case where the bounded-stable middle is itself stable, so
// the only variation between the two encoded forms is the volatile
// suffix. AvgPrefixNew / AvgPrefixOld are the mean matching-prefix
// length across StablePairs, in bytes; AvgEncodedLen contextualizes
// those numbers against the total encoded response size. AvgPrefixGain
// is AvgPrefixNew - AvgPrefixOld and is the empirical win attributable
// to the ASH-108 reorder.
type CachePrefixSummary struct {
	Verb          string `msgpack:"verb" json:"verb"`
	Pairs         int    `msgpack:"pairs" json:"pairs"`
	StablePairs   int    `msgpack:"stable_pairs" json:"stable_pairs"`
	AvgEncodedLen int    `msgpack:"avg_enc_len" json:"avg_enc_len"`
	AvgPrefixNew  int    `msgpack:"avg_prefix_new" json:"avg_prefix_new"`
	AvgPrefixOld  int    `msgpack:"avg_prefix_old" json:"avg_prefix_old"`
	AvgPrefixGain int    `msgpack:"avg_prefix_gain" json:"avg_prefix_gain"`
}

// CachePrefixResult is the cache-prefix block returned from RunWithDeps
// when --cache_prefix=true. ByVerb is sorted alphabetically; Overall
// aggregates across every counted pair.
type CachePrefixResult struct {
	ByVerb  []CachePrefixSummary `msgpack:"by_verb" json:"by_verb"`
	Overall CachePrefixSummary   `msgpack:"overall" json:"overall"`
}

// verbResp is the per-call payload computeCachePrefix consumes. The
// list arrives chronologically (oldest first) so consecutive pair
// (i, i+1) mirrors the order the agent's conversation transcript
// would have seen.
type verbResp struct {
	Verb string
	Rsp  *proto.Response
}

// computeCachePrefix produces the per-verb summary. Inputs must be in
// chronological order (oldest first). Pairs are formed within each
// verb only: cross-verb consecutive pairs share a single-byte prefix
// (just the protocol-version key) and would dilute the metric.
//
// Each pair is encoded with synthetic, distinct ID and Metrics values
// before measurement. replayOne deliberately leaves those volatile
// fields zero (the daemon populates them after the verb runs, not at
// dispatch time), so without this synthesis the encoded bytes would be
// byte-identical and the matching prefix would equal the entire
// encoded length — meaningless as an A/B against the legacy ordering.
// The synthesis mirrors what real cross-call variance looks like: a
// random ID per call and slightly different LatencyExecUs / TokensOut
// numbers.
func computeCachePrefix(resps []verbResp) *CachePrefixResult {
	byVerb := map[string][]*proto.Response{}
	for _, r := range resps {
		byVerb[r.Verb] = append(byVerb[r.Verb], r.Rsp)
	}

	var summaries []CachePrefixSummary
	overall := CachePrefixSummary{Verb: "overall"}
	var (
		overallSumNew, overallSumOld, overallSumLen int64
	)

	for verb, rsps := range byVerb {
		if len(rsps) < 2 {
			continue
		}
		s := CachePrefixSummary{Verb: verb}
		var sumNew, sumOld, sumLen int64
		for i := 1; i < len(rsps); i++ {
			ra := withVolatile(rsps[i-1], uint64(i)*0x1111111111111111, int64(100+i*7))
			rb := withVolatile(rsps[i], uint64(i+1)*0x9999999999999999, int64(200+i*13))
			a, errA := proto.EncodeResponse(&ra)
			b, errB := proto.EncodeResponse(&rb)
			if errA != nil || errB != nil {
				continue
			}
			s.Pairs++
			overall.Pairs++
			if !bytesEq(ra.Data, rb.Data) {
				continue
			}
			la, errLA := encodeLegacy(&ra)
			lb, errLB := encodeLegacy(&rb)
			if errLA != nil || errLB != nil {
				continue
			}
			s.StablePairs++
			overall.StablePairs++
			sumNew += int64(commonPrefixLen(a, b))
			sumOld += int64(commonPrefixLen(la, lb))
			sumLen += int64(len(a))
		}
		if s.StablePairs > 0 {
			s.AvgPrefixNew = int(sumNew / int64(s.StablePairs))
			s.AvgPrefixOld = int(sumOld / int64(s.StablePairs))
			s.AvgPrefixGain = s.AvgPrefixNew - s.AvgPrefixOld
			s.AvgEncodedLen = int(sumLen / int64(s.StablePairs))
			overallSumNew += sumNew
			overallSumOld += sumOld
			overallSumLen += sumLen
		}
		summaries = append(summaries, s)
	}

	if overall.StablePairs > 0 {
		overall.AvgPrefixNew = int(overallSumNew / int64(overall.StablePairs))
		overall.AvgPrefixOld = int(overallSumOld / int64(overall.StablePairs))
		overall.AvgPrefixGain = overall.AvgPrefixNew - overall.AvgPrefixOld
		overall.AvgEncodedLen = int(overallSumLen / int64(overall.StablePairs))
	}

	sortByVerb(summaries)
	return &CachePrefixResult{ByVerb: summaries, Overall: overall}
}

// withVolatile returns a shallow copy of rsp with ID and Metrics set to
// the supplied synthetic values. The original Data and OK fields ride
// through unchanged so the bounded-stable middle of two consecutive
// responses can still match exactly when the world held still.
func withVolatile(rsp *proto.Response, id uint64, latencyUs int64) proto.Response {
	return proto.Response{
		V:    rsp.V,
		OK:   rsp.OK,
		Data: rsp.Data,
		Err:  rsp.Err,
		ID:   id,
		Metrics: &proto.Metrics{
			LatencyExecUs: latencyUs,
			TokensOut:     int(latencyUs / 3),
			BytesOut:      int(latencyUs * 4),
		},
	}
}

func bytesEq(a, b msgpack.RawMessage) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func sortByVerb(s []CachePrefixSummary) {
	// Insertion sort — the list is small (one entry per replayed verb,
	// max ~14) so avoiding a sort.Slice keeps the file dep-free.
	for i := 1; i < len(s); i++ {
		j := i
		for j > 0 && s[j-1].Verb > s[j].Verb {
			s[j-1], s[j] = s[j], s[j-1]
			j--
		}
	}
}
