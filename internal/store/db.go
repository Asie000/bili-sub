package store

import (
	"database/sql"

	_ "modernc.org/sqlite"
)

const (
	StatePending        = "pending"
	StateProbed         = "probed"
	StateDownloadable   = "downloadable"
	StateNoSubtitle     = "no_subtitle"
	StateChargingLocked = "charging_locked"
	StateFetched        = "fetched"
	StateTranscribed    = "transcribed"
	StateExported       = "exported"
	StateFailed         = "failed"
)

type Store struct{ db *sql.DB }

func Open(path string) (*Store, error) {
	// File-backed databases are shared by concurrent workers and the HTTP
	// server, so every connection needs a busy timeout; without it the second
	// concurrent writer fails immediately with SQLITE_BUSY.
	dsn := path
	if path != ":memory:" {
		dsn = "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// An in-memory database is private to one connection: if the pool opened a
	// second one it would see an empty database, so pin the pool to one conn.
	if path == ":memory:" {
		db.SetMaxOpenConns(1)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }
