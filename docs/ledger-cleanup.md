# ASH-34: Ledger Cleanup / Retention

## Context

The SQLite ledger at `.ash/ledger.db` grows without bound — every `ash` invocation appends a row to `calls` and nothing ever prunes old rows. With heavy daily use a project ledger could balloon to 100 MB+ over months. The fix is automatic age-based (and optionally row-count-based) cleanup that runs at daemon startup, with sensible defaults so new installs are protected without requiring any config.

## Approach

Age-based deletion at daemon startup. No new verb, no background goroutine — just one synchronous prune pass before the accept loop opens. `PRAGMA optimize` runs after any deletion; full `VACUUM` is opt-in (slow, rewrites the whole file).

**Default: 30-day retention.** Fresh installs get cleanup automatically; users who want the old unbounded behavior set `max_age = "0s"`.

## Files to change

| File | What changes |
|---|---|
| `internal/config/config.go` | Add `LedgerConfig` struct; add `Ledger LedgerConfig` to `Config`; update `Defaults()` |
| `internal/ledger/ledger.go` | Add `CleanupCfg`, `CleanupResult`, `(*Ledger).Cleanup()` |
| `cmd/ashd/main.go` | Call `led.Cleanup(...)` after `ledger.Open()`; log result |
| `ash.toml.example` | Add commented `[ledger]` section |
| `docs/configuration.md` | Document `[ledger]` table |
| `internal/ledger/ledger_test.go` (new) | Unit tests for `Cleanup` |
| `internal/config/config_test.go` | Extend layering tests to cover `LedgerConfig` |

## Step 1 — `internal/config/config.go`

Add `LedgerConfig` following the same pattern as `DaemonConfig`:

```go
type LedgerConfig struct {
    // MaxAge is how long to keep call rows. 0 = no age limit.
    MaxAge Duration `toml:"max_age"`
    // MaxRows caps total rows kept after age-based cleanup. 0 = no limit.
    MaxRows int `toml:"max_rows"`
    // Vacuum runs PRAGMA vacuum after cleanup. Default false (slow; opt-in).
    Vacuum bool `toml:"vacuum"`
}
```

Add to `Config`:
```go
type Config struct {
    Daemon  DaemonConfig  `toml:"daemon"`
    Jail    JailConfig    `toml:"jail"`
    Git     GitConfig     `toml:"git"`
    Ledger  LedgerConfig  `toml:"ledger"`
}
```

Update `Defaults()`:
```go
Ledger: LedgerConfig{
    MaxAge: Duration(30 * 24 * time.Hour), // 30 days
},
```

## Step 2 — `internal/ledger/ledger.go`

Add types and method:

```go
type CleanupCfg struct {
    MaxAge  time.Duration // 0 = no age limit
    MaxRows int           // 0 = no row limit
    Vacuum  bool
}

type CleanupResult struct {
    DeletedCalls    int64
    DeletedSessions int64
    Vacuumed        bool
}

func (l *Ledger) Cleanup(cfg CleanupCfg) (*CleanupResult, error) {
    res := &CleanupResult{}

    // 1. Age-based deletion.
    if cfg.MaxAge > 0 {
        cutoff := time.Now().Add(-cfg.MaxAge).UnixNano()
        r, err := l.db.Exec(`DELETE FROM calls WHERE ts < ?`, cutoff)
        if err != nil { return nil, err }
        res.DeletedCalls, _ = r.RowsAffected()
    }

    // 2. Row-count cap (applied after age cleanup).
    if cfg.MaxRows > 0 {
        var count int
        l.db.QueryRow(`SELECT COUNT(*) FROM calls`).Scan(&count)
        if excess := count - cfg.MaxRows; excess > 0 {
            r, err := l.db.Exec(
                `DELETE FROM calls WHERE id IN (SELECT id FROM calls ORDER BY id ASC LIMIT ?)`,
                excess)
            if err != nil { return nil, err }
            n, _ := r.RowsAffected()
            res.DeletedCalls += n
        }
    }

    // 3. Orphaned sessions (exclude current session — it has no calls yet).
    r, err := l.db.Exec(
        `DELETE FROM sessions WHERE id NOT IN (SELECT DISTINCT session_id FROM calls) AND id != ?`,
        l.sessionID)
    if err != nil { return nil, err }
    res.DeletedSessions, _ = r.RowsAffected()

    // 4. PRAGMA optimize (cheap; updates query planner stats).
    l.db.Exec(`PRAGMA optimize`)

    // 5. Optional full vacuum.
    if cfg.Vacuum {
        if _, err := l.db.Exec(`VACUUM`); err != nil {
            return nil, err
        }
        res.Vacuumed = true
    }

    return res, nil
}
```

