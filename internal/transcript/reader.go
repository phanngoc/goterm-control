package transcript

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// ReadAll reads all events from a transcript file.
func ReadAll(path string) ([]Event, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open transcript: %w", err)
	}
	defer f.Close()

	var events []Event
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	for scanner.Scan() {
		var ev Event
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			continue // skip malformed lines
		}
		events = append(events, ev)
	}
	return events, scanner.Err()
}

// ReadLast reads the last n events from a transcript file.
func ReadLast(path string, n int) ([]Event, error) {
	all, err := ReadAll(path)
	if err != nil {
		return nil, err
	}
	if len(all) <= n {
		return all, nil
	}
	return all[len(all)-n:], nil
}

// ReadByType reads events of a specific type from a transcript file.
func ReadByType(path string, eventType EventType) ([]Event, error) {
	all, err := ReadAll(path)
	if err != nil {
		return nil, err
	}
	var filtered []Event
	for _, ev := range all {
		if ev.Type == eventType {
			filtered = append(filtered, ev)
		}
	}
	return filtered, nil
}

// ListTranscripts returns all transcript files in a directory.
func ListTranscripts(dir string) ([]string, error) {
	matches, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil {
		return nil, err
	}
	return matches, nil
}
