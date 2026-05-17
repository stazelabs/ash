// Package cache is the SQLite-backed cache for LSP responses (ASH-137).
//
// The cache lives at .ash/lang-cache.db — separate from .ash/ledger.db
// so it can be blown away without losing call history. Each row keys on
// (file_abs, mtime_ns, op, args_hash) so a call is a hit iff the file
// has not been touched on disk since the response was cached.
//
// Invalidation has two layers:
//
//   - mtime-keying is the canonical correctness story: a Get whose
//     mtime_ns disagrees with the cached row misses, and the row is
//     left in place (it will be reaped by the next file-level
//     Invalidate or by TTL pruning).
//   - The write/edit hook calls Invalidate(file) to wipe every row for
//     a touched file. This is belt-and-suspenders: mtime alone catches
//     the same case, but Invalidate keeps the table from growing
//     unboundedly across repeated edits of the same file.
//
// An optional TTL ([lsp].cache_ttl in ash.toml) is a safety net for
// cases the mtime path can't see — clock skew, files written by tools
// outside ash. Zero disables it.
package cache

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"
)

const schemaSQL = `
CREATE TABLE IF NOT EXISTS lang_cache (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	file          TEXT NOT NULL,
	mtime_ns      INTEGER NOT NULL,
	op            TEXT NOT NULL,
	args_hash     TEXT NOT NULL,
	response_json BLOB NOT NULL,
	ts            INTEGER NOT NULL,
	UNIQUE(file, mtime_ns, op, args_hash)
);
CREATE INDEX IF NOT EXISTS idx_lang_cache_file ON lang_cache(file);
CREATE INDEX IF NOT EXISTS idx_lang_cache_ts ON lang_cache(ts);
`

// Cache is the public handle. It is safe for concurrent use by many
// goroutines. The zero value is not useful; construct one via Open.
type Cache struct {
	mu  sync.Mutex
	db  *sql.DB
	ttl time.Duration

	hits        atomic.Int64
	misses      atomic.Int64
	puts        atomic.Int64
	invalidated atomic.Int64

	// ASH-157: workspace-scope cache state. wsMtime is the monotonic
	// watermark for workspace-keyed rows; every successful Bump moves
	// it forward, and workspace Gets miss when their cached row's
	// mtime_ns disagrees with the current value. Zero means "no
	// workspace state observed yet" — the first PutWorkspace seeds it.
	wsMtime  atomic.Int64
	wsHits   atomic.Int64
	wsMisses atomic.Int64
	wsPuts   atomic.Int64
}

// workspaceFileKey is the sentinel string stored in lang_cache.file for
// workspace-scoped rows. Single underscore-bracketed value so it cannot
// collide with a real filesystem path.
const workspaceFileKey = "<workspace>"

// Options configures Open.
type Options struct {
	// Path is the SQLite file path. The parent directory must exist.
	Path string
	// TTL is the soft expiry for cached rows. Zero means "never expire
	// on time alone" — only mtime mismatch (and explicit Invalidate)
	// retire rows.
	TTL time.Duration
}

// Open returns a Cache backed by an SQLite database at opts.Path,
// creating the schema if it does not exist. Concurrent Opens on the
// same path produce independent *Cache values pointing at the same
// underlying database — SQLite handles the locking; the package does
// not enforce single-process ownership because ashd is the single
// writer in practice.
func Open(opts Options) (*Cache, error) {
	db, err := sql.Open("sqlite", opts.Path)
	if err != nil {
		return nil, fmt.Errorf("lang-cache open %s: %w", opts.Path, err)
	}
	// Pragmas chosen to match internal/ledger: WAL for concurrent
	// readers, NORMAL synchronous for cheap commits, foreign keys off
	// (no FKs in this schema).
	if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("lang-cache pragma WAL: %w", err)
	}
	if _, err := db.Exec(`PRAGMA synchronous=NORMAL`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("lang-cache pragma synchronous: %w", err)
	}
	if _, err := db.Exec(schemaSQL); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("lang-cache schema: %w", err)
	}
	return &Cache{db: db, ttl: opts.TTL}, nil
}

// Close releases the database handle. Safe to call multiple times; only
// the first call performs work.
func (c *Cache) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.db == nil {
		return nil
	}
	err := c.db.Close()
	c.db = nil
	return err
}

