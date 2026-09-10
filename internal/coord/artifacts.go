package coord

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Artifacts are how a task hands work to another task.
//
// Before this, a parent read its children through childrenSummary, which
// pasted each child's result into the parent's prompt and cut it at 1500
// runes. Anything real — a patch, a report, a generated file — either did not
// survive that cut or never went through it at all: the agents had started
// dropping files in ~/goterm-shared/mailbox/ by hand and naming the path in a
// message, which is this feature with no schema and no lifecycle.
//
// So: the bytes live on disk and the row is a pointer. A parent gathering
// eight children reads an index — id, kind, title, size, a short preview —
// and pulls the full content of only what it needs.

// PreviewRunes is how much of a text artifact is stored inline for the index.
// Long enough to tell two patches apart, short enough that eight of them do
// not crowd out the parent's own brief.
const PreviewRunes = 400

// Artifact kinds. `link` carries a URL instead of a file; the rest are bytes.
const (
	ArtifactDocument = "document" // markdown a human or agent reads (plan, report)
	ArtifactPatch    = "patch"    // a diff meant to be applied
	ArtifactFile     = "file"     // anything else with bytes
	ArtifactLink     = "link"     // a URL: PR, dashboard, external doc
	ArtifactResult   = "result"   // structured output of the task itself
)

var artifactKinds = map[string]bool{
	ArtifactDocument: true, ArtifactPatch: true, ArtifactFile: true,
	ArtifactLink: true, ArtifactResult: true,
}

// Artifact roles on a task. A task's own output is implicit in
// artifacts.task_id; artifact_links records the edges that are not that.
const (
	RoleInput  = "input"
	RoleOutput = "output"
)

// Artifact is one piece of work product, addressable by id.
type Artifact struct {
	ID          string    `json:"id"`
	ContextID   string    `json:"context_id"`
	TaskID      string    `json:"task_id"`
	Kind        string    `json:"kind"`
	Title       string    `json:"title"`
	ContentType string    `json:"content_type,omitempty"`
	Path        string    `json:"path,omitempty"` // relative to the artifacts root
	URL         string    `json:"url,omitempty"`
	Preview     string    `json:"preview,omitempty"`
	Bytes       int64     `json:"bytes"`
	CreatedBy   string    `json:"created_by"`
	CreatedAt   time.Time `json:"created_at"`

	// Role is filled by the queries that join artifact_links, so a caller can
	// tell an input handed down from an output the task produced.
	Role string `json:"role,omitempty"`
}

// NewArtifact is the input to PutArtifact. Either Content or URL is set,
// according to Kind.
type NewArtifact struct {
	TaskID      string
	Kind        string
	Title       string
	Filename    string // suggested name on disk; derived from Title when empty
	ContentType string
	Content     []byte
	URL         string // kind=link only
	CreatedBy   string
}

// DefaultArtifactsDir is where artifact bytes live. It sits in the shared
// working directory rather than beside coord.db because these are files people
// and agents open — the same place NOTES.md and the hand-rolled mailbox/ are.
// BOMCLAW_ARTIFACTS_DIR lets the CLI, which opens the shared database on its
// own, land on the same root the gateway configured. The gateway exports it
// into every subprocess it spawns, exactly as it does BOMCLAW_AGENT_ID.
func DefaultArtifactsDir() string {
	if d := strings.TrimSpace(os.Getenv("BOMCLAW_ARTIFACTS_DIR")); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "goterm-shared", "artifacts")
}

// ArtifactsDir is the root this database writes artifact bytes under.
func (db *DB) ArtifactsDir() string {
	if db.artifactsDir == "" {
		return DefaultArtifactsDir()
	}
	return db.artifactsDir
}

// SetArtifactsDir overrides the root, for config and for tests.
func (db *DB) SetArtifactsDir(dir string) { db.artifactsDir = dir }

