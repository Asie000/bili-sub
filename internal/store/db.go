package store

import (
	"database/sql"
	"strings"

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
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// migrate brings a database created before the aid column existed up to date.
// Adding an already-present column is the only error tolerated: it is how an
// idempotent re-open looks, and any other failure is real.
func migrate(db *sql.DB) error {
	if _, err := db.Exec(`ALTER TABLE videos ADD COLUMN aid INTEGER NOT NULL DEFAULT 0`); err != nil {
		if strings.Contains(err.Error(), "duplicate column name") {
			return nil
		}
		return err
	}
	return nil
}

func (s *Store) Close() error { return s.db.Close() }
