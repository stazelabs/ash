package vocab

import (
	"sort"

	"github.com/stazelabs/ash/internal/verbs/help"
)

// extractFromHelpRegistry derives the verb / flag / enum entries from the
// help package's canonical registry. No AST walking — help.Registry is the
// source of truth for the input surface, and we trust it directly.
func extractFromHelpRegistry(counter Counter) (verbs, flags, enums []Entry) {
	reg := help.Registry()

	// Verbs.
	for _, vs := range reg {
		verbs = append(verbs, Entry{
			Literal: vs.Verb,
			Tokens:  counter.Count(vs.Verb),
		})
	}
	sort.Slice(verbs, func(i, j int) bool { return entryLess(verbs[i], verbs[j]) })

	// Flags. Dedup across verbs — flag names like `path`, `glob`, `format`
	// appear on many verbs. We track each verb a flag is used on in
	// Context (comma-joined), since per-verb flag-name churn is a real
	// source of agent friction.
	flagVerbs := make(map[string][]string)
	for _, vs := range reg {
		for _, a := range vs.Args {
			flagVerbs[a.Name] = append(flagVerbs[a.Name], vs.Verb)
		}
	}
	for name, vlist := range flagVerbs {
		sort.Strings(vlist)
		ctx := joinCSV(dedupSorted(vlist))
		flags = append(flags, Entry{
			Literal: "--" + name,
			Tokens:  counter.Count("--" + name),
			Context: ctx,
		})
	}
	sort.Slice(flags, func(i, j int) bool { return entryLess(flags[i], flags[j]) })

	// Value enums. Each Values []string on an ArgSchema describes a
	// closed set. Emit one entry per *value*; Context names verb:flag.
	for _, vs := range reg {
		for _, a := range vs.Args {
			if len(a.Values) == 0 {
				continue
			}
			for _, v := range a.Values {
				enums = append(enums, Entry{
					Literal: v,
					Tokens:  counter.Count(v),
					Context: vs.Verb + ":--" + a.Name,
				})
			}
		}
	}
	sort.Slice(enums, func(i, j int) bool { return entryLess(enums[i], enums[j]) })

	return verbs, flags, enums
}

// entryLess sorts costliest-first, then alphabetically by literal, then
// by context — stable and useful for human review.
func entryLess(a, b Entry) bool {
	if a.Tokens != b.Tokens {
		return a.Tokens > b.Tokens
	}
	if a.Literal != b.Literal {
		return a.Literal < b.Literal
	}
	return a.Context < b.Context
}

func dedupSorted(in []string) []string {
	if len(in) <= 1 {
		return in
	}
	out := in[:1]
	for _, s := range in[1:] {
		if s != out[len(out)-1] {
			out = append(out, s)
		}
	}
	return out
}

func joinCSV(in []string) string {
	switch len(in) {
	case 0:
		return ""
	case 1:
		return in[0]
	}
	n := len(in) - 1
	for _, s := range in {
		n += len(s)
	}
	out := make([]byte, 0, n)
	for i, s := range in {
		if i > 0 {
			out = append(out, ',')
		}
		out = append(out, s...)
	}
	return string(out)
}