// PutArtifact stores work product against a task and returns the row. The task
// must exist: an artifact with no task is a file nobody will ever find again.
func (db *DB) PutArtifact(n NewArtifact) (*Artifact, error) {
	if strings.TrimSpace(n.Title) == "" {
		return nil, fmt.Errorf("coord: artifact title is required")
	}
	if n.Kind == "" {
		n.Kind = ArtifactFile
	}
	if !artifactKinds[n.Kind] {
		return nil, fmt.Errorf("coord: unknown artifact kind %q", n.Kind)
	}
	task, err := db.GetTask(n.TaskID)
	if err != nil {
		return nil, err
	}
	if n.Kind == ArtifactLink {
		if strings.TrimSpace(n.URL) == "" {
			return nil, fmt.Errorf("coord: artifact kind=link needs a url")
		}
	} else if len(n.Content) == 0 {
		return nil, fmt.Errorf("coord: artifact kind=%s needs content", n.Kind)
	}

	a := &Artifact{
		ID:          "a_" + uuid.NewString(),
		ContextID:   task.ContextID,
		TaskID:      task.ID,
		Kind:        n.Kind,
		Title:       strings.TrimSpace(n.Title),
		ContentType: n.ContentType,
		URL:         strings.TrimSpace(n.URL),
		Bytes:       int64(len(n.Content)),
		CreatedBy:   n.CreatedBy,
		CreatedAt:   time.Now(),
	}

	if n.Kind != ArtifactLink {
		rel, err := db.writeArtifactFile(task, n)
		if err != nil {
			return nil, err
		}
		a.Path = rel
		a.Preview = previewOf(n.Content)
	}

	_, err = db.conn.Exec(`INSERT INTO artifacts
		(id, context_id, task_id, kind, title, content_type, path, url, preview, bytes, created_by, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ID, a.ContextID, a.TaskID, a.Kind, a.Title, a.ContentType,
		a.Path, a.URL, a.Preview, a.Bytes, a.CreatedBy, ts(a.CreatedAt))
	if err != nil {
		return nil, fmt.Errorf("put artifact: %w", err)
	}
	_ = db.appendEvent(task.ID, n.CreatedBy, task.State, task.State,
		"artifact "+a.ID+" "+truncateNote(a.Title))
	return a, nil
}

// writeArtifactFile lays the bytes down under <root>/<context>/<task>/ and
// returns the path relative to the root, which is what the row stores — so
// moving the root (a different machine, a restored backup) does not strand
// every artifact.
func (db *DB) writeArtifactFile(task *Task, n NewArtifact) (string, error) {
	dir := filepath.Join(db.ArtifactsDir(), task.ContextID, task.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("artifact dir %s: %w", dir, err)
	}
	name := artifactFilename(n)
	full := filepath.Join(dir, name)
	// Two children writing "report.md" in the same task must not silently
	// overwrite each other.
	for i := 1; ; i++ {
		if _, err := os.Stat(full); os.IsNotExist(err) {
			break
		}
		ext := filepath.Ext(name)
		full = filepath.Join(dir, fmt.Sprintf("%s-%d%s", strings.TrimSuffix(name, ext), i, ext))
		if i > 100 {
			return "", fmt.Errorf("artifact %q: too many name collisions in %s", name, dir)
		}
	}
	if err := os.WriteFile(full, n.Content, 0o644); err != nil {
		return "", fmt.Errorf("write artifact %s: %w", full, err)
	}
	rel, err := filepath.Rel(db.ArtifactsDir(), full)
	if err != nil {
		return "", fmt.Errorf("relative path for %s: %w", full, err)
	}
	return rel, nil
}

var unsafeFilename = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// artifactFilename picks a name on disk. It never trusts the caller's string:
// the task id already scopes the directory, so a "../../.ssh/authorized_keys"
// must collapse to a leaf name, not climb out.
func artifactFilename(n NewArtifact) string {
	raw := n.Filename
	if strings.TrimSpace(raw) == "" {
		raw = n.Title
	}
	raw = filepath.Base(filepath.Clean("/" + raw))
	raw = unsafeFilename.ReplaceAllString(raw, "-")
	raw = strings.Trim(raw, "-.")
	if raw == "" {
		raw = "artifact"
	}
	if len(raw) > 80 {
		raw = raw[:80]
	}
	if filepath.Ext(raw) == "" {
		switch n.Kind {
		case ArtifactDocument:
			raw += ".md"
		case ArtifactPatch:
			raw += ".patch"
		default:
			raw += ".txt"
		}
	}
	return raw
}

// previewOf is the inline snippet. Binary content gets no preview rather than
// a screenful of replacement characters.
func previewOf(content []byte) string {
	if !utf8.Valid(content) {
		return ""
	}
	return truncateRunes(strings.TrimSpace(string(content)), PreviewRunes)
}

const artifactCols = `id, context_id, task_id, kind, title, content_type, path, url, preview, bytes, created_by, created_at`

// GetArtifact returns one artifact row.
func (db *DB) GetArtifact(id string) (*Artifact, error) {
	row := db.conn.QueryRow(`SELECT `+artifactCols+` FROM artifacts WHERE id = ?`, id)
	a, err := scanArtifact(row)
	if err != nil {
		return nil, fmt.Errorf("artifact %s: %w", id, err)
	}
	return a, nil
}

// ReadArtifact returns the bytes. kind=link has none — the caller wants URL.
func (db *DB) ReadArtifact(id string) ([]byte, error) {
	a, err := db.GetArtifact(id)
	if err != nil {
		return nil, err
	}
	if a.Path == "" {
		return nil, fmt.Errorf("artifact %s is a %s, not a file (url: %s)", id, a.Kind, a.URL)
	}
	full := filepath.Join(db.ArtifactsDir(), a.Path)
	b, err := os.ReadFile(full)
	if err != nil {
		return nil, fmt.Errorf("read artifact %s at %s: %w", id, full, err)
	}
	return b, nil
}

// TaskArtifacts lists everything a task produced plus everything handed to it,
// oldest first, with Role set on each.
func (db *DB) TaskArtifacts(taskID string) ([]Artifact, error) {
	rows, err := db.conn.Query(`
		SELECT `+artifactCols+`, 'output' AS role FROM artifacts WHERE task_id = ?
		UNION ALL
		SELECT `+prefixed("a", artifactCols)+`, l.role FROM artifacts a
			JOIN artifact_links l ON l.artifact_id = a.id
			WHERE l.task_id = ? AND NOT (a.task_id = ? AND l.role = 'output')
		ORDER BY created_at`, taskID, taskID, taskID)
	if err != nil {
		return nil, fmt.Errorf("artifacts of %s: %w", taskID, err)
	}
	return scanArtifactsWithRole(rows)
}

// ContextArtifacts lists every artifact in a task tree — Paperclip's
// group_by=parent_task, which is how a parent sees its children's output
// without any child pushing content upward.
func (db *DB) ContextArtifacts(contextID string) ([]Artifact, error) {
	rows, err := db.conn.Query(`SELECT `+artifactCols+`, 'output' AS role
		FROM artifacts WHERE context_id = ? ORDER BY created_at`, contextID)
	if err != nil {
		return nil, fmt.Errorf("artifacts of context %s: %w", contextID, err)
	}
	return scanArtifactsWithRole(rows)
}

// LinkArtifact records an extra edge: usually a parent handing an artifact
// down to a child as an input. Idempotent, so a retried run does not fail.
func (db *DB) LinkArtifact(artifactID, taskID, role string) error {
	if role != RoleInput && role != RoleOutput {
		return fmt.Errorf("coord: artifact role must be %q or %q, got %q", RoleInput, RoleOutput, role)
	}
	if _, err := db.GetArtifact(artifactID); err != nil {
		return err
	}
	_, err := db.conn.Exec(`INSERT INTO artifact_links (artifact_id, task_id, role, created_at)
		VALUES (?, ?, ?, ?) ON CONFLICT DO NOTHING`,
		artifactID, taskID, role, ts(time.Now()))
	if err != nil {
		return fmt.Errorf("link artifact %s to %s: %w", artifactID, taskID, err)
	}
	return nil
}

// prefixed qualifies a column list with a table alias, so the two halves of a
// UNION line up without writing the list twice.
func prefixed(alias, cols string) string {
	parts := strings.Split(cols, ", ")
	for i, c := range parts {
		parts[i] = alias + "." + c
	}
	return strings.Join(parts, ", ")
}

func scanArtifact(s scanner) (*Artifact, error) {
	var a Artifact
	var created string
	if err := s.Scan(&a.ID, &a.ContextID, &a.TaskID, &a.Kind, &a.Title, &a.ContentType,
		&a.Path, &a.URL, &a.Preview, &a.Bytes, &a.CreatedBy, &created); err != nil {
		return nil, err
	}
	a.CreatedAt = parseTS(created)
	return &a, nil
}

func scanArtifactsWithRole(rows interface {
	Next() bool
	Scan(...any) error
	Err() error
	Close() error
}) ([]Artifact, error) {
	defer rows.Close()
	out := []Artifact{}
	for rows.Next() {
		var a Artifact
		var created string
		if err := rows.Scan(&a.ID, &a.ContextID, &a.TaskID, &a.Kind, &a.Title, &a.ContentType,
			&a.Path, &a.URL, &a.Preview, &a.Bytes, &a.CreatedBy, &created, &a.Role); err != nil {
			return nil, err
		}
		a.CreatedAt = parseTS(created)
		out = append(out, a)
	}
	return out, rows.Err()
}
