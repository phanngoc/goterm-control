package transcript

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Writer appends transcript events as JSONL to per-session files.
type Writer struct {
	dir string
}

// NewWriter creates a transcript writer that stores files under dir.
func NewWriter(dir string) *Writer {
	return &Writer{dir: dir}
}

// Append writes a single event to the session's transcript file.
func (w *Writer) Append(sessionID string, event Event) error {
	if err := os.MkdirAll(w.dir, 0755); err != nil {
		return fmt.Errorf("mkdir transcripts: %w", err)
	}

	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now()
	}
	if event.SessionID == "" {
		event.SessionID = sessionID
	}

	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}

	path := w.filePath(sessionID)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open transcript: %w", err)
	}
	defer f.Close()

	_, err = f.Write(append(data, '\n'))
	return err
}

// AppendAll writes multiple events to the session's transcript file.
func (w *Writer) AppendAll(sessionID string, events []Event) error {
	for _, ev := range events {
		if err := w.Append(sessionID, ev); err != nil {
			return err
		}
	}
	return nil
}

func (w *Writer) filePath(sessionID string) string {
	return filepath.Join(w.dir, sessionID+".jsonl")
}
