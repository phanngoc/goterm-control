package storage

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// DB wraps a SQLite connection with goterm-specific operations.
type DB struct {
	conn    *sql.DB
	path    string
	dataDir string // parent directory, used for migration lookups
}

// DSN builds the connection string for a goterm SQLite database.
//
// The pragmas MUST ride in the DSN rather than a PRAGMA statement after
// sql.Open: *sql.DB is a connection POOL, so an Exec applies to whichever
// single connection served it. journal_mode is persisted in the database
// header and survives that, but busy_timeout, foreign_keys and synchronous are
// per-connection — every connection the pool opened later would silently get
// busy_timeout=0 and fail instantly on the first write contention. Harmless
// with one process on the file; a permanent fault once two agents share one.
func DSN(path string) string {
	return "file:" + path +
		"?_pragma=journal_mode(WAL)" +
		"&_pragma=busy_timeout(10000)" +
		"&_pragma=foreign_keys(1)" +
		"&_pragma=synchronous(NORMAL)"
}

// Open opens (or creates) the SQLite database at the given path.
// Runs schema migrations and optional data import automatically.
func Open(path string) (*DB, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("storage: mkdir %s: %w", dir, err)
	}

	conn, err := sql.Open("sqlite", DSN(path))
	if err != nil {
		return nil, fmt.Errorf("storage: open %s: %w", path, err)
	}

	db := &DB{conn: conn, path: path, dataDir: dir}

	if err := db.migrate(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("storage: migrate: %w", err)
	}

	return db, nil
}

// Close closes the database connection.
func (db *DB) Close() error {
	return db.conn.Close()
}

// Conn returns the underlying *sql.DB for advanced use.
func (db *DB) Conn() *sql.DB {
	return db.conn
}
