package ledger

// Bench-run persistence. The bench_runs and bench_case_results tables
// hold one record per `ash bench` invocation plus per-case rows. Cleanup
// (age- and row-based) deliberately does NOT touch these tables: bench
// data is sparse and we want trends over months, not days.

import (
	"database/sql"
	"fmt"
	"time"
)

// BenchRun mirrors a row in the bench_runs table.
type BenchRun struct {
	ID             int64
	RunUUID        string
	Timestamp      time.Time
	AshVersion     string
	AshCommitSHA   string
	CaseSetVersion string
	RepoSHA        string
	RepoDirty      bool
	Hostname       string
	CPUCount       int
	DaemonUptimeUs int64
	RepeatN        int
	WarmupN        int
	Notes          string
}

// BenchCaseResult mirrors a row in the bench_case_results table. Tokens
// are deterministic given identical inputs and pinned to the first
// measured iteration; latency is summarized as p50 + min over the
// measured iterations.
type BenchCaseResult struct {
	RunID            int64
	CaseName         string
	Verb             string
	AshTokens        int
	BashTokens       int
	AshBytes         int
	BashBytes        int
	AshLatencyUsP50  int64
	AshLatencyUsMin  int64
	BashLatencyUsP50 int64
	BashLatencyUsMin int64
	AshOK            bool
	AshErr           string
	BashExit         int
	BashRunErr       string
	AshTruncated     bool
	BashTruncated    bool
}