**Key subtlety:** The current session row is inserted in `Open()` before `Cleanup()` runs, but has zero calls. Orphan cleanup must exclude it via `AND id != ?` or it would delete the current session and break the FK constraint when the first call comes in.

`OpenReadOnly` never calls `Cleanup` — `sessionID` is empty on read-only handles and cleanup is a write operation.

## Step 3 — `cmd/ashd/main.go`

After the existing `ledger.Open(...)` call:

```go
led, err := ledger.Open(session.LedgerPath(rootFlag), rootFlag, "ashd/v0.1")
if err != nil {
    log.Fatalf("ashd: ledger: %v", err)
}
defer led.Close()

cleanCfg := ledger.CleanupCfg{
    MaxAge:  cfg.Ledger.MaxAge.AsDuration(),
    MaxRows: cfg.Ledger.MaxRows,
    Vacuum:  cfg.Ledger.Vacuum,
}
if cr, err := led.Cleanup(cleanCfg); err != nil {
    log.Printf("ashd: ledger cleanup: %v", err) // non-fatal; don't abort startup
} else if cr.DeletedCalls > 0 || cr.DeletedSessions > 0 {
    log.Printf("ashd: ledger cleanup: deleted %d calls, %d sessions", cr.DeletedCalls, cr.DeletedSessions)
}
```

Cleanup is non-fatal — a cleanup error does not prevent the daemon from starting.

## Step 4 — `ash.toml.example`

Add a commented `[ledger]` block after the `[git]` block:

```toml
# [ledger]
# # How long to keep call rows in the ledger. Default "720h" (30 days).
# # Set to "0s" to disable age-based cleanup entirely (unbounded growth).
# max_age = "720h"
#
# # Cap total rows after age cleanup. 0 (default) = no row limit.
# max_rows = 0
#
# # Run PRAGMA VACUUM after cleanup (rewrites the DB file; slow on large
# # ledgers). Default false — PRAGMA optimize runs instead, which is
# # cheap and sufficient for routine maintenance.
# vacuum = false
```

## Step 5 — `docs/configuration.md`

Add a `### [ledger]` section under the Schema heading, documenting:
- `max_age`: duration string, default 30 days, `"0s"` to disable
- `max_rows`: integer, default 0 (no cap), applied after age cleanup
- `vacuum`: bool, default false, VACUUM vs optimize tradeoff

## Step 6 — Tests

**`internal/ledger/ledger_test.go` (new file):**
- Open a test ledger in `t.TempDir()`.
- Insert two sessions with calls timestamped 40 days ago (old) and 10 days ago (recent).
- Call `Cleanup(CleanupCfg{MaxAge: 30*24*time.Hour})`.
- Assert old session calls deleted, recent session calls retained.
- Assert old session row deleted, recent session row retained, current session not deleted.
- Separate sub-test: insert 200 calls, `Cleanup(CleanupCfg{MaxRows: 50})`, assert 150 deleted.
- Separate sub-test: `Cleanup(CleanupCfg{Vacuum: true})`, assert no error.

**`internal/config/config_test.go`:**
- Verify `Defaults()` produces `MaxAge == 30*24*time.Hour`.
- Extend layering test: write `[ledger]\nmax_age = "168h"` to project `ash.toml`, load, verify `MaxAge == 7*24*time.Hour` overrides the 30-day default.

## Verification

```sh
go build -o bin/ash ./cmd/ash && go build -o bin/ashd ./cmd/ashd

# Run new + existing tests
bin/ash test --packages internal/ledger,internal/config

# Confirm no-op cleanup log at startup (fresh ledger)
bin/ash stop
bin/ash git --op status        # auto-starts daemon
grep "ledger cleanup" .ash/ashd.log   # should be absent (nothing to delete yet)

# Smoke-test defaults still work
bin/ash report                 # current session summary, unchanged behavior
```