// Get attempts to retrieve a cached response for (file, op, args). The
// file must exist on disk — its current mtime is part of the key.
// Returns (response, true, nil) on hit; (nil, false, nil) on miss
// (including "file does not exist", "no row", "row exists but mtime
// disagrees", and "row exists but TTL has expired"). Returns a non-nil
// error only for SQLite or OS-level failures.
//
// Hit-and-miss counters update atomically; lookup latency is dominated
// by the one-row index probe.
func (c *Cache) Get(file, op string, args any) ([]byte, bool, error) {
	if c == nil {
		return nil, false, nil
	}
	if c.db == nil {
		c.misses.Add(1)
		return nil, false, nil
	}
	mtime, ok, err := statMtimeNs(file)
	if err != nil {
		c.misses.Add(1)
		return nil, false, err
	}
	if !ok {
		c.misses.Add(1)
		return nil, false, nil
	}
	hash, err := hashArgs(args)
	if err != nil {
		c.misses.Add(1)
		return nil, false, err
	}
	var (
		resp []byte
		ts   int64
	)
	row := c.db.QueryRow(
		`SELECT response_json, ts FROM lang_cache WHERE file=? AND mtime_ns=? AND op=? AND args_hash=? LIMIT 1`,
		file, mtime, op, hash,
	)
	if err := row.Scan(&resp, &ts); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			c.misses.Add(1)
			return nil, false, nil
		}
		c.misses.Add(1)
		return nil, false, err
	}
	if c.ttl > 0 {
		age := time.Since(time.Unix(0, ts))
		if age > c.ttl {
			c.misses.Add(1)
			return nil, false, nil
		}
	}
	c.hits.Add(1)
	return resp, true, nil
}

// Put writes a cached response for (file, op, args) keyed at the file's
// current mtime. Replaces any existing row with the same key. A Put on
// a file that no longer exists is silently dropped — the response
// cannot be keyed on a meaningful mtime, and a stale row would be
// guaranteed to miss anyway.
func (c *Cache) Put(file, op string, args any, response []byte) error {
	if c == nil || c.db == nil {
		return nil
	}
	mtime, ok, err := statMtimeNs(file)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	hash, err := hashArgs(args)
	if err != nil {
		return err
	}
	_, err = c.db.Exec(
		`INSERT OR REPLACE INTO lang_cache (file, mtime_ns, op, args_hash, response_json, ts) VALUES (?,?,?,?,?,?)`,
		file, mtime, op, hash, response, time.Now().UnixNano(),
	)
	if err != nil {
		return err
	}
	c.puts.Add(1)
	return nil
}

// Invalidate deletes every cached row for the given file. Returns the
// number of rows removed. A no-op when the cache is nil or closed.
//
// Called from the write/edit success path so a touched file's stale
// responses are reclaimed promptly instead of waiting for mtime
// mismatch on the next Get.
func (c *Cache) Invalidate(file string) (int, error) {
	if c == nil || c.db == nil {
		return 0, nil
	}
	res, err := c.db.Exec(`DELETE FROM lang_cache WHERE file=?`, file)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		c.invalidated.Add(n)
	}
	return int(n), nil
}

// GetWorkspace looks up a cached workspace-scoped response keyed on
// (current_workspace_mtime, op, args_hash). Returns (response, true, nil)
// on hit; (nil, false, nil) on miss — including the cold-start case
// where the workspace watermark has not been seeded yet.
//
// Counter discipline: WorkspaceHits and WorkspaceMisses are independent
// of the per-file Hits/Misses, so report-side surfacing can show both.
func (c *Cache) GetWorkspace(op string, args any) ([]byte, bool, error) {
	if c == nil {
		return nil, false, nil
	}
	if c.db == nil {
		c.wsMisses.Add(1)
		return nil, false, nil
	}
	mtime := c.wsMtime.Load()
	if mtime == 0 {
		c.wsMisses.Add(1)
		return nil, false, nil
	}
	hash, err := hashArgs(args)
	if err != nil {
		c.wsMisses.Add(1)
		return nil, false, err
	}
	var (
		resp []byte
		ts   int64
	)
	row := c.db.QueryRow(
		`SELECT response_json, ts FROM lang_cache WHERE file=? AND mtime_ns=? AND op=? AND args_hash=? LIMIT 1`,
		workspaceFileKey, mtime, op, hash,
	)
	if err := row.Scan(&resp, &ts); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			c.wsMisses.Add(1)
			return nil, false, nil
		}
		c.wsMisses.Add(1)
		return nil, false, err
	}
	if c.ttl > 0 && time.Since(time.Unix(0, ts)) > c.ttl {
		c.wsMisses.Add(1)
		return nil, false, nil
	}
	c.wsHits.Add(1)
	return resp, true, nil
}

// PutWorkspace stores a workspace-scoped response. The first call after
// a daemon start (or after a BumpWorkspace) seeds the watermark using
// time.Now(); subsequent calls reuse the existing watermark so rows
// keyed on the same mtime can hit. A response stored before the next
// Bump survives; afterwards the row is unreachable (next Get sees a
// fresh mtime and misses).
func (c *Cache) PutWorkspace(op string, args any, response []byte) error {
	if c == nil || c.db == nil {
		return nil
	}
	mtime := c.wsMtime.Load()
	if mtime == 0 {
		// Seed the watermark so future Gets within the same workspace
		// state can hit. CAS-protected so concurrent Puts agree on one
		// seed value.
		fresh := time.Now().UnixNano()
		if c.wsMtime.CompareAndSwap(0, fresh) {
			mtime = fresh
		} else {
			mtime = c.wsMtime.Load()
		}
	}
	hash, err := hashArgs(args)
	if err != nil {
		return err
	}
	_, err = c.db.Exec(
		`INSERT OR REPLACE INTO lang_cache (file, mtime_ns, op, args_hash, response_json, ts) VALUES (?,?,?,?,?,?)`,
		workspaceFileKey, mtime, op, hash, response, time.Now().UnixNano(),
	)
	if err != nil {
		return err
	}
	c.wsPuts.Add(1)
	return nil
}

