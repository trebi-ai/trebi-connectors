package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "github.com/mattn/go-sqlite3"
)

type DB struct {
	path       string
	sql        *sql.DB
	ftsEnabled bool
	closed     bool
}

func Open(path string) (*DB, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("db path is required")
	}
	// Reject paths that could inject SQLite URI parameters (#59).
	if strings.ContainsAny(path, "?#") {
		return nil, fmt.Errorf("db path must not contain '?' or '#'")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create db directory: %w", err)
	}

	db, err := sql.Open("sqlite3", fmt.Sprintf("file:%s?_foreign_keys=on&_busy_timeout=5000&_journal_mode=WAL&_synchronous=NORMAL", path))
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	s := &DB{path: path, sql: db}
	if err := s.init(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close moves the WAL into the database file and closes it.
func (d *DB) Close() error {
	if d == nil || d.sql == nil || d.closed {
		return nil
	}
	d.closed = true
	_, err := d.sql.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	if cerr := d.sql.Close(); err == nil {
		err = cerr
	}
	return err
}

func (d *DB) init() error {
	_, _ = d.sql.Exec("PRAGMA temp_store=MEMORY;")

	return d.ensureSchema()
}
