// Package store persists monitor checks, incidents and load test summaries in
// SQLite. Times are stored as Unix nanoseconds and durations as nanoseconds.
package store

import (
	"database/sql"
	"time"

	_ "modernc.org/sqlite"

	"github.com/nartkosova/tui-load-testing-tool/internal/load"
	"github.com/nartkosova/tui-load-testing-tool/internal/monitor"
)

const schema = `
CREATE TABLE IF NOT EXISTS checks (
	target TEXT NOT NULL,
	at INTEGER NOT NULL,
	ok INTEGER NOT NULL,
	status INTEGER NOT NULL,
	latency INTEGER NOT NULL,
	error TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS checks_target_at ON checks (target, at);
CREATE TABLE IF NOT EXISTS incidents (
	target TEXT NOT NULL,
	start INTEGER NOT NULL,
	end INTEGER,
	cause TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS load_runs (
	at INTEGER NOT NULL,
	url TEXT NOT NULL,
	pattern TEXT NOT NULL,
	concurrency INTEGER NOT NULL,
	requests INTEGER NOT NULL,
	responses INTEGER NOT NULL,
	errors INTEGER NOT NULL,
	http_errors INTEGER NOT NULL,
	canceled INTEGER NOT NULL,
	bytes INTEGER NOT NULL,
	duration INTEGER NOT NULL,
	p50 INTEGER NOT NULL,
	p95 INTEGER NOT NULL,
	p99 INTEGER NOT NULL,
	max INTEGER NOT NULL
);`

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

// RecordCheck stores a check and opens or closes an incident when the target
// goes down or recovers.
func (s *Store) RecordCheck(u monitor.Update) error {
	c, name := u.Check, u.Status.Name
	cause := ""
	if !c.OK {
		cause = monitor.Cause(c.Result)
	}
	if _, err := s.db.Exec(`INSERT INTO checks VALUES (?, ?, ?, ?, ?, ?)`,
		name, c.At.UnixNano(), c.OK, c.Result.StatusCode, int64(c.Result.Duration), cause); err != nil {
		return err
	}
	switch {
	case u.Changed() && u.Status.State == monitor.Down:
		_, err := s.db.Exec(`INSERT INTO incidents VALUES (?, ?, NULL, ?)`, name, u.Status.Since.UnixNano(), cause)
		return err
	case u.Changed() && u.Previous == monitor.Down:
		_, err := s.db.Exec(`UPDATE incidents SET end = ? WHERE target = ? AND end IS NULL`, c.At.UnixNano(), name)
		return err
	}
	return nil
}

func (s *Store) RecordLoadRun(at time.Time, url, pattern string, concurrency int, sum load.Summary) error {
	l := sum.SuccessLatency
	_, err := s.db.Exec(`INSERT INTO load_runs VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		at.UnixNano(), url, pattern, concurrency, sum.Requests, sum.Responses, sum.Errors, sum.HTTPErrors,
		sum.Canceled, sum.BytesRead, int64(sum.Duration), int64(l.P50), int64(l.P95), int64(l.P99), int64(l.Max))
	return err
}
