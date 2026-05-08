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
	truncated INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_calls_session ON calls(session_id);
CREATE INDEX IF NOT EXISTS idx_calls_verb ON calls(verb);
CREATE INDEX IF NOT EXISTS idx_calls_ts ON calls(ts);
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
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schemaSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("ledger: schema: %w", err)
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
			SELECT ts, verb, ok, err_code,
			       latency_parse_us, latency_exec_us, latency_serialize_us,
			       tokens_in, tokens_out, tokens_method,
			       bytes_in, bytes_out, truncated
			FROM calls WHERE verb = ?
			ORDER BY id DESC LIMIT ?`, verbFilter, n)
	} else {
		rows, err = l.db.Query(`
			SELECT ts, verb, ok, err_code,
			       latency_parse_us, latency_exec_us, latency_serialize_us,
			       tokens_in, tokens_out, tokens_method,
			       bytes_in, bytes_out, truncated
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
			&ts, &c.Verb, &okInt, &c.ErrCode,
			&c.LatencyParseUs, &c.LatencyExecUs, &c.LatencySerializeUs,
			&c.TokensIn, &c.TokensOut, &c.TokensMethod,
			&c.BytesIn, &c.BytesOut, &truncInt,
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
		bytes_in, bytes_out, truncated
	) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		l.sessionID, int64(c.RequestID), c.Timestamp.UnixNano(), c.Verb, c.ArgsMsgpack,
		boolToInt(c.OK), c.ErrCode, c.ErrMsg,
		c.LatencyParseUs, c.LatencyExecUs, c.LatencySerializeUs,
		c.TokensIn, c.TokensOut, c.TokensMethod,
		c.BytesIn, c.BytesOut, boolToInt(c.Truncated),
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
