// Package ledger persists per-call instrumentation to a per-project SQLite
// database. Writes are synchronous; latency cost is sub-millisecond and we
// optimize for tokens, not microseconds.
package ledger

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const schemaVersion = "1"

const schemaSQL = `
CREATE TABLE IF NOT EXISTS meta (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS sessions (
	id TEXT PRIMARY KEY,
	started_at INTEGER NOT NULL,
	project_root TEXT,
	client_info TEXT
);
CREATE TABLE IF NOT EXISTS calls (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	session_id TEXT NOT NULL REFERENCES sessions(id),
	request_id INTEGER NOT NULL,
	ts INTEGER NOT NULL,
	verb TEXT NOT NULL,
	args_msgpack BLOB,
	ok INTEGER NOT NULL,
	err_code TEXT,
	err_msg TEXT,
	latency_parse_us INTEGER NOT NULL,
	latency_exec_us INTEGER NOT NULL,
	latency_serialize_us INTEGER NOT NULL,
	tokens_in INTEGER,
	tokens_out INTEGER,
	tokens_out_no_prefix INTEGER NOT NULL DEFAULT 0,
	tokens_method TEXT,
	bytes_in INTEGER NOT NULL,
	bytes_out INTEGER NOT NULL,
	truncated INTEGER NOT NULL DEFAULT 0,
	walk_us INTEGER NOT NULL DEFAULT 0,
	io_us INTEGER NOT NULL DEFAULT 0,
	regex_us INTEGER NOT NULL DEFAULT 0,
	regex_compile_us INTEGER NOT NULL DEFAULT 0,
	latency_dispatch_us INTEGER NOT NULL DEFAULT 0,
	streaming INTEGER NOT NULL DEFAULT 0,
	chunks_out INTEGER NOT NULL DEFAULT 0,
	time_to_first_chunk_us INTEGER NOT NULL DEFAULT 0,
	tokens_out_emit INTEGER NOT NULL DEFAULT 0,
	bytes_out_emit INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_calls_session ON calls(session_id);
CREATE INDEX IF NOT EXISTS idx_calls_verb ON calls(verb);
CREATE INDEX IF NOT EXISTS idx_calls_ts ON calls(ts);

CREATE TABLE IF NOT EXISTS bench_runs (
	id                  INTEGER PRIMARY KEY AUTOINCREMENT,
	run_uuid            TEXT NOT NULL UNIQUE,
	ts                  INTEGER NOT NULL,
	ash_version         TEXT NOT NULL,
	ash_commit_sha      TEXT,
	case_set_version    TEXT NOT NULL,
	repo_sha            TEXT,
	repo_dirty          INTEGER NOT NULL DEFAULT 0,
	platform            TEXT,
	cpu_count           INTEGER NOT NULL DEFAULT 0,
	daemon_uptime_us    INTEGER NOT NULL DEFAULT 0,
	repeat_n            INTEGER NOT NULL DEFAULT 1,
	warmup_n            INTEGER NOT NULL DEFAULT 0,
	notes               TEXT
);
CREATE INDEX IF NOT EXISTS idx_bench_runs_ts ON bench_runs(ts);

CREATE TABLE IF NOT EXISTS bench_case_results (
	id                       INTEGER PRIMARY KEY AUTOINCREMENT,
	run_id                   INTEGER NOT NULL REFERENCES bench_runs(id) ON DELETE CASCADE,
	case_name                TEXT NOT NULL,
	verb                     TEXT NOT NULL,
	ash_tokens               INTEGER NOT NULL,
	bash_tokens              INTEGER NOT NULL,
	ash_bytes                INTEGER NOT NULL,
	bash_bytes               INTEGER NOT NULL,
	ash_latency_us_p50       INTEGER NOT NULL,
	ash_latency_us_min       INTEGER NOT NULL,
	bash_latency_us_p50      INTEGER NOT NULL,
	bash_latency_us_min      INTEGER NOT NULL,
	ash_ok                   INTEGER NOT NULL,
	ash_err                  TEXT,
	bash_exit                INTEGER NOT NULL DEFAULT 0,
	bash_run_err             TEXT,
	ash_truncated            INTEGER NOT NULL DEFAULT 0,
	bash_truncated           INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_bench_case_results_run  ON bench_case_results(run_id);
CREATE INDEX IF NOT EXISTS idx_bench_case_results_case ON bench_case_results(case_name);
`