// BumpWorkspace advances the workspace watermark when path is a Go-
// relevant source file. Workspace-scoped rows keyed on the prior mtime
// become unreachable on the next Get (mtime mismatch) and will be
// reaped by a future Invalidate pass or by accumulated row-count
// cleanup. The bump is best-effort: passing a non-Go path is a no-op.
//
// Called from cmd/ashd/main.go's write/edit sink alongside the
// per-file Invalidate call. The two together cover both the per-file
// (outline) and workspace-scoped (def/refs/callers/impl) cases.
func (c *Cache) BumpWorkspace(path string) {
	if c == nil {
		return
	}
	if !goplsRelevantPath(path) {
		return
	}
	now := time.Now().UnixNano()
	// Monotonic forward-move: never let a slow caller stamp a value
	// older than what's already there.
	for {
		prev := c.wsMtime.Load()
		if now <= prev {
			return
		}
		if c.wsMtime.CompareAndSwap(prev, now) {
			return
		}
	}
}

// WorkspaceMtime returns the current watermark. Useful for tests and
// diagnostics; the production code path uses Get/Put/Bump and never
// reads the value directly.
func (c *Cache) WorkspaceMtime() int64 {
	if c == nil {
		return 0
	}
	return c.wsMtime.Load()
}

// goplsRelevantPath reports whether path's extension is one gopls
// indexes. Mirrors the same predicate in internal/lsp without taking
// a dependency on that package.
func goplsRelevantPath(path string) bool {
	// Simple suffix match — filepath.Ext + strings.ToLower would be
	// equivalent but slower for the hot sink path.
	for _, ext := range []string{".go", ".mod", ".sum"} {
		if len(path) >= len(ext) && path[len(path)-len(ext):] == ext {
			return true
		}
	}
	return false
}

// Stats is a snapshot of the cache's lifetime counters. Cheap; reads
// atomic integers without acquiring locks.
type Stats struct {
	Hits        int64
	Misses      int64
	Puts        int64
	Invalidated int64

	// ASH-157: workspace-scope counters. Independent of the per-file
	// trio above so a report-side breakdown can show both buckets.
	WorkspaceHits   int64
	WorkspaceMisses int64
	WorkspacePuts   int64
}

// Snapshot returns the current Stats. Counters are monotonic across the
// lifetime of the Cache (Close does not reset them).
func (c *Cache) Snapshot() Stats {
	if c == nil {
		return Stats{}
	}
	return Stats{
		Hits:            c.hits.Load(),
		Misses:          c.misses.Load(),
		Puts:            c.puts.Load(),
		Invalidated:     c.invalidated.Load(),
		WorkspaceHits:   c.wsHits.Load(),
		WorkspaceMisses: c.wsMisses.Load(),
		WorkspacePuts:   c.wsPuts.Load(),
	}
}

// HitRatio is per-file hits / (hits + misses), or 0 when neither has
// fired yet. Workspace hits live in WorkspaceHitRatio.
func (s Stats) HitRatio() float64 {
	total := s.Hits + s.Misses
	if total == 0 {
		return 0
	}
	return float64(s.Hits) / float64(total)
}

// WorkspaceHitRatio is workspace-scoped hits / (hits + misses), or 0
// when neither has fired yet.
func (s Stats) WorkspaceHitRatio() float64 {
	total := s.WorkspaceHits + s.WorkspaceMisses
	if total == 0 {
		return 0
	}
	return float64(s.WorkspaceHits) / float64(total)
}

// ----------------------------------------------------------------------
// helpers

// hashArgs produces a stable 16-byte hex digest of an args value. The
// canonical form is JSON: maps with sorted keys per encoding/json's
// default, slices in their natural order. nil and empty maps hash
// identically so the cache key is robust to representational drift.
func hashArgs(args any) (string, error) {
	if args == nil {
		return "00000000000000000000000000000000", nil
	}
	body, err := json.Marshal(args)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:16]), nil
}

// statMtimeNs returns the file's modification time in nanoseconds. The
// second return value is false when the file does not exist; the
// caller treats that as a miss without surfacing an error.
func statMtimeNs(path string) (int64, bool, error) {
	st, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, false, nil
		}
		return 0, false, err
	}
	return st.ModTime().UnixNano(), true, nil
}
