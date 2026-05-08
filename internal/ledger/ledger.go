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

const schemaSQL = `
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
	tokens_method TEXT,
	bytes_in INTEGER NOT NULL,
	bytes_out INTEGER NOT NULL,
	truncated INTEGER NOT NULL DEFAULT 0,
	walk_us INTEGER NOT NULL DEFAULT 0,
	io_us INTEGER NOT NULL DEFAULT 0,
	regex_us INTEGER NOT NULL DEFAULT 0,
	regex_compile_us INTEGER NOT NULL DEFAULT 0,
	latency_dispatch_us INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_calls_session ON calls(session_id);
CREATE INDEX IF NOT EXISTS idx_calls_verb ON calls(verb);
CREATE INDEX IF NOT EXISTS idx_calls_ts ON calls(ts);
`

// migrateSchema brings older ledger DBs (created before the sub-phase
// columns existed) up to the current shape. CREATE TABLE IF NOT EXISTS
// won't add columns to an existing table, so each new column needs an
// explicit ALTER. The function is idempotent: it queries the existing
// columns first and only adds what's missing.
func migrateSchema(db *sql.DB) error {
	cols, err := tableColumns(db, "calls")
	if err != nil {
		return fmt.Errorf("ledger: read schema: %w", err)
	}
	type col struct {
		name string
		decl string
	}
	additions := []col{
		{"walk_us", "INTEGER NOT NULL DEFAULT 0"},
		{"io_us", "INTEGER NOT NULL DEFAULT 0"},
		{"regex_us", "INTEGER NOT NULL DEFAULT 0"},
		{"regex_compile_us", "INTEGER NOT NULL DEFAULT 0"},
		{"latency_dispatch_us", "INTEGER NOT NULL DEFAULT 0"},
	}
	for _, c := range additions {
		if _, ok := cols[c.name]; ok {
			continue
		}
		if _, err := db.Exec(fmt.Sprintf("ALTER TABLE calls ADD COLUMN %s %s", c.name, c.decl)); err != nil {
			return fmt.Errorf("ledger: add column %s: %w", c.name, err)
		}
	}
	return nil
}

func tableColumns(db *sql.DB, table string) (map[string]struct{}, error) {
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols := map[string]struct{}{}
	for rows.Next() {
		var (
			cid     int
			name    string
			ctype   string
			notnull int
			dflt    sql.NullString
			pk      int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return nil, err
		}
		cols[name] = struct{}{}
	}
	return cols, rows.Err()
}

type Ledger struct {
	db        *sql.DB
	sessionID string
	counter   *Counter
}

func Open(path, projectRoot, clientInfo string) (*Ledger, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schemaSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("ledger: schema: %w", err)
	}
	if err := migrateSchema(db); err != nil {
		db.Close()
		return nil, err
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
		       tokens_in, tokens_out, tokens_method,
		       bytes_in, bytes_out, truncated,
		       walk_us, io_us, regex_us, regex_compile_us, latency_dispatch_us,
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
			&c.TokensIn, &c.TokensOut, &c.TokensMethod,
			&c.BytesIn, &c.BytesOut, &truncInt,
			&c.WalkUs, &c.IOUs, &c.RegexUs, &c.RegexCompileUs, &c.LatencyDispatchUs,
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
			       tokens_in, tokens_out, tokens_method,
			       bytes_in, bytes_out, truncated,
			       walk_us, io_us, regex_us, regex_compile_us, latency_dispatch_us
			FROM calls WHERE verb = ?
			ORDER BY id DESC LIMIT ?`, verbFilter, n)
	} else {
		rows, err = l.db.Query(`
			SELECT ts, verb, ok, err_code, err_msg,
			       latency_parse_us, latency_exec_us, latency_serialize_us,
			       tokens_in, tokens_out, tokens_method,
			       bytes_in, bytes_out, truncated,
			       walk_us, io_us, regex_us, regex_compile_us, latency_dispatch_us
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
			&c.TokensIn, &c.TokensOut, &c.TokensMethod,
			&c.BytesIn, &c.BytesOut, &truncInt,
			&c.WalkUs, &c.IOUs, &c.RegexUs, &c.RegexCompileUs, &c.LatencyDispatchUs,
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

func (l *Ledger) Record(c *Call) error {
	_, err := l.db.Exec(`INSERT INTO calls (
		session_id, request_id, ts, verb, args_msgpack,
		ok, err_code, err_msg,
		latency_parse_us, latency_exec_us, latency_serialize_us,
		tokens_in, tokens_out, tokens_method,
		bytes_in, bytes_out, truncated,
		walk_us, io_us, regex_us, regex_compile_us, latency_dispatch_us
	) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		l.sessionID, int64(c.RequestID), c.Timestamp.UnixNano(), c.Verb, c.ArgsMsgpack,
		boolToInt(c.OK), c.ErrCode, c.ErrMsg,
		c.LatencyParseUs, c.LatencyExecUs, c.LatencySerializeUs,
		c.TokensIn, c.TokensOut, c.TokensMethod,
		c.BytesIn, c.BytesOut, boolToInt(c.Truncated),
		c.WalkUs, c.IOUs, c.RegexUs, c.RegexCompileUs, c.LatencyDispatchUs,
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
