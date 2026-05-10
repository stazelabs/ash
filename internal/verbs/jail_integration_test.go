package verbs

// Integration test for ASH-61's jail enforcement. Verifies that every
// path-taking verb's ParseArgs invokes jail.CheckPaths and returns a
// "path_denied" proto.Error for paths outside the active policy. Lives
// in this package because internal/verbs imports every verb already,
// so a single file can exercise all of them.
//
// The per-verb tests in each subpackage cover positive cases (in-root
// paths, optional path defaults, etc.); this file covers the wiring
// only — that the check happens at all.

import (
	"path/filepath"
	"testing"

	"github.com/stazelabs/ash/internal/jail"
	"github.com/stazelabs/ash/internal/verbs/diff"
	"github.com/stazelabs/ash/internal/verbs/edit"
	"github.com/stazelabs/ash/internal/verbs/find"
	"github.com/stazelabs/ash/internal/verbs/git"
	"github.com/stazelabs/ash/internal/verbs/grep"
	"github.com/stazelabs/ash/internal/verbs/initverb"
	"github.com/stazelabs/ash/internal/verbs/read"
	"github.com/stazelabs/ash/internal/verbs/stat"
	testverb "github.com/stazelabs/ash/internal/verbs/test"
	"github.com/stazelabs/ash/internal/verbs/uninit"
	"github.com/stazelabs/ash/internal/verbs/write"
)

func TestJailEnforcement_AllVerbs(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir() // sibling temp dir, definitely outside root
	outsidePath := filepath.Join(outside, "x.go")

	jail.SetPolicy(jail.FromConfig(true, root, nil, nil))
	defer jail.SetPolicy(nil)

	cases := []struct {
		name  string
		parse func() *protoErrorShim
	}{
		{"read", func() *protoErrorShim {
			_, perr := read.ParseArgs(map[string]any{"path": outsidePath})
			return shim(perr)
		}},
		{"find", func() *protoErrorShim {
			_, perr := find.ParseArgs(map[string]any{"path": outsidePath})
			return shim(perr)
		}},
		{"grep", func() *protoErrorShim {
			_, perr := grep.ParseArgs(map[string]any{"pattern": "x", "path": outsidePath})
			return shim(perr)
		}},
		{"git", func() *protoErrorShim {
			_, perr := git.ParseArgs(map[string]any{"op": "status", "path": outsidePath})
			return shim(perr)
		}},
		{"diff:path", func() *protoErrorShim {
			_, perr := diff.ParseArgs(map[string]any{"path": outsidePath, "other": filepath.Join(root, "ok.go")})
			return shim(perr)
		}},
		{"diff:other", func() *protoErrorShim {
			// path inside root, other outside — exercises the second key.
			_, perr := diff.ParseArgs(map[string]any{"path": filepath.Join(root, "a.go"), "other": outsidePath})
			return shim(perr)
		}},
		{"edit", func() *protoErrorShim {
			_, perr := edit.ParseArgs(map[string]any{"path": outsidePath, "old_string": "x", "new_string": "y"})
			return shim(perr)
		}},
		{"write", func() *protoErrorShim {
			_, perr := write.ParseArgs(map[string]any{"path": outsidePath, "content": "x"})
			return shim(perr)
		}},
		{"stat", func() *protoErrorShim {
			_, perr := stat.ParseArgs(map[string]any{"paths": outsidePath})
			return shim(perr)
		}},
		{"init", func() *protoErrorShim {
			_, perr := initverb.ParseArgs(map[string]any{"path": outsidePath})
			return shim(perr)
		}},
		{"uninit", func() *protoErrorShim {
			_, perr := uninit.ParseArgs(map[string]any{"path": outsidePath})
			return shim(perr)
		}},
		{"test", func() *protoErrorShim {
			_, perr := testverb.ParseArgs(map[string]any{"packages": outsidePath})
			return shim(perr)
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			perr := tc.parse()
			if perr == nil {
				t.Fatalf("%s: expected path_denied for outside-root path %q", tc.name, outsidePath)
			}
			if perr.Code != "path_denied" {
				t.Errorf("%s: error code: want path_denied, got %q (msg=%s)", tc.name, perr.Code, perr.Msg)
			}
		})
	}
}

// TestJailEnforcement_DisabledAllowsAll verifies the no-config default —
// with no active policy, ParseArgs must accept any path.
func TestJailEnforcement_DisabledAllowsAll(t *testing.T) {
	jail.SetPolicy(nil)
	tmp := t.TempDir()
	mustExist := func(name string) string {
		p := filepath.Join(tmp, name)
		mustWrite(t, p)
		return p
	}
	if _, perr := read.ParseArgs(map[string]any{"path": mustExist("a.go")}); perr != nil {
		t.Errorf("read: unexpected denial: %v", perr)
	}
	if _, perr := write.ParseArgs(map[string]any{"path": filepath.Join(tmp, "new.go"), "content": "x"}); perr != nil {
		t.Errorf("write: unexpected denial: %v", perr)
	}
}
