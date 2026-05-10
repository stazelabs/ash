package bench

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"sync"
)

// CaseSetVersion returns a stable identifier for the current canonical
// case list. It changes only when a case is added, removed, renamed, or
// has its verb/args changed; comments and Why text do NOT bump it.
//
// The hash is computed once via sync.Once at first call and cached.
//
// Format: "cs-" + first-8-hex of sha256 over canonical case data.
func CaseSetVersion() string {
	caseSetVersionOnce.Do(computeCaseSetVersion)
	return cachedCaseSetVersion
}

var (
	caseSetVersionOnce   sync.Once
	cachedCaseSetVersion string
)

func computeCaseSetVersion() {
	h := sha256.New()
	for _, c := range Cases {
		fmt.Fprintf(h, "%s\x00%s\x00", c.Name, c.Verb)
		keys := make([]string, 0, len(c.AshArgs))
		for k := range c.AshArgs {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(h, "%s\x00%v\x00", k, c.AshArgs[k])
		}
		h.Write([]byte{0x01})
	}
	cachedCaseSetVersion = "cs-" + hex.EncodeToString(h.Sum(nil)[:8])
}
