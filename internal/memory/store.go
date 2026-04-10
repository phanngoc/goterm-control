package memory

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// MemoryBackend is the interface for memory storage backends.
// Both the JSONL Store and SQLite store implement this.
type MemoryBackend interface {
	Append(entry Entry) error
	Search(query string, limit int) ([]Entry, error)
	ReadAll() ([]Entry, error)
}

// Entry is a single memory record extracted from a conversation turn.
type Entry struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	SessionID string    `json:"session_id"`
	ChatID    int64     `json:"chat_id,omitempty"`
	Facts     []string  `json:"facts"`
	Keywords  []string  `json:"keywords"`
	Summary   string    `json:"summary"`
}

// Store manages memory entries in a JSONL file.
type Store struct {
	dir string
}

// NewStore creates a memory store that writes to dir/memory.jsonl.
func NewStore(dir string) *Store {
	return &Store{dir: dir}
}

// Append adds an entry to the memory file.
func (s *Store) Append(entry Entry) error {
	if err := os.MkdirAll(s.dir, 0755); err != nil {
		return fmt.Errorf("mkdir memory: %w", err)
	}

	if entry.ID == "" {
		entry.ID = fmt.Sprintf("mem_%d", time.Now().UnixNano())
	}
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = time.Now()
	}

	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal memory entry: %w", err)
	}

	path := s.filePath()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open memory file: %w", err)
	}
	defer f.Close()

	_, err = f.Write(append(data, '\n'))
	return err
}

// ReadAll returns all memory entries.
func (s *Store) ReadAll() ([]Entry, error) {
	path := s.filePath()
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open memory file: %w", err)
	}
	defer f.Close()

	var entries []Entry
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 256*1024), 256*1024)

	for scanner.Scan() {
		var e Entry
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			continue
		}
		entries = append(entries, e)
	}
	return entries, scanner.Err()
}

// Search finds memory entries relevant to the query, sorted by score.
func (s *Store) Search(query string, limit int) ([]Entry, error) {
	all, err := s.ReadAll()
	if err != nil {
		return nil, err
	}
	return searchEntries(all, query, limit), nil
}

func (s *Store) filePath() string {
	return filepath.Join(s.dir, "memory.jsonl")
}