type Ledger struct {
	db        *sql.DB
	sessionID string
	counter   *Counter
}

func Open(path, projectRoot, clientInfo string) (*Ledger, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schemaSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("ledger: schema: %w", err)
	}
	if _, err := db.Exec(`INSERT OR IGNORE INTO meta (key, value) VALUES ('schema_version', ?)`, schemaVersion); err != nil {
		db.Close()
		return nil, fmt.Errorf("ledger: schema version: %w", err)
	}
	// Idempotent migration: pre-platform schemas stored os.Hostname() in
	// bench_runs.hostname. Rename to `platform` and scrub historic values
	// (they were machine-identifying). RENAME COLUMN errors silently on
	// fresh DBs where `platform` already exists from schemaSQL.
	_, _ = db.Exec(`ALTER TABLE bench_runs RENAME COLUMN hostname TO platform`)
	// ASH-71: tokens_out_no_prefix captures the tokenized response with
	// known path prefixes stripped, so reports can quote the path-prefix
	// tax. Errors silently on fresh DBs where the column already exists.
	_, _ = db.Exec(`ALTER TABLE calls ADD COLUMN tokens_out_no_prefix INTEGER NOT NULL DEFAULT 0`)
	// ASH-106: streaming columns. Same idempotent-ALTER pattern — fresh
	// DBs already have them from schemaSQL; existing DBs need the migration.
	// streaming = 1 when the response went out as Chunk frames + Final.
	// chunks_out counts Chunk frames written (excludes Final).
	// time_to_first_chunk_us measures the latency from request decode to
	// the first Chunk flush — the headline streaming metric.
	_, _ = db.Exec(`ALTER TABLE calls ADD COLUMN streaming INTEGER NOT NULL DEFAULT 0`)
	_, _ = db.Exec(`ALTER TABLE calls ADD COLUMN chunks_out INTEGER NOT NULL DEFAULT 0`)
	_, _ = db.Exec(`ALTER TABLE calls ADD COLUMN time_to_first_chunk_us INTEGER NOT NULL DEFAULT 0`)
	// ASH-123: MCP-transport emit accounting. tokens_out (above) counts
	// the daemon-pretty-rendered text — what a CLI client sees. When the
	// request arrives over `ashmcp` (Request.Transport == "mcp"), the
	// daemon also tokenizes the JSON envelope ashmcp will re-wrap the
	// response into before emitting as TextContent, and records those
	// numbers here. CLI rows leave both at zero; report/metrics surface
	// them only when non-zero so existing pretty output is unchanged for
	// CLI-only sessions.
	_, _ = db.Exec(`ALTER TABLE calls ADD COLUMN tokens_out_emit INTEGER NOT NULL DEFAULT 0`)
	_, _ = db.Exec(`ALTER TABLE calls ADD COLUMN bytes_out_emit INTEGER NOT NULL DEFAULT 0`)
	var migDone string
	_ = db.QueryRow(`SELECT value FROM meta WHERE key='mig_bench_platform'`).Scan(&migDone)
	if migDone != "1" {
		_, _ = db.Exec(`UPDATE bench_runs SET platform = NULL`)
		_, _ = db.Exec(`INSERT OR REPLACE INTO meta (key, value) VALUES ('mig_bench_platform', '1')`)
	}
	sid := newSessionID()
	if _, err := db.Exec(
		`INSERT INTO sessions (id, started_at, project_root, client_info) VALUES (?, ?, ?, ?)`,
		sid, time.Now().UnixNano(), projectRoot, clientInfo,
	); err != nil {
		db.Close()
		return nil, err
	}
	counter, err := NewCounter()
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("ledger: tokenizer: %w", err)
	}
	return &Ledger{db: db, sessionID: sid, counter: counter}, nil
}

