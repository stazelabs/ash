package main

import (
	"bytes"
	"io"
	"testing"

	"github.com/stazelabs/ash/internal/verbs/hook"
)

// Representative PreToolUse payloads. Three cases pin down the hot path:
//
//   - allowGrep:    a Grep call (no exclude_verbs) — deny path through the
//                   read/grep/write/edit switch, the most common branch.
//   - allowBashLs:  a Bash `ls -la` — passes the bash parser without
//                   matching any redirect rule, exits via the allow fallthrough.
//   - denyHeredoc:  a bash heredoc redirecting into a file — exercises the
//                   heaviest path through segments() + detectOutputRedirect().
var hookBenchPayloads = map[string][]byte{
	"deny_grep":    []byte(`{"tool_name":"Grep","tool_input":{"pattern":"foo","path":"internal/"}}`),
	"allow_bash":   []byte(`{"tool_name":"Bash","tool_input":{"command":"ls -la"}}`),
	"deny_heredoc": []byte(`{"tool_name":"Bash","tool_input":{"command":"cat > /tmp/x << 'EOF'\nhello\nEOF\n"}}`),
}

func BenchmarkRunHookFromReader(b *testing.B) {
	for name, payload := range hookBenchPayloads {
		b.Run(name, func(b *testing.B) {
			r := bytes.NewReader(nil)
			w := io.Discard
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				r.Reset(payload)
				_, _, _ = runHookFromReader(r, w)
			}
		})
	}
}

// BenchmarkHookDecide isolates the pure decision step from I/O and config
// load, so we can see how much of runHookFromReader's cost is the rules
// engine versus the surrounding plumbing.
func BenchmarkHookDecide(b *testing.B) {
	for name, payload := range hookBenchPayloads {
		_, args, err := hook.ExtractArgs(payload)
		if err != nil {
			b.Fatalf("%s: extract: %v", name, err)
		}
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = hook.Decide(args)
			}
		})
	}
}
