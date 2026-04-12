package storage

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ngocp/goterm-control/internal/memory"
)

// SQLiteMemoryStore implements memory.MemoryBackend backed by SQLite with FTS5.
type SQLiteMemoryStore struct {
	db *DB
}

// NewMemoryStore creates a memory store backed by the given database.
func NewMemoryStore(db *DB) *SQLiteMemoryStore {
	return &SQLiteMemoryStore{db: db}
}

// Append inserts a memory entry into the database.
func (m *SQLiteMemoryStore) Append(entry memory.Entry) error {
	if entry.ID == "" {
		entry.ID = fmt.Sprintf("mem_%d", time.Now().UnixNano())
	}
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = time.Now()
	}

	factsJSON, err := json.Marshal(entry.Facts)
	if err != nil {
		return fmt.Errorf("marshal facts: %w", err)
	}
	keywordsJSON, err := json.Marshal(entry.Keywords)
	if err != nil {
		return fmt.Errorf("marshal keywords: %w", err)
	}

	_, err = m.db.conn.Exec(`INSERT INTO memory (id, created_at, session_id, chat_id, facts, keywords, summary, intent)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		entry.ID, entry.CreatedAt.Format(time.RFC3339),
		entry.SessionID, entry.ChatID,
		string(factsJSON), string(keywordsJSON), entry.Summary, entry.Intent,
	)
	return err
}

// Search finds memory entries matching the query using FTS5.
// Falls back to LIKE queries if FTS match fails.
func (m *SQLiteMemoryStore) Search(query string, limit int) ([]memory.Entry, error) {
	if limit <= 0 {
		limit = 5
	}

	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}

	// Build FTS5 query: tokenize and OR together
	tokens := tokenizeForFTS(query)
	if len(tokens) == 0 {
		return nil, nil
	}

	ftsQuery := strings.Join(tokens, " OR ")

	rows, err := m.db.conn.Query(`
		SELECT m.id, m.created_at, m.session_id, m.chat_id, m.facts, m.keywords, m.summary, m.intent
		FROM memory m
		JOIN memory_fts f ON f.rowid = m.rowid
		WHERE memory_fts MATCH ?
		ORDER BY rank
		LIMIT ?`, ftsQuery, limit)
	if err != nil {
		// Fallback to LIKE if FTS fails (e.g. special characters)
		return m.searchFallback(query, limit)
	}
	defer rows.Close()

	return scanMemoryRows(rows)
}

// searchFallback uses LIKE queries when FTS5 MATCH fails.
func (m *SQLiteMemoryStore) searchFallback(query string, limit int) ([]memory.Entry, error) {
	pattern := "%" + query + "%"
	rows, err := m.db.conn.Query(`
		SELECT id, created_at, session_id, chat_id, facts, keywords, summary, intent
		FROM memory
		WHERE keywords LIKE ? OR facts LIKE ? OR summary LIKE ? OR intent LIKE ?
		ORDER BY created_at DESC
		LIMIT ?`, pattern, pattern, pattern, pattern, limit)
	if err != nil {
		return nil, fmt.Errorf("search fallback: %w", err)
	}
	defer rows.Close()
	return scanMemoryRows(rows)
}

// ReadAll returns all memory entries ordered by creation time.
func (m *SQLiteMemoryStore) ReadAll() ([]memory.Entry, error) {
	rows, err := m.db.conn.Query(`SELECT id, created_at, session_id, chat_id, facts, keywords, summary, intent
		FROM memory ORDER BY created_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("read all memory: %w", err)
	}
	defer rows.Close()
	return scanMemoryRows(rows)
}

type rowScanner interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
}

func scanMemoryRows(rows rowScanner) ([]memory.Entry, error) {
	var entries []memory.Entry
	for rows.Next() {
		var (
			id, createdStr, sessionID, factsStr, keywordsStr, summary, intent string
			chatID                                                            int64
		)
		if err := rows.Scan(&id, &createdStr, &sessionID, &chatID, &factsStr, &keywordsStr, &summary, &intent); err != nil {
			continue
		}

		created, _ := time.Parse(time.RFC3339, createdStr)

		var facts []string
		json.Unmarshal([]byte(factsStr), &facts)
		var keywords []string
		json.Unmarshal([]byte(keywordsStr), &keywords)

		entries = append(entries, memory.Entry{
			ID:        id,
			CreatedAt: created,
			SessionID: sessionID,
			ChatID:    chatID,
			Facts:     facts,
			Keywords:  keywords,
			Summary:   summary,
			Intent:    intent,
		})
	}
	return entries, rows.Err()
}

// tokenizeForFTS splits a query into FTS5-safe tokens.
// Preserves Unicode (Vietnamese) and filters stopwords to prevent
// low-relevance matches from polluting memory injection.
func tokenizeForFTS(query string) []string {
	words := strings.Fields(strings.ToLower(query))
	var tokens []string
	for _, w := range words {
		// Strip punctuation but keep Unicode letters and digits
		clean := strings.Map(func(r rune) rune {
			if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' ||
				r >= 0x00C0 && r <= 0x024F || // Latin Extended (Vietnamese diacritics)
				r >= 0x1E00 && r <= 0x1EFF { // Latin Extended Additional (ắ, ồ, ử, etc.)
				return r
			}
			return -1
		}, w)
		if len(clean) < 2 || memory.Stopwords[clean] {
			continue
		}
		tokens = append(tokens, "\""+clean+"\"") // quote for FTS5 exact token match
	}
	return tokens
}
