package cache

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newCache(t *testing.T, ttl time.Duration) (*Cache, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "lang-cache.db")
	c, err := Open(Options{Path: path, TTL: ttl})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c, dir
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestCache_GetMissOnFresh(t *testing.T) {
	c, dir := newCache(t, 0)
	p := filepath.Join(dir, "a.go")
	writeFile(t, p, "package a\n")

	got, hit, err := c.Get(p, "documentSymbol", map[string]any{"x": 1})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if hit {
		t.Fatalf("fresh cache should miss; got hit with %s", got)
	}
	if s := c.Snapshot(); s.Misses != 1 || s.Hits != 0 {
		t.Errorf("counters: %+v want misses=1 hits=0", s)
	}
}

func TestCache_PutThenGetHit(t *testing.T) {
	c, dir := newCache(t, 0)
	p := filepath.Join(dir, "a.go")
	writeFile(t, p, "package a\n")

	resp := []byte(`{"symbols":["A"]}`)
	if err := c.Put(p, "documentSymbol", map[string]any{"x": 1}, resp); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, hit, err := c.Get(p, "documentSymbol", map[string]any{"x": 1})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !hit {
		t.Fatalf("expected hit after Put")
	}
	if string(got) != string(resp) {
		t.Errorf("response mismatch: got %s want %s", got, resp)
	}
	if s := c.Snapshot(); s.Hits != 1 || s.Puts != 1 {
		t.Errorf("counters: %+v want hits=1 puts=1", s)
	}
}

// TestCache_MtimeInvalidation is the ASH-137 correctness story: a file
// touched on disk produces a miss even without an explicit Invalidate.
func TestCache_MtimeInvalidation(t *testing.T) {
	c, dir := newCache(t, 0)
	p := filepath.Join(dir, "a.go")
	writeFile(t, p, "package a\n")

	resp := []byte(`{"symbols":["A"]}`)
	if err := c.Put(p, "documentSymbol", nil, resp); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Bump mtime past the cached value. Stat resolution on macOS can
	// be 1us; sleep ensures the next mtime is strictly greater.
	time.Sleep(10 * time.Millisecond)
	writeFile(t, p, "package a\n// changed\n")

	_, hit, err := c.Get(p, "documentSymbol", nil)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if hit {
		t.Fatalf("Get after mtime bump should miss (cached row is stale)")
	}
}

// TestCache_InvalidateRemovesAllRowsForFile covers the write/edit
// invalidation hook contract.
func TestCache_InvalidateRemovesAllRowsForFile(t *testing.T) {
	c, dir := newCache(t, 0)
	p := filepath.Join(dir, "a.go")
	writeFile(t, p, "package a\n")

	// Three different op/args triples for the same file.
	for i, op := range []string{"documentSymbol", "definition", "references"} {
		if err := c.Put(p, op, map[string]any{"i": i}, []byte("body")); err != nil {
			t.Fatalf("Put %s: %v", op, err)
		}
	}
	n, err := c.Invalidate(p)
	if err != nil {
		t.Fatalf("Invalidate: %v", err)
	}
	if n != 3 {
		t.Errorf("Invalidate removed %d rows; want 3", n)
	}
	// All ops now miss.
	for _, op := range []string{"documentSymbol", "definition", "references"} {
		if _, hit, _ := c.Get(p, op, map[string]any{"i": 0}); hit {
			t.Errorf("op %s should miss after Invalidate", op)
		}
	}
}

