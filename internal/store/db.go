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
	db, err := sql.Open("sqlite", path)
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
