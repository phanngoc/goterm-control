package bds

import (
	"crypto/sha1"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// DB wraps a SQLite connection for BDS listings.
type DB struct {
	conn *sql.DB
}

const ddl = `
CREATE TABLE IF NOT EXISTS listings (
	id          TEXT PRIMARY KEY,
	title       TEXT NOT NULL DEFAULT '',
	price       INTEGER NOT NULL DEFAULT 0,
	area        REAL NOT NULL DEFAULT 0,
	location    TEXT NOT NULL DEFAULT '',
	url         TEXT NOT NULL UNIQUE,
	source      TEXT NOT NULL DEFAULT '',
	crawled_at  TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_listings_price  ON listings(price);
CREATE INDEX IF NOT EXISTS idx_listings_source ON listings(source);
CREATE INDEX IF NOT EXISTS idx_listings_crawled ON listings(crawled_at DESC);
`

// OpenDB opens (or creates) the BDS SQLite database at path.
func OpenDB(path string) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, err
	}
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	for _, p := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
		"PRAGMA busy_timeout=5000",
		"PRAGMA synchronous=NORMAL",
	} {
		if _, err := conn.Exec(p); err != nil {
			conn.Close()
			return nil, fmt.Errorf("pragma %s: %w", p, err)
		}
	}
	if _, err := conn.Exec(ddl); err != nil {
		conn.Close()
		return nil, fmt.Errorf("ddl: %w", err)
	}
	return &DB{conn: conn}, nil
}

// Close closes the connection.
func (db *DB) Close() error { return db.conn.Close() }

// Save inserts or ignores a listing (dedup by URL).
// Sets listing.ID from URL hash if empty.
func (db *DB) Save(l *Listing) error {
	if l.ID == "" {
		l.ID = urlID(l.URL)
	}
	if l.CrawledAt.IsZero() {
		l.CrawledAt = time.Now()
	}
	_, err := db.conn.Exec(
		`INSERT OR IGNORE INTO listings(id,title,price,area,location,url,source,crawled_at)
		 VALUES(?,?,?,?,?,?,?,?)`,
		l.ID, l.Title, l.Price, l.Area, l.Location,
		l.URL, l.Source, l.CrawledAt.Format(time.RFC3339),
	)
	return err
}

// Search returns listings matching params, newest first.
func (db *DB) Search(p SearchParams) ([]Listing, error) {
	q := `SELECT id,title,price,area,location,url,source,crawled_at
	      FROM listings WHERE 1=1`
	args := []any{}

	if p.MaxPrice > 0 {
		q += " AND price <= ?"
		args = append(args, p.MaxPrice)
	}
	if p.MinArea > 0 {
		q += " AND area >= ?"
		args = append(args, p.MinArea)
	}
	if p.Source != "" {
		q += " AND source = ?"
		args = append(args, p.Source)
	}
	q += " ORDER BY crawled_at DESC"

	rows, err := db.conn.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Listing
	for rows.Next() {
		var l Listing
		var ts string
		if err := rows.Scan(&l.ID, &l.Title, &l.Price, &l.Area,
			&l.Location, &l.URL, &l.Source, &ts); err != nil {
			continue
		}
		l.CrawledAt, _ = time.Parse(time.RFC3339, ts)
		out = append(out, l)
	}
	return out, rows.Err()
}

// Count returns number of listings in DB.
func (db *DB) Count() int {
	var n int
	db.conn.QueryRow(`SELECT COUNT(*) FROM listings`).Scan(&n)
	return n
}

// urlID creates a short deterministic ID from a URL.
func urlID(url string) string {
	h := sha1.Sum([]byte(url))
	return fmt.Sprintf("%x", h[:8])
}
