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

// Open opens (or creates) the SQLite database at the given path.
// Runs schema migrations and optional data import automatically.
func Open(path string) (*DB, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("storage: mkdir %s: %w", dir, err)
	}

	conn, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("storage: open %s: %w", path, err)
	}

	// Performance and safety pragmas
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA busy_timeout=5000",
		"PRAGMA foreign_keys=ON",
		"PRAGMA synchronous=NORMAL",
	} {
		if _, err := conn.Exec(pragma); err != nil {
			conn.Close()
			return nil, fmt.Errorf("storage: %s: %w", pragma, err)
		}
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
