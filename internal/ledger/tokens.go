package ledger

import (
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
	return len(c.enc.Encode(s, nil, nil))
}
