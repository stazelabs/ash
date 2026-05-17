package lang

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stazelabs/ash/internal/lsp"
	"github.com/stazelabs/ash/internal/lsp/cache"
)

// goplsAvailable skips the test cleanly when gopls is not on PATH so a
// developer box without it still produces a green `go test ./...`.
func goplsAvailable(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("gopls"); err != nil {
		t.Skip("gopls not on PATH; skipping ash lang integration test")
	}
}

// goModWorkspace writes a minimal Go module with two named decls so
// gopls can produce documentSymbol output without indexing the whole
// host repo.
func goModWorkspace(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "go.mod"), "module example.test/p\n\ngo 1.21\n")
	mustWrite(t, filepath.Join(dir, "p.go"), `package p

// Greeter is the exported type.
type Greeter struct{ Name string }

// Hello returns a greeting.
func (g Greeter) Hello() string { return "hi " + g.Name }

// Run is a free function.
func Run() error { return nil }
`)
	return dir
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestParseArgs covers the validation surface — wrong op, missing
// required args, jail check, unknown arg.
func TestParseArgs(t *testing.T) {
	if _, perr := ParseArgs(map[string]any{}); perr == nil || perr.Code != "args" {
		t.Errorf("missing op: want args error")
	}
	if _, perr := ParseArgs(map[string]any{"op": "outlinex"}); perr == nil || perr.Code != "args" {
		t.Errorf("unknown op: want args error")
	}
	if _, perr := ParseArgs(map[string]any{"op": "outline"}); perr == nil {
		t.Errorf("outline without --path should fail")
	}
	if _, perr := ParseArgs(map[string]any{"op": "def"}); perr == nil {
		t.Errorf("def without --symbol should fail")
	}
	if _, perr := ParseArgs(map[string]any{"op": "outline", "path": "/etc/passwd", "bogus": 1}); perr == nil {
		t.Errorf("unknown arg should fail")
	}
	if a, perr := ParseArgs(map[string]any{"op": "outline", "path": "/tmp/x.go"}); perr != nil || a.Op != "outline" {
		t.Errorf("valid outline parse: a=%+v perr=%v", a, perr)
	}
}

func TestRunWithDeps_RejectsDisabledBroker(t *testing.T) {
	d := Deps{Broker: lsp.New(lsp.Config{Enabled: false, Root: t.TempDir()})}
	_, perr := RunWithDeps(d, &Args{Op: "outline", Path: "x.go"})
	if perr == nil || perr.Code != "lsp_disabled" {
		t.Fatalf("expected lsp_disabled, got %v", perr)
	}
}

// TestOutline_CachedRoundTrip covers the headline ASH-138 + ASH-137
// integration: first call goes to gopls and Put-s into the cache;
// second call serves from the cache (CacheHit=true) without touching
// gopls. We assert the cache hit explicitly via the Stats counter so
// the test fails loudly if the cache wiring breaks.
func TestOutline_CachedRoundTrip(t *testing.T) {
	goplsAvailable(t)
	root := goModWorkspace(t)
	broker := lsp.New(lsp.Config{Enabled: true, Root: root})
	t.Cleanup(func() { _ = broker.Close() })
	c, err := cache.Open(cache.Options{Path: filepath.Join(root, "lang-cache.db")})
	if err != nil {
		t.Fatalf("cache.Open: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	deps := Deps{Broker: broker, Cache: c, ProjectRoot: root}
	pgoPath := filepath.Join(root, "p.go")

	res1, perr := RunWithDeps(deps, &Args{Op: "outline", Path: pgoPath})
	if perr != nil {
		t.Fatalf("first outline: %v", perr)
	}
	if res1.CacheHit {
		t.Errorf("first call should be a miss")
	}
	if len(res1.Symbols) == 0 {
		t.Fatalf("outline returned no symbols; got Result=%+v", res1)
	}
	// Confirm we got the expected three top-level decls. Note that
	// child methods nest under their parent struct in DocumentSymbol[],
	// so Greeter.Hello shows up with container="Greeter".
	names := map[string]bool{}
	for _, s := range res1.Symbols {
		names[s.Name] = true
	}
	for _, want := range []string{"Greeter", "Run"} {
		if !names[want] {
			t.Errorf("outline missing symbol %q; got %+v", want, names)
		}
	}

	// Second call: should hit the cache.
	res2, perr := RunWithDeps(deps, &Args{Op: "outline", Path: pgoPath})
	if perr != nil {
		t.Fatalf("second outline: %v", perr)
	}
	if !res2.CacheHit {
		t.Errorf("second call should hit the cache; CacheHit=false")
	}
	if s := c.Snapshot(); s.Hits < 1 || s.Puts < 1 {
		t.Errorf("cache counters: %+v want hits>=1 puts>=1", s)
	}
}

// TestDef_WorkspaceSymbol asserts the def op routes through
// workspace/symbol and surfaces an exact-name match. workspace/symbol
// is async-indexed in gopls, so we tolerate a slow first response —
// the broker's 15s request timeout covers cold start.
func TestDef_WorkspaceSymbol(t *testing.T) {
	goplsAvailable(t)
	root := goModWorkspace(t)
	broker := lsp.New(lsp.Config{Enabled: true, Root: root})
	t.Cleanup(func() { _ = broker.Close() })
	deps := Deps{Broker: broker, ProjectRoot: root}

	res, perr := RunWithDeps(deps, &Args{Op: "def", Symbol: "Greeter"})
	if perr != nil {
		t.Fatalf("def: %v", perr)
	}
	if len(res.Symbols) == 0 {
		t.Fatalf("def returned no symbols; got %+v", res)
	}
	for _, s := range res.Symbols {
		if s.Name != "Greeter" {
			t.Errorf("non-exact match returned: %q", s.Name)
		}
	}
}

func TestPrettyResponse_NilSafe(t *testing.T) {
	if got := PrettyResponse(nil, nil); got != "" {
		t.Errorf("nil response should produce empty string; got %q", got)
	}
}