func TestCache_TTLExpiry(t *testing.T) {
	c, dir := newCache(t, 10*time.Millisecond)
	p := filepath.Join(dir, "a.go")
	writeFile(t, p, "package a\n")

	if err := c.Put(p, "documentSymbol", nil, []byte("body")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	// Immediately: hit.
	if _, hit, _ := c.Get(p, "documentSymbol", nil); !hit {
		t.Fatalf("expected immediate hit")
	}
	// Past TTL: miss even though mtime is unchanged.
	time.Sleep(20 * time.Millisecond)
	if _, hit, _ := c.Get(p, "documentSymbol", nil); hit {
		t.Fatalf("expected miss after TTL expiry")
	}
}

func TestCache_GetOnMissingFileMisses(t *testing.T) {
	c, _ := newCache(t, 0)
	if _, hit, err := c.Get("/does/not/exist/x.go", "documentSymbol", nil); err != nil || hit {
		t.Fatalf("missing-file Get: hit=%v err=%v; want miss/no-error", hit, err)
	}
}

func TestCache_PutOnMissingFileDrops(t *testing.T) {
	c, _ := newCache(t, 0)
	// No error; Put silently drops because there is no mtime to key on.
	if err := c.Put("/does/not/exist/x.go", "op", nil, []byte("body")); err != nil {
		t.Fatalf("Put on missing file should be a silent no-op, got %v", err)
	}
	if s := c.Snapshot(); s.Puts != 0 {
		t.Errorf("Put counter incremented on dropped insert; got %d", s.Puts)
	}
}

func TestCache_ArgsHashStability(t *testing.T) {
	// Same args, different map iteration orders: encoding/json sorts
	// keys, so the hash is stable.
	a := map[string]any{"name": "Hello", "kind": 1}
	b := map[string]any{"kind": 1, "name": "Hello"}
	ha, _ := hashArgs(a)
	hb, _ := hashArgs(b)
	if ha != hb {
		t.Errorf("hashArgs unstable: %q != %q", ha, hb)
	}
	// Different args produce different hashes.
	c := map[string]any{"name": "Hello", "kind": 2}
	hc, _ := hashArgs(c)
	if ha == hc {
		t.Errorf("hashArgs collision: %q", ha)
	}
}

func TestStats_HitRatio(t *testing.T) {
	s := Stats{Hits: 3, Misses: 1}
	if got, want := s.HitRatio(), 0.75; got != want {
		t.Errorf("HitRatio=%v want %v", got, want)
	}
	if (Stats{}).HitRatio() != 0 {
		t.Errorf("empty Stats.HitRatio should be 0")
	}
}

// TestCache_InvalidateAfterWrite simulates the daemon's write/edit
// invalidation sink: a Put is followed by a fresh write to disk and
// then an Invalidate(file) call. The next Get is a miss as both layers
// (mtime + explicit Invalidate) would catch.
func TestCache_InvalidateAfterWrite(t *testing.T) {
	c, dir := newCache(t, 0)
	p := filepath.Join(dir, "a.go")
	writeFile(t, p, "package a\n")

	if err := c.Put(p, "documentSymbol", nil, []byte("body")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	// Simulate a write through ash write — touches mtime AND triggers
	// the sink's Invalidate call.
	time.Sleep(5 * time.Millisecond)
	writeFile(t, p, "package a\n// edited\n")
	n, err := c.Invalidate(p)
	if err != nil {
		t.Fatalf("Invalidate: %v", err)
	}
	if n != 1 {
		t.Errorf("Invalidate after write removed %d rows; want 1", n)
	}
	if _, hit, _ := c.Get(p, "documentSymbol", nil); hit {
		t.Fatalf("Get after write+Invalidate should miss")
	}
}

// TestWorkspace_GetMissOnFresh covers cold-start: GetWorkspace before
// any Put returns a miss because the watermark is unseeded.
func TestWorkspace_GetMissOnFresh(t *testing.T) {
	c, _ := newCache(t, 0)
	got, hit, err := c.GetWorkspace("refs", map[string]any{"symbol": "X"})
	if err != nil {
		t.Fatalf("GetWorkspace: %v", err)
	}
	if hit {
		t.Fatalf("cold cache should miss; got %s", got)
	}
	if s := c.Snapshot(); s.WorkspaceMisses != 1 || s.WorkspaceHits != 0 {
		t.Errorf("counters: %+v want misses=1 hits=0", s)
	}
}

// TestWorkspace_PutThenGetHit covers the headline case: a Put seeds the
// watermark and a subsequent Get with the same args is a hit.
func TestWorkspace_PutThenGetHit(t *testing.T) {
	c, _ := newCache(t, 0)
	resp := []byte(`{"rows":["a","b"]}`)
	if err := c.PutWorkspace("refs", map[string]any{"symbol": "X"}, resp); err != nil {
		t.Fatalf("PutWorkspace: %v", err)
	}
	if c.WorkspaceMtime() == 0 {
		t.Errorf("Put should have seeded the workspace mtime")
	}
	got, hit, err := c.GetWorkspace("refs", map[string]any{"symbol": "X"})
	if err != nil {
		t.Fatalf("GetWorkspace: %v", err)
	}
	if !hit {
		t.Fatalf("expected hit after Put")
	}
	if string(got) != string(resp) {
		t.Errorf("response mismatch: got %s want %s", got, resp)
	}
	if s := c.Snapshot(); s.WorkspaceHits != 1 || s.WorkspacePuts != 1 {
		t.Errorf("counters: %+v want WorkspaceHits=1 WorkspacePuts=1", s)
	}
}

// TestWorkspace_BumpInvalidates is the ASH-157 correctness story: a
// write/edit-side BumpWorkspace call advances the watermark and the
// next Get misses on a row that was a hit moments before.
func TestWorkspace_BumpInvalidates(t *testing.T) {
	c, _ := newCache(t, 0)
	resp := []byte(`{"rows":["a"]}`)
	if err := c.PutWorkspace("refs", map[string]any{"symbol": "X"}, resp); err != nil {
		t.Fatalf("PutWorkspace: %v", err)
	}
	// Sleep so the next time.Now is strictly greater than the seeded mtime.
	time.Sleep(2 * time.Millisecond)
	c.BumpWorkspace("/tmp/foo.go")
	if _, hit, _ := c.GetWorkspace("refs", map[string]any{"symbol": "X"}); hit {
		t.Fatalf("Get after Bump should miss (watermark advanced)")
	}
}

// TestWorkspace_BumpIgnoresNonGo confirms the bump is gated on file
// extension: a write to README.md (or any non-Go file) leaves the
// workspace watermark untouched and existing hits stay valid.
func TestWorkspace_BumpIgnoresNonGo(t *testing.T) {
	c, _ := newCache(t, 0)
	if err := c.PutWorkspace("refs", nil, []byte("body")); err != nil {
		t.Fatalf("PutWorkspace: %v", err)
	}
	before := c.WorkspaceMtime()
	c.BumpWorkspace("/tmp/README.md")
	c.BumpWorkspace("/tmp/data.json")
	c.BumpWorkspace("/tmp/Makefile")
	if c.WorkspaceMtime() != before {
		t.Errorf("non-Go bumps shifted watermark: before=%d after=%d", before, c.WorkspaceMtime())
	}
	if _, hit, _ := c.GetWorkspace("refs", nil); !hit {
		t.Errorf("Get after non-Go bumps should still hit")
	}
}

func TestWorkspace_TTL(t *testing.T) {
	c, _ := newCache(t, 10*time.Millisecond)
	if err := c.PutWorkspace("refs", nil, []byte("body")); err != nil {
		t.Fatalf("PutWorkspace: %v", err)
	}
	if _, hit, _ := c.GetWorkspace("refs", nil); !hit {
		t.Fatalf("immediate hit expected")
	}
	time.Sleep(20 * time.Millisecond)
	if _, hit, _ := c.GetWorkspace("refs", nil); hit {
		t.Fatalf("hit after TTL expiry; want miss")
	}
}

func TestWorkspace_NilSafety(t *testing.T) {
	var c *Cache
	if _, hit, err := c.GetWorkspace("refs", nil); hit || err != nil {
		t.Errorf("nil cache GetWorkspace: hit=%v err=%v", hit, err)
	}
	if err := c.PutWorkspace("refs", nil, []byte("body")); err != nil {
		t.Errorf("nil cache PutWorkspace: %v", err)
	}
	c.BumpWorkspace("/x.go")
	if c.WorkspaceMtime() != 0 {
		t.Errorf("nil cache mtime: got %d want 0", c.WorkspaceMtime())
	}
}

func TestWorkspace_HitRatio(t *testing.T) {
	s := Stats{WorkspaceHits: 3, WorkspaceMisses: 1}
	if got, want := s.WorkspaceHitRatio(), 0.75; got != want {
		t.Errorf("WorkspaceHitRatio=%v want %v", got, want)
	}
	if (Stats{}).WorkspaceHitRatio() != 0 {
		t.Errorf("empty Stats.WorkspaceHitRatio should be 0")
	}
}

func TestCache_NilSafety(t *testing.T) {
	var c *Cache
	if _, hit, _ := c.Get("/x", "op", nil); hit {
		t.Errorf("nil cache Get should miss")
	}
	if err := c.Put("/x", "op", nil, []byte("body")); err != nil {
		t.Errorf("nil cache Put should be no-op")
	}
	if n, _ := c.Invalidate("/x"); n != 0 {
		t.Errorf("nil cache Invalidate should be no-op")
	}
	if c.Snapshot() != (Stats{}) {
		t.Errorf("nil cache Snapshot should be zero Stats")
	}
}
