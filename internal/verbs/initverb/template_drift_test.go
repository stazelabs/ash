package initverb

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stazelabs/ash/internal/verbs/help"
)

// TestTemplate_FlagNamesMatchSchema scans the embedded guidance template
// for `ash <verb> ... --<flag>` mentions and asserts each flag exists in
// help.Registry() for the named verb. This guards against the drift that
// motivated ASH-230 — agents acting on a template are operating blind:
// any --flag we teach must actually parse, and aliases (--old_string,
// --new_string) are deliberately not allowed here because the template
// is teaching canonical names.
//
// Strategy: walk each line. If the line names a verb (`ash <verb>`),
// every `--flag` on that line is bound to the most recent preceding verb
// on that line. Flags without a verb context on the same line are
// skipped (likely prose). The regex catches `--foo`, `--foo-bar`, and
// `--foo_bar`; `<!-- ash:begin -->` and `<name>` placeholders don't match.
func TestTemplate_FlagNamesMatchSchema(t *testing.T) {
	argSets := map[string]map[string]bool{}
	verbNames := make([]string, 0, len(help.Registry()))
	for _, v := range help.Registry() {
		set := map[string]bool{}
		for _, a := range v.Args {
			set[a.Name] = true
		}
		argSets[v.Verb] = set
		verbNames = append(verbNames, regexp.QuoteMeta(v.Verb))
	}

	verbRE := regexp.MustCompile(`\bash\s+(` + strings.Join(verbNames, "|") + `)\b`)
	flagRE := regexp.MustCompile(`--([a-z][a-z0-9_-]*)\b`)

	for ln, line := range strings.Split(guidanceBody, "\n") {
		verbHits := verbRE.FindAllStringSubmatchIndex(line, -1)
		if len(verbHits) == 0 {
			continue
		}
		for _, fh := range flagRE.FindAllStringSubmatchIndex(line, -1) {
			flag := line[fh[2]:fh[3]]
			verb := ""
			for _, vh := range verbHits {
				if vh[0] >= fh[0] {
					break
				}
				verb = line[vh[2]:vh[3]]
			}
			if verb == "" {
				continue
			}
			if !argSets[verb][flag] {
				t.Errorf("template.md line %d: `ash %s --%s` — flag not in help.Registry() for %q", ln+1, verb, flag, verb)
			}
		}
	}
}
