// Package memory implements an openclaw-style persistent memory system for
// the bot: plain markdown files inside the agent workspace (MEMORY.md for
// curated long-term memory, memory/YYYY-MM-DD.md for append-only daily notes).
//
// The Claude CLI subprocess has full tool access in the workspace, so the
// agent itself reads, writes, and greps these files. This package only owns
// the file layout, the context block injected into new sessions, the flush
// prompt, and the rotation schedule — orchestration lives in the bot handler.
package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Config controls the persistent memory system.
type Config struct {
	Enabled             bool
	Dir                 string        // memory root; normally the claude workspace
	MaxFileChars        int           // per embedded file cap
	MaxTotalChars       int           // cap for the whole injected block
	FlushPrompt         string        // custom flush prompt ("" = default)
	SoftThresholdTokens int           // context tokens that trigger an early flush
	FlushTimeout        time.Duration // max duration of a flush turn
}

// Manager owns the markdown memory files inside the agent workspace.
type Manager struct {
	cfg Config
	loc *time.Location
}

func NewManager(cfg Config) *Manager {
	return &Manager{cfg: cfg, loc: time.Local}
}

// Enabled reports whether memory is active. Safe on a nil manager.
func (m *Manager) Enabled() bool { return m != nil && m.cfg.Enabled && m.cfg.Dir != "" }

func (m *Manager) SoftThresholdTokens() int { return m.cfg.SoftThresholdTokens }

func (m *Manager) FlushTimeout() time.Duration { return m.cfg.FlushTimeout }

// MemoryFilePath returns the absolute path of MEMORY.md.
func (m *Manager) MemoryFilePath() string { return filepath.Join(m.cfg.Dir, "MEMORY.md") }

// NotesDir returns the absolute path of the daily-notes directory.
func (m *Manager) NotesDir() string { return filepath.Join(m.cfg.Dir, "memory") }

// TodayNotePath returns the absolute path of today's daily note.
func (m *Manager) TodayNotePath(now time.Time) string {
	return filepath.Join(m.NotesDir(), m.dayStamp(now)+".md")
}

func (m *Manager) dayStamp(t time.Time) string { return t.In(m.loc).Format("2006-01-02") }

// relNote is the workspace-relative daily-note path used inside prompts,
// e.g. "memory/2026-07-02.md" — the CLI runs with the workspace as cwd.
func (m *Manager) relNote(t time.Time) string { return "memory/" + m.dayStamp(t) + ".md" }

// Bootstrap creates the memory directory and seeds MEMORY.md if missing.
// It never overwrites existing files.
func (m *Manager) Bootstrap() error {
	if !m.Enabled() {
		return nil
	}
	if err := os.MkdirAll(m.NotesDir(), 0755); err != nil {
		return fmt.Errorf("create memory dir: %w", err)
	}
	path := m.MemoryFilePath()
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	if err := os.WriteFile(path, []byte(memorySeed), 0644); err != nil {
		return fmt.Errorf("seed MEMORY.md: %w", err)
	}
	return nil
}

// BuildContext returns the memory block appended to the system prompt of a
// brand-new session: usage policy + MEMORY.md + today's + yesterday's notes.
// Returns "" when memory is disabled.
func (m *Manager) BuildContext(now time.Time) string {
	if !m.Enabled() {
		return ""
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf(policyHeader, m.relNote(now)))

	sb.WriteString("\n### MEMORY.md\n")
	sb.WriteString(m.readCapped(m.MemoryFilePath(), "MEMORY.md", "(empty)"))

	sb.WriteString(fmt.Sprintf("\n\n### Today (%s)\n", m.relNote(now)))
	sb.WriteString(m.readCapped(m.TodayNotePath(now), m.relNote(now), "(no notes yet)"))

	yesterday := now.AddDate(0, 0, -1)
	sb.WriteString(fmt.Sprintf("\n\n### Yesterday (%s)\n", m.relNote(yesterday)))
	sb.WriteString(m.readCapped(m.TodayNotePath(yesterday), m.relNote(yesterday), "(no notes)"))
	sb.WriteString("\n")

	out := sb.String()
	if max := m.cfg.MaxTotalChars; max > 0 {
		if r := []rune(out); len(r) > max {
			out = string(r[:max]) + "\n[memory truncated — read MEMORY.md and memory/ for full contents]\n"
		}
	}
	return out
}

// readCapped reads a file trimmed and capped at MaxFileChars, appending a
// truncation marker that points the agent at the on-disk file.
func (m *Manager) readCapped(path, relPath, empty string) string {
	data, err := os.ReadFile(path)
	content := strings.TrimSpace(string(data))
	if err != nil || content == "" {
		return empty
	}
	if max := m.cfg.MaxFileChars; max > 0 {
		if r := []rune(content); len(r) > max {
			return string(r[:max]) + fmt.Sprintf("\n[truncated — read %s for the rest]", relPath)
		}
	}
	return content
}

// AppendNote appends a timestamped bullet to today's daily note, creating the
// file (with a date header) and directory as needed. Used by /remember.
func (m *Manager) AppendNote(now time.Time, text string) error {
	if !m.Enabled() {
		return fmt.Errorf("memory is disabled")
	}
	if err := os.MkdirAll(m.NotesDir(), 0755); err != nil {
		return fmt.Errorf("create memory dir: %w", err)
	}
	path := m.TodayNotePath(now)

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		return err
	}
	entry := ""
	if st.Size() == 0 {
		entry = "# " + m.dayStamp(now) + "\n\n"
	}
	entry += fmt.Sprintf("- %s %s\n", now.In(m.loc).Format("15:04"), strings.TrimSpace(text))
	_, err = f.WriteString(entry)
	return err
}

// Stats summarises the memory store for the /memory command.
type Stats struct {
	Dir             string
	MemoryMDBytes   int64
	MemoryMDPreview string
	DailyNoteCount  int
	TodayBytes      int64
}

const previewChars = 600

// Stats returns a summary of the memory files on disk.
func (m *Manager) Stats(now time.Time) (Stats, error) {
	st := Stats{Dir: m.cfg.Dir}
	if !m.Enabled() {
		return st, fmt.Errorf("memory is disabled")
	}

	if data, err := os.ReadFile(m.MemoryFilePath()); err == nil {
		st.MemoryMDBytes = int64(len(data))
		preview := strings.TrimSpace(string(data))
		if r := []rune(preview); len(r) > previewChars {
			preview = string(r[:previewChars]) + "..."
		}
		st.MemoryMDPreview = preview
	}

	if entries, err := os.ReadDir(m.NotesDir()); err == nil {
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
				st.DailyNoteCount++
			}
		}
	}

	if fi, err := os.Stat(m.TodayNotePath(now)); err == nil {
		st.TodayBytes = fi.Size()
	}
	return st, nil
}