func (l *Ledger) SessionID() string { return l.sessionID }
func (l *Ledger) Counter() *Counter { return l.counter }
func (l *Ledger) Close() error      { return l.db.Close() }

// OpenReadOnly opens an existing ledger file for query-only access. No
// session row is inserted (so SessionID returns the empty string), and
// the SQLite connection is opened with mode=ro so a foreign daemon
// writing to the same file is not disturbed. Use this for
// `ash report --root <p>` against a target repos ledger.
func OpenReadOnly(path string) (*Ledger, error) {
	db, err := sql.Open("sqlite", path+"?mode=ro&_pragma=busy_timeout(2000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return &Ledger{db: db}, nil
}

type Call struct {
	RequestID          uint64
	Timestamp          time.Time
	Verb               string
	ArgsMsgpack        []byte
	OK                 bool
	ErrCode            string
	ErrMsg             string
	LatencyParseUs     int64
	LatencyExecUs      int64
	LatencySerializeUs int64
	TokensIn           int
	TokensOut          int
	TokensOutNoPrefix  int
	TokensMethod       string
	BytesIn            int
	BytesOut           int
	Truncated          bool
	// Sub-phase latencies (microseconds). Optional; verbs that don't
	// instrument leave them at 0. They overlap by design (walk_us is the
	// wall time of walker.Walk, which contains visitor IO/regex time).
	WalkUs             int64
	IOUs               int64
	RegexUs            int64
	RegexCompileUs     int64
	LatencyDispatchUs  int64
	// ASH-106 streaming surface. Streaming is true when the response went
	// out as Chunk frames + a Final frame; the count and ttfc fields are
	// only meaningful when Streaming is true.
	Streaming          bool
	ChunksOut          int
	TimeToFirstChunkUs int64
	// ASH-123 MCP-transport emit accounting. Populated by the daemon
	// only for requests with Transport == "mcp"; the bytes/tokens here
	// correspond to the JSON envelope the harness actually consumes,
	// which differs from the daemon-pretty-rendered TokensOut.
	TokensOutEmit int
	BytesOutEmit  int
}

// QueryOpts filters for QueryWindow.
type QueryOpts struct {
	// SessionID restricts to a specific session. Empty means no filter.
	// Use the sentinel value "current" to mean the Ledger's own session.
	SessionID string
	// Since restricts to calls with ts >= Since (zero means no filter).
	Since time.Time
	// VerbFilter restricts to calls for a specific verb (empty = no filter).
	VerbFilter string
	// Limit caps the number of rows returned (0 = use DefaultWindowLimit).
	Limit int
}

const DefaultWindowLimit = 5000

// QueryWindow returns calls matching opts in reverse chronological order.
func (l *Ledger) QueryWindow(opts QueryOpts) ([]Call, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = DefaultWindowLimit
	}

	sid := opts.SessionID
	if sid == "current" {
		sid = l.sessionID
	}

	// Build WHERE clauses dynamically.
	where := []string{}
	args := []any{}
	if sid != "" {
		where = append(where, "session_id = ?")
		args = append(args, sid)
	}
	if !opts.Since.IsZero() {
		where = append(where, "ts >= ?")
		args = append(args, opts.Since.UnixNano())
	}
	if opts.VerbFilter != "" {
		where = append(where, "verb = ?")
		args = append(args, opts.VerbFilter)
	}
	args = append(args, limit)

	clause := ""
	if len(where) > 0 {
		clause = "WHERE " + strings.Join(where, " AND ") + " "
	}

	rows, err := l.db.Query(`
		SELECT ts, verb, ok, err_code, err_msg,
		       latency_parse_us, latency_exec_us, latency_serialize_us,
		       tokens_in, tokens_out, tokens_out_no_prefix, tokens_method,
		       bytes_in, bytes_out, truncated,
		       walk_us, io_us, regex_us, regex_compile_us, latency_dispatch_us,
		       tokens_out_emit, bytes_out_emit,
		       args_msgpack
		FROM calls `+clause+`ORDER BY id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var calls []Call
	for rows.Next() {
		var c Call
		var ts int64
		var okInt, truncInt int
		if err := rows.Scan(
			&ts, &c.Verb, &okInt, &c.ErrCode, &c.ErrMsg,
			&c.LatencyParseUs, &c.LatencyExecUs, &c.LatencySerializeUs,
			&c.TokensIn, &c.TokensOut, &c.TokensOutNoPrefix, &c.TokensMethod,
			&c.BytesIn, &c.BytesOut, &truncInt,
			&c.WalkUs, &c.IOUs, &c.RegexUs, &c.RegexCompileUs, &c.LatencyDispatchUs,
			&c.TokensOutEmit, &c.BytesOutEmit,
			&c.ArgsMsgpack,
		); err != nil {
			return nil, err
		}
		c.Timestamp = time.Unix(0, ts)
		c.OK = okInt != 0
		c.Truncated = truncInt != 0
		calls = append(calls, c)
	}
	return calls, rows.Err()
}

// QueryRecent returns up to n calls in reverse chronological order.
// If verbFilter is non-empty, only calls with that verb are returned.
func (l *Ledger) QueryRecent(n int, verbFilter string) ([]Call, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if verbFilter != "" {
		rows, err = l.db.Query(`
			SELECT ts, verb, ok, err_code, err_msg,
			       latency_parse_us, latency_exec_us, latency_serialize_us,
			       tokens_in, tokens_out, tokens_out_no_prefix, tokens_method,
			       bytes_in, bytes_out, truncated,
			       walk_us, io_us, regex_us, regex_compile_us, latency_dispatch_us,
			       tokens_out_emit, bytes_out_emit
			FROM calls WHERE verb = ?
			ORDER BY id DESC LIMIT ?`, verbFilter, n)
	} else {
		rows, err = l.db.Query(`
			SELECT ts, verb, ok, err_code, err_msg,
			       latency_parse_us, latency_exec_us, latency_serialize_us,
			       tokens_in, tokens_out, tokens_out_no_prefix, tokens_method,
			       bytes_in, bytes_out, truncated,
			       walk_us, io_us, regex_us, regex_compile_us, latency_dispatch_us,
			       tokens_out_emit, bytes_out_emit
			FROM calls ORDER BY id DESC LIMIT ?`, n)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var calls []Call
	for rows.Next() {
		var c Call
		var ts int64
		var okInt, truncInt int
		if err := rows.Scan(
			&ts, &c.Verb, &okInt, &c.ErrCode, &c.ErrMsg,
			&c.LatencyParseUs, &c.LatencyExecUs, &c.LatencySerializeUs,
			&c.TokensIn, &c.TokensOut, &c.TokensOutNoPrefix, &c.TokensMethod,
			&c.BytesIn, &c.BytesOut, &truncInt,
			&c.WalkUs, &c.IOUs, &c.RegexUs, &c.RegexCompileUs, &c.LatencyDispatchUs,
			&c.TokensOutEmit, &c.BytesOutEmit,
		); err != nil {
			return nil, err
		}
		c.Timestamp = time.Unix(0, ts)
		c.OK = okInt != 0
		c.Truncated = truncInt != 0
		calls = append(calls, c)
	}
	return calls, rows.Err()
}

func (l *Ledger) Record(c *Call) (int64, error) {
	res, err := l.db.Exec(`INSERT INTO calls (
		session_id, request_id, ts, verb, args_msgpack,
		ok, err_code, err_msg,
		latency_parse_us, latency_exec_us, latency_serialize_us,
		tokens_in, tokens_out, tokens_out_no_prefix, tokens_method,
		bytes_in, bytes_out, truncated,
		walk_us, io_us, regex_us, regex_compile_us, latency_dispatch_us,
		streaming, chunks_out, time_to_first_chunk_us,
		tokens_out_emit, bytes_out_emit
	) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		l.sessionID, int64(c.RequestID), c.Timestamp.UnixNano(), c.Verb, c.ArgsMsgpack,
		boolToInt(c.OK), c.ErrCode, c.ErrMsg,
		c.LatencyParseUs, c.LatencyExecUs, c.LatencySerializeUs,
		c.TokensIn, c.TokensOut, c.TokensOutNoPrefix, c.TokensMethod,
		c.BytesIn, c.BytesOut, boolToInt(c.Truncated),
		c.WalkUs, c.IOUs, c.RegexUs, c.RegexCompileUs, c.LatencyDispatchUs,
		boolToInt(c.Streaming), c.ChunksOut, c.TimeToFirstChunkUs,
		c.TokensOutEmit, c.BytesOutEmit,
	)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	return id, err
}

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

