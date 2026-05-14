// Package vocab generates an inventory of ash's stable agent-facing
// strings — every literal the tokenizer sees on input and output that
// doesn't depend on the user's data. Categories:
//
//   - verbs:    verb names from internal/verbs/help.Registry().
//   - flags:    flag names per verb (also from help.Registry).
//   - enums:    flag value enums (Values field on help.ArgSchema).
//   - errors:   error codes from &proto.Error{Code: "..."} composite
//               literals across internal/verbs/, internal/runner/,
//               internal/jail/ (AST walk).
//   - status:   status enum values (test/stop/git-diff status fields).
//               Hand-curated; the ~10 values are too few to AST-walk
//               reliably and the source-of-truth comment lives next to
//               the assignment sites.
//   - headers:  the `§<verb>: …` sentinel from pretty renderers,
//               plus the `[ash …]`
//               metrics footer and the `[truncation: …]` annotation.
//               AST scan for format-string literals.
//   - labels:   other label-shaped substrings inside PrettyResponse
//               format strings — best-effort heuristic extraction.
//
// Output: Inventory.Markdown() and Inventory.JSON().
package vocab

// Category names. Stable strings used as map keys in JSON output.
const (
	CategoryVerbs   = "verbs"
	CategoryFlags   = "flags"
	CategoryEnums   = "enums"
	CategoryErrors  = "errors"
	CategoryStatus  = "status"
	CategoryHeaders = "headers"
	CategoryLabels  = "labels"
)

// Categories lists category names in the canonical render order
// (smallest/most-stable categories first).
var Categories = []string{
	CategoryVerbs,
	CategoryFlags,
	CategoryEnums,
	CategoryStatus,
	CategoryErrors,
	CategoryHeaders,
	CategoryLabels,
}

// Site is a single source-code occurrence of a literal.
type Site struct {
	File string `json:"file"`
	Line int    `json:"line"`
}

// Entry is one literal in the inventory.
type Entry struct {
	// Literal is the agent-visible string. For error codes with a
	// non-literal construction (the runner's `prog + "_failed"`
	// pattern), Literal is the concrete expansion (e.g. "go_failed");
	// the computed-form site is preserved as a sibling entry.
	Literal string `json:"literal"`

	// Tokens is the cl100k_base token count for Literal.
	Tokens int `json:"cl100k_tokens"`

	// Context narrows the meaning of Literal within its category
	// (e.g. the verb a flag belongs to, the verb a header decorates).
	// Empty for category-global literals.
	Context string `json:"context,omitempty"`

	// Sites is the list of source-code occurrences. Empty for
	// category-imported entries (verbs/flags/enums/status), where the
	// source-of-truth is a registry rather than a scatter of literals.
	Sites []Site `json:"sites,omitempty"`

	// Hints carries the prose Hint: strings attached to an error code.
	// Empty for non-error entries.
	Hints []Hint `json:"hints,omitempty"`

	// Computed is true for error codes built from a binary-add
	// expression (runner's `prog + "_failed"` pattern). When true,
	// Literal is a placeholder like `<prog>+"_failed"` and concrete
	// expansions appear as sibling entries.
	Computed bool `json:"computed,omitempty"`
}

// Hint is a prose hint attached to an error code (post-ASH-84).
type Hint struct {
	Text   string `json:"text"`
	Tokens int    `json:"cl100k_tokens"`
}

// Inventory is the full vocabulary inventory.
type Inventory struct {
	GeneratedBy string             `json:"generated_by"`
	Tokenizer   string             `json:"tokenizer"`
	Categories  map[string][]Entry `json:"categories"`
}
