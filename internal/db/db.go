// Package db provides SQLite-backed persistent storage for the
// voice learning model, conversation history, and configuration.
package db

import (
	"database/sql"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// TranscriptEntry is a single turn in the conversation log.
type TranscriptEntry struct {
	Role      string
	Text      string
	CreatedAt time.Time
}

// DB wraps a SQLite database connection.
type DB struct {
	db *sql.DB
}

// Open opens (or creates) a SQLite database at path and ensures the
// schema exists.
func Open(path string) (*DB, error) {
	sqlDB, err := sql.Open("sqlite3", path+"?_journal_mode=WAL")
	if err != nil {
		return nil, err
	}
	for _, ddl := range []string{
		`CREATE TABLE IF NOT EXISTS transcript (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			role       TEXT NOT NULL,
			text       TEXT NOT NULL,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS kv (
			key   TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS raw_log (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			source     TEXT NOT NULL,
			line       TEXT NOT NULL,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
	} {
		if _, err := sqlDB.Exec(ddl); err != nil {
			sqlDB.Close()
			return nil, err
		}
	}
	return &DB{db: sqlDB}, nil
}

// AppendTranscript inserts a transcript entry.
func (d *DB) AppendTranscript(role, text string) error {
	_, err := d.db.Exec(
		`INSERT INTO transcript (role, text) VALUES (?, ?)`,
		role, text,
	)
	return err
}

// LoadTranscript returns all transcript entries in chronological order.
func (d *DB) LoadTranscript() ([]TranscriptEntry, error) {
	rows, err := d.db.Query(
		`SELECT role, text, created_at FROM transcript ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []TranscriptEntry
	for rows.Next() {
		var e TranscriptEntry
		if err := rows.Scan(&e.Role, &e.Text, &e.CreatedAt); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// AppendRawLog inserts a raw NDJSON line from a Claude process.
func (d *DB) AppendRawLog(source, line string) error {
	_, err := d.db.Exec(
		`INSERT INTO raw_log (source, line) VALUES (?, ?)`,
		source, line,
	)
	return err
}

// Get returns a value from the kv table, or "" if not found.
func (d *DB) Get(key string) string {
	var val string
	d.db.QueryRow(`SELECT value FROM kv WHERE key = ?`, key).Scan(&val)
	return val
}

// Set upserts a value in the kv table.
func (d *DB) Set(key, value string) error {
	_, err := d.db.Exec(
		`INSERT INTO kv (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		key, value,
	)
	return err
}

// Close closes the database connection.
func (d *DB) Close() error {
	return d.db.Close()
}
