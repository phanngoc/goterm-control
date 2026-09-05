package coord

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Note kinds. Free-form in the column, but these are what the CLI suggests and
// what the renderer groups by.
const (
	KindFact     = "fact"
	KindDecision = "decision"
	KindResult   = "result"
	KindGotcha   = "gotcha"
)

// ScopeShared marks a note every agent should see. Any other value scopes the
// note to the agent whose id it matches.
const ScopeShared = "shared"

// Note is one durable thing an agent learned.
type Note struct {
	ID           string    `json:"id"`
	Author       string    `json:"author"`
	Scope        string    `json:"scope"`
	Kind         string    `json:"kind"`
	Title        string    `json:"title"`
	Body         string    `json:"body,omitempty"`
	Tags         string    `json:"tags,omitempty"`
	SupersededBy string    `json:"superseded_by,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

// NewNote describes a note to record.
type NewNote struct {
	Author     string
	Scope      string // "" means shared
	Kind       string // "" means fact
	Title      string
	Body       string
	Tags       string
	Supersedes string // id of the note this replaces
}

// AddNote records a note. When Supersedes is set the older note is marked
// replaced rather than edited, so both the correction and what it corrected
// stay readable.
func (db *DB) AddNote(n NewNote) (*Note, error) {
	if strings.TrimSpace(n.Title) == "" {
		return nil, fmt.Errorf("coord: note title is required")
	}
	note := &Note{
		ID:        "n_" + uuid.NewString(),
		Author:    n.Author,
		Scope:     firstNonEmpty(n.Scope, ScopeShared),
		Kind:      firstNonEmpty(n.Kind, KindFact),
		Title:     n.Title,
		Body:      n.Body,
		Tags:      n.Tags,
		CreatedAt: time.Now(),
	}

	tx, err := db.conn.Begin()
	if err != nil {
		return nil, fmt.Errorf("add note: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`INSERT INTO shared_notes
		(id, author, scope, kind, title, body, tags, superseded_by, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, '', ?)`,
		note.ID, note.Author, note.Scope, note.Kind, note.Title, note.Body,
		note.Tags, ts(note.CreatedAt)); err != nil {
		return nil, fmt.Errorf("add note: %w", err)
	}

	if n.Supersedes != "" {
		res, err := tx.Exec(`UPDATE shared_notes SET superseded_by = ?
			WHERE id = ? AND superseded_by = ''`, note.ID, n.Supersedes)
		if err != nil {
			return nil, fmt.Errorf("supersede %s: %w", n.Supersedes, err)
		}
		if rows, _ := res.RowsAffected(); rows == 0 {
			return nil, fmt.Errorf("coord: note %s is unknown or already superseded", n.Supersedes)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("add note: %w", err)
	}
	return note, nil
}

// NoteFilter narrows a note listing.
type NoteFilter struct {
	// Scope "" returns shared notes plus nothing else; set it to an agent id
	// to get that agent's private notes as well.
	Scope           string
	Kind            string
	IncludeReplaced bool
	Limit           int
}

// ListNotes returns notes newest first, current ones only unless asked.
func (db *DB) ListNotes(f NoteFilter) ([]Note, error) {
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	where := []string{"(scope = ? OR scope = ?)"}
	args := []any{ScopeShared, firstNonEmpty(f.Scope, ScopeShared)}
	if !f.IncludeReplaced {
		where = append(where, "superseded_by = ''")
	}
	if f.Kind != "" {
		where = append(where, "kind = ?")
		args = append(args, f.Kind)
	}
	args = append(args, limit)

	return db.queryNotes(`SELECT id, author, scope, kind, title, body, tags,
		superseded_by, created_at FROM shared_notes
		WHERE `+strings.Join(where, " AND ")+`
		ORDER BY created_at DESC LIMIT ?`, args...)
}

// SearchNotes runs a full-text search over current notes.
//
// The query is passed to FTS5 as a quoted phrase rather than raw: a bare user
// string can contain FTS operators and would either fail with a syntax error or
// silently mean something else than the caller typed.
func (db *DB) SearchNotes(query, scope string, limit int) ([]Note, error) {
	if strings.TrimSpace(query) == "" {
		return db.ListNotes(NoteFilter{Scope: scope, Limit: limit})
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	phrase := `"` + strings.ReplaceAll(query, `"`, `""`) + `"`

	return db.queryNotes(`SELECT n.id, n.author, n.scope, n.kind, n.title, n.body,
		n.tags, n.superseded_by, n.created_at
		FROM shared_notes_fts
		JOIN shared_notes n ON n.rowid = shared_notes_fts.rowid
		WHERE shared_notes_fts MATCH ? AND n.superseded_by = '' AND (n.scope = ? OR n.scope = ?)
		ORDER BY rank
		LIMIT ?`, phrase, ScopeShared, firstNonEmpty(scope, ScopeShared), limit)
}

func (db *DB) queryNotes(q string, args ...any) ([]Note, error) {
	rows, err := db.conn.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("query notes: %w", err)
	}
	defer rows.Close()

	out := []Note{}
	for rows.Next() {
		var n Note
		var created string
		if err := rows.Scan(&n.ID, &n.Author, &n.Scope, &n.Kind, &n.Title,
			&n.Body, &n.Tags, &n.SupersededBy, &created); err != nil {
			return nil, fmt.Errorf("scan note: %w", err)
		}
		n.CreatedAt = parseTS(created)
		out = append(out, n)
	}
	return out, rows.Err()
}

// RenderNotes formats current notes as markdown, grouped by kind. Agents read
// files far more naturally than they query SQL, so this is written to disk for
// them; the database stays the source of truth and the file is regenerated.
func RenderNotes(notes []Note) string {
	var b strings.Builder
	b.WriteString("# Shared notes\n\n")
	b.WriteString("> Generated from the shared coordination database. Do not edit by hand —\n")
	b.WriteString("> use `bomclaw note add`. Edits here are overwritten on the next write.\n")

	byKind := map[string][]Note{}
	for _, n := range notes {
		byKind[n.Kind] = append(byKind[n.Kind], n)
	}
	kinds := make([]string, 0, len(byKind))
	for k := range byKind {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)

	if len(kinds) == 0 {
		b.WriteString("\n_No notes recorded yet._\n")
		return b.String()
	}

	for _, kind := range kinds {
		fmt.Fprintf(&b, "\n## %s\n", kind)
		for _, n := range byKind[kind] {
			fmt.Fprintf(&b, "\n### %s\n", n.Title)
			fmt.Fprintf(&b, "\n_%s · %s_", n.Author, n.CreatedAt.Local().Format("2006-01-02"))
			if n.Tags != "" {
				fmt.Fprintf(&b, " · `%s`", n.Tags)
			}
			b.WriteString("\n")
			if n.Body != "" {
				fmt.Fprintf(&b, "\n%s\n", n.Body)
			}
		}
	}
	return b.String()
}

// DefaultNotesFile is where the rendered markdown lands when config says
// nothing. It sits next to the shared git repo the agents already use.
func DefaultNotesFile() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "goterm-shared", "NOTES.md")
}

// WriteNotesFile regenerates the markdown view of current shared notes.
//
// Written atomically: an agent may be reading the file while another writes it,
// and a half-written NOTES.md would be worse than a stale one.
func (db *DB) WriteNotesFile(path string) error {
	if path == "" {
		path = DefaultNotesFile()
	}
	notes, err := db.ListNotes(NoteFilter{Limit: 500})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("notes file: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(RenderNotes(notes)), 0644); err != nil {
		return fmt.Errorf("notes file: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("notes file: %w", err)
	}
	return nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