// RecordBenchRun inserts the run row and all per-case rows in one
// transaction. The returned int64 is the run_id assigned by SQLite.
func (l *Ledger) RecordBenchRun(run *BenchRun, results []BenchCaseResult) (int64, error) {
	tx, err := l.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	res, err := tx.Exec(`INSERT INTO bench_runs (
		run_uuid, ts, ash_version, ash_commit_sha, case_set_version,
		repo_sha, repo_dirty, hostname, cpu_count, daemon_uptime_us,
		repeat_n, warmup_n, notes
	) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		run.RunUUID, run.Timestamp.UnixNano(), run.AshVersion, run.AshCommitSHA, run.CaseSetVersion,
		run.RepoSHA, boolToInt(run.RepoDirty), run.Hostname, run.CPUCount, run.DaemonUptimeUs,
		run.RepeatN, run.WarmupN, run.Notes,
	)
	if err != nil {
		return 0, fmt.Errorf("insert bench_run: %w", err)
	}
	runID, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}

	stmt, err := tx.Prepare(`INSERT INTO bench_case_results (
		run_id, case_name, verb,
		ash_tokens, bash_tokens, ash_bytes, bash_bytes,
		ash_latency_us_p50, ash_latency_us_min,
		bash_latency_us_p50, bash_latency_us_min,
		ash_ok, ash_err, bash_exit, bash_run_err,
		ash_truncated, bash_truncated
	) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()

	for i := range results {
		r := &results[i]
		if _, err := stmt.Exec(
			runID, r.CaseName, r.Verb,
			r.AshTokens, r.BashTokens, r.AshBytes, r.BashBytes,
			r.AshLatencyUsP50, r.AshLatencyUsMin,
			r.BashLatencyUsP50, r.BashLatencyUsMin,
			boolToInt(r.AshOK), r.AshErr, r.BashExit, r.BashRunErr,
			boolToInt(r.AshTruncated), boolToInt(r.BashTruncated),
		); err != nil {
			return 0, fmt.Errorf("insert bench_case_result %s: %w", r.CaseName, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return runID, nil
}

// QueryBenchRuns returns up to limit recent bench runs, newest first.
// Limit <= 0 falls back to 50.
func (l *Ledger) QueryBenchRuns(limit int) ([]BenchRun, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := l.db.Query(`SELECT id, run_uuid, ts, ash_version, ash_commit_sha,
		case_set_version, repo_sha, repo_dirty, hostname, cpu_count,
		daemon_uptime_us, repeat_n, warmup_n, COALESCE(notes,'')
		FROM bench_runs ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanBenchRuns(rows)
}

// QueryBenchRun returns one run plus its per-case rows. The runUUID
// argument can be a full UUID or a unique prefix (>= 4 chars). An
// ambiguous prefix returns an error; an unknown UUID returns
// sql.ErrNoRows so callers can distinguish.
func (l *Ledger) QueryBenchRun(runUUID string) (*BenchRun, []BenchCaseResult, error) {
	if len(runUUID) < 4 {
		return nil, nil, fmt.Errorf("run_uuid must be >= 4 characters")
	}
	resolved, err := l.resolveRunUUID(runUUID)
	if err != nil {
		return nil, nil, err
	}
	var run BenchRun
	var ts int64
	var dirtyInt int
	err = l.db.QueryRow(`SELECT id, run_uuid, ts, ash_version, COALESCE(ash_commit_sha,''),
		case_set_version, COALESCE(repo_sha,''), repo_dirty, COALESCE(hostname,''), cpu_count,
		daemon_uptime_us, repeat_n, warmup_n, COALESCE(notes,'')
		FROM bench_runs WHERE run_uuid = ?`, resolved).Scan(
		&run.ID, &run.RunUUID, &ts, &run.AshVersion, &run.AshCommitSHA,
		&run.CaseSetVersion, &run.RepoSHA, &dirtyInt, &run.Hostname, &run.CPUCount,
		&run.DaemonUptimeUs, &run.RepeatN, &run.WarmupN, &run.Notes,
	)
	if err != nil {
		return nil, nil, err
	}
	run.Timestamp = time.Unix(0, ts)
	run.RepoDirty = dirtyInt != 0

	results, err := l.queryBenchCaseResults(run.ID)
	if err != nil {
		return nil, nil, err
	}
	return &run, results, nil
}

// QueryBenchBaseline returns the per-case median ash/bash tokens and
// latency across all bench runs whose ts >= since. Used by `ash bench
// --baseline 7d` to compare a fresh run against rolling history.
//
// The aggregation strategy: for each (case_name, verb), select the run
// closest to the median by ash_tokens, then return that row. SQLite has
// no MEDIAN(); this is a simple, deterministic-enough proxy that
// preserves a real row's worth of data instead of synthesizing
// per-column medians that wouldn't correspond to any single run.
func (l *Ledger) QueryBenchBaseline(since time.Time) (map[string]BenchCaseResult, error) {
	rows, err := l.db.Query(`
		WITH eligible AS (
			SELECT bcr.*, ROW_NUMBER() OVER (
				PARTITION BY bcr.case_name
				ORDER BY bcr.ash_tokens
			) AS rn,
			COUNT(*) OVER (PARTITION BY bcr.case_name) AS cnt
			FROM bench_case_results bcr
			JOIN bench_runs br ON br.id = bcr.run_id
			WHERE br.ts >= ?
		)
		SELECT run_id, case_name, verb,
		       ash_tokens, bash_tokens, ash_bytes, bash_bytes,
		       ash_latency_us_p50, ash_latency_us_min,
		       bash_latency_us_p50, bash_latency_us_min,
		       ash_ok, COALESCE(ash_err,''), bash_exit, COALESCE(bash_run_err,''),
		       ash_truncated, bash_truncated
		FROM eligible
		WHERE rn = (cnt + 1) / 2`,
		since.UnixNano())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]BenchCaseResult{}
	for rows.Next() {
		var r BenchCaseResult
		var okInt, ashTruncInt, bashTruncInt int
		if err := rows.Scan(
			&r.RunID, &r.CaseName, &r.Verb,
			&r.AshTokens, &r.BashTokens, &r.AshBytes, &r.BashBytes,
			&r.AshLatencyUsP50, &r.AshLatencyUsMin,
			&r.BashLatencyUsP50, &r.BashLatencyUsMin,
			&okInt, &r.AshErr, &r.BashExit, &r.BashRunErr,
			&ashTruncInt, &bashTruncInt,
		); err != nil {
			return nil, err
		}
		r.AshOK = okInt != 0
		r.AshTruncated = ashTruncInt != 0
		r.BashTruncated = bashTruncInt != 0
		out[r.CaseName] = r
	}
	return out, rows.Err()
}

func (l *Ledger) queryBenchCaseResults(runID int64) ([]BenchCaseResult, error) {
	rows, err := l.db.Query(`SELECT run_id, case_name, verb,
		ash_tokens, bash_tokens, ash_bytes, bash_bytes,
		ash_latency_us_p50, ash_latency_us_min,
		bash_latency_us_p50, bash_latency_us_min,
		ash_ok, COALESCE(ash_err,''), bash_exit, COALESCE(bash_run_err,''),
		ash_truncated, bash_truncated
		FROM bench_case_results WHERE run_id = ? ORDER BY id ASC`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []BenchCaseResult
	for rows.Next() {
		var r BenchCaseResult
		var okInt, ashTruncInt, bashTruncInt int
		if err := rows.Scan(
			&r.RunID, &r.CaseName, &r.Verb,
			&r.AshTokens, &r.BashTokens, &r.AshBytes, &r.BashBytes,
			&r.AshLatencyUsP50, &r.AshLatencyUsMin,
			&r.BashLatencyUsP50, &r.BashLatencyUsMin,
			&okInt, &r.AshErr, &r.BashExit, &r.BashRunErr,
			&ashTruncInt, &bashTruncInt,
		); err != nil {
			return nil, err
		}
		r.AshOK = okInt != 0
		r.AshTruncated = ashTruncInt != 0
		r.BashTruncated = bashTruncInt != 0
		out = append(out, r)
	}
	return out, rows.Err()
}

// resolveRunUUID returns the canonical full UUID matching token. token
// can be either a full UUID or a unique prefix. Returns sql.ErrNoRows
// when no match exists; returns a typed ambiguity error when the
// prefix matches more than one row.
func (l *Ledger) resolveRunUUID(token string) (string, error) {
	rows, err := l.db.Query(`SELECT run_uuid FROM bench_runs WHERE run_uuid LIKE ? || '%' LIMIT 2`, token)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var matches []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return "", err
		}
		matches = append(matches, u)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	switch len(matches) {
	case 0:
		return "", sql.ErrNoRows
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("ambiguous run_uuid prefix %q (matches at least 2 runs); pass more characters", token)
	}
}

func scanBenchRuns(rows *sql.Rows) ([]BenchRun, error) {
	var out []BenchRun
	for rows.Next() {
		var r BenchRun
		var ts int64
		var dirtyInt int
		var commit, repo, host sql.NullString
		if err := rows.Scan(
			&r.ID, &r.RunUUID, &ts, &r.AshVersion, &commit,
			&r.CaseSetVersion, &repo, &dirtyInt, &host, &r.CPUCount,
			&r.DaemonUptimeUs, &r.RepeatN, &r.WarmupN, &r.Notes,
		); err != nil {
			return nil, err
		}
		r.Timestamp = time.Unix(0, ts)
		r.AshCommitSHA = commit.String
		r.RepoSHA = repo.String
		r.Hostname = host.String
		r.RepoDirty = dirtyInt != 0
		out = append(out, r)
	}
	return out, rows.Err()
}