// Cleanup removes old call rows and orphaned sessions from the ledger.
// It is called once at daemon startup before any connections are accepted.
// Errors are non-fatal; callers should log them but continue.
func (l *Ledger) Cleanup(cfg CleanupCfg) (*CleanupResult, error) {
	res := &CleanupResult{}

	if cfg.MaxAge > 0 {
		cutoff := time.Now().Add(-cfg.MaxAge).UnixNano()
		r, err := l.db.Exec(`DELETE FROM calls WHERE ts < ?`, cutoff)
		if err != nil {
			return nil, fmt.Errorf("ledger cleanup age: %w", err)
		}
		res.DeletedCalls, _ = r.RowsAffected()
	}

	if cfg.MaxRows > 0 {
		var count int
		if err := l.db.QueryRow(`SELECT COUNT(*) FROM calls`).Scan(&count); err != nil {
			return nil, fmt.Errorf("ledger cleanup count: %w", err)
		}
		if excess := count - cfg.MaxRows; excess > 0 {
			r, err := l.db.Exec(
				`DELETE FROM calls WHERE id IN (SELECT id FROM calls ORDER BY id ASC LIMIT ?)`,
				excess)
			if err != nil {
				return nil, fmt.Errorf("ledger cleanup rows: %w", err)
			}
			n, _ := r.RowsAffected()
			res.DeletedCalls += n
		}
	}

	// Delete sessions that have no remaining calls, excluding the current
	// session (which was just created in Open and has no calls yet).
	r, err := l.db.Exec(
		`DELETE FROM sessions WHERE id NOT IN (SELECT DISTINCT session_id FROM calls) AND id != ?`,
		l.sessionID)
	if err != nil {
		return nil, fmt.Errorf("ledger cleanup sessions: %w", err)
	}
	res.DeletedSessions, _ = r.RowsAffected()

	l.db.Exec(`PRAGMA optimize`)

	if cfg.Vacuum {
		if _, err := l.db.Exec(`VACUUM`); err != nil {
			return nil, fmt.Errorf("ledger vacuum: %w", err)
		}
		res.Vacuumed = true
	}

	return res, nil
}

func (l *Ledger) UpdateSerializeStats(rowID int64, bytesOut int, serializeUs int64) error {
	_, err := l.db.Exec(
		`UPDATE calls SET bytes_out = ?, latency_serialize_us = ? WHERE id = ?`,
		bytesOut, serializeUs, rowID,
	)
	return err
}

// UpdateMCPEmit patches the MCP-envelope emit accounting onto a row that
// has already been Record()ed. The daemon calls this only for requests
// with Transport == "mcp" — once the response is encoded and bytes_out is
// known, it builds the JSON envelope ashmcp would emit (proto.MCPEnvelope),
// tokenizes those bytes, and writes the result here. CLI rows leave both
// columns at zero.
func (l *Ledger) UpdateMCPEmit(rowID int64, bytesEmit, tokensEmit int) error {
	_, err := l.db.Exec(
		`UPDATE calls SET bytes_out_emit = ?, tokens_out_emit = ? WHERE id = ?`,
		bytesEmit, tokensEmit, rowID,
	)
	return err
}

func newSessionID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failure is unrecoverable; fall back to time-based.
		return fmt.Sprintf("t%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
