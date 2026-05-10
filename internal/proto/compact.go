package proto

// CompactData is the array-of-arrays wire shape for row-shaped verb responses.
// K lists abbreviated column names once; R contains one positional array per
// row. Eliminates per-row key repetition — 40-60% reduction on key overhead
// for verbs that return many rows (metrics, find, grep, git log, etc.).
type CompactData struct {
	K []string `json:"k"`
	R [][]any  `json:"r"`
}
