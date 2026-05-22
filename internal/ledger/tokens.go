package ledger

import (
	"sort"
	"strings"

	"github.com/pkoukk/tiktoken-go"
	tiktoken_loader "github.com/pkoukk/tiktoken-go-loader"
)

// TokenizerName is the BPE encoding used for token counts. cl100k_base is the
// GPT-4 / Claude-ish standard; counts are not exact for Claude's tokenizer but
// are close and stable across runs, which is what matters for comparisons.
const TokenizerName = "cl100k_base"

// TokensMethod is the value persisted in the ledger's tokens_method column so
// future readers can tell how a count was produced.
const TokensMethod = "real:" + TokenizerName

// Counter wraps tiktoken-go for token counting. The BPE table is bundled in
// the binary via tiktoken-go-loader, so first use does not hit the network.
type Counter struct {
	enc *tiktoken.Tiktoken
}

func NewCounter() (*Counter, error) {
	tiktoken.SetBpeLoader(tiktoken_loader.NewOfflineLoader())
	enc, err := tiktoken.GetEncoding(TokenizerName)
	if err != nil {
		return nil, err
	}
	return &Counter{enc: enc}, nil
}

func (c *Counter) Count(s string) int {
	if c == nil || c.enc == nil {
		return 0
	}
	// EncodeOrdinary skips the special-token scan — ash responses never
	// contain <|endoftext|>-style tokens, so counts are identical and
	// the path is cheaper (one fewer full []rune pass).
	return len(c.enc.EncodeOrdinary(s))
}

// StripPrefixes returns s with every occurrence of "<prefix>/" replaced
// by the empty string, for each prefix in prefixes. Longest prefixes
// are tried first so "/a/b" strips before "/a" would mask it.
//
// Used to compute a path-prefix-free variant of the pretty response for
// the ledger's tokens_out_no_prefix column (ASH-71). The substitution
// is literal — no path-context check — which is fine for measurement:
// the worst case is over-stripping a prefix that happens to appear in
// non-path text, which only inflates the estimated tax slightly.
func StripPrefixes(s string, prefixes []string) string {
	if s == "" || len(prefixes) == 0 {
		return s
	}
	sorted := make([]string, 0, len(prefixes))
	for _, p := range prefixes {
		if p == "" || p == "/" {
			continue
		}
		sorted = append(sorted, p)
	}
	sort.Slice(sorted, func(i, j int) bool { return len(sorted[i]) > len(sorted[j]) })
	for _, p := range sorted {
		s = strings.ReplaceAll(s, p+"/", "")
	}
	return s
}
