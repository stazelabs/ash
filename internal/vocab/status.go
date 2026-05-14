package vocab

import "sort"

// statusRegistry is a hand-curated table of the status-enum values that
// agents see on the wire. AST extraction is too brittle for this small
// set (bare string assignments like `t.Status = "pass"` are
// indistinguishable from any other "pass" literal in the source tree).
// The Context column maps each value to its emitting verb so drift is
// visible in the inventory: if a new value lands in test.go without an
// entry here, the cross-check at the end of Generate flags it.
//
// Sources of truth (the comments next to each Status field):
//
//   - test verb:     internal/verbs/test/test.go:135-140 (test/package status)
//   - stop verb:     internal/verbs/stop/stop.go:39 (operation status)
//   - git diff verb: internal/verbs/git/{diff,gogit_diff}.go (per-file letter)
//   - envelope:      internal/proto/pretty.go (response OK/err prefix)
type statusEntry struct {
	Verb  string
	Field string // logical field — "Test.Status", "Package.Status", "Result.Status", "FileDiff.Status".
	Value string
}

var statusRegistry = []statusEntry{
	// proto envelope — every verb response.
	{"_envelope", "Response", "ok"},
	{"_envelope", "Response", "err"},

	// test verb.
	{"test", "Test.Status", "pass"},
	{"test", "Test.Status", "fail"},
	{"test", "Test.Status", "skip"},
	{"test", "Package.Status", "pass"},
	{"test", "Package.Status", "fail"},
	{"test", "Package.Status", "skip"},
	{"test", "Package.Status", "build_failed"},
	{"test", "Package.Status", "no_tests"},
	{"test", "Package.Status", "timeout"},

	// stop verb.
	{"stop", "Result.Status", "stopped"},
	{"stop", "Result.Status", "already_stopped"},
	{"stop", "Result.Status", "timeout"},

	// git diff verb — single-letter porcelain-style status per file.
	{"git", "FileDiff.Status", "A"},
	{"git", "FileDiff.Status", "D"},
	{"git", "FileDiff.Status", "M"},
	{"git", "FileDiff.Status", "R"},
	{"git", "FileDiff.Status", "C"},
}

func extractStatus(counter Counter) []Entry {
	out := make([]Entry, 0, len(statusRegistry))
	for _, s := range statusRegistry {
		ctx := s.Verb + ":" + s.Field
		out = append(out, Entry{
			Literal: s.Value,
			Tokens:  counter.Count(s.Value),
			Context: ctx,
		})
	}
	sort.Slice(out, func(i, j int) bool { return entryLess(out[i], out[j]) })
	return out
}
