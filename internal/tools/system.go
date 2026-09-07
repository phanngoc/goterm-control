package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// SystemTool reports on the host and manages its processes. The queries
// themselves are platform-specific (system_darwin.go, system_windows.go,
// system_other.go); this file holds only the JSON plumbing.
type SystemTool struct{}

func (s *SystemTool) Info(ctx context.Context, _ json.RawMessage) (string, error) {
	return hostInfo(ctx)
}

type listProcessInput struct {
	Filter string `json:"filter"`
	SortBy string `json:"sort_by"`
}

func (s *SystemTool) Processes(ctx context.Context, raw json.RawMessage) (string, error) {
	var inp listProcessInput
	if err := json.Unmarshal(raw, &inp); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	return listProcesses(ctx, inp.Filter, inp.SortBy)
}

type killInput struct {
	PID    int    `json:"pid"`
	Name   string `json:"name"`
	Signal string `json:"signal"`
}

func (s *SystemTool) Kill(ctx context.Context, raw json.RawMessage) (string, error) {
	var inp killInput
	if err := json.Unmarshal(raw, &inp); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}

	force := inp.Signal == "KILL"

	if inp.PID > 0 {
		return killByPID(ctx, inp.PID, force)
	}
	if inp.Name != "" {
		return killByName(ctx, inp.Name, force)
	}
	return "", fmt.Errorf("provide either pid or name")
}

// filterProcessTable keeps the header row and the rows matching filter, and
// caps an unfiltered listing so a full process table does not flood a turn.
// Shared by the platform implementations, which differ only in how they
// produce the table.
func filterProcessTable(out string, filter string) string {
	lines := strings.Split(out, "\n")
	if len(lines) == 0 {
		return "No processes found"
	}

	result := lines[0] + "\n"
	count := 0
	for _, line := range lines[1:] {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if filter != "" && !strings.Contains(strings.ToLower(line), strings.ToLower(filter)) {
			continue
		}
		result += line + "\n"
		count++
		if count >= 30 && filter == "" {
			result += fmt.Sprintf("... (showing top 30 of %d processes)", len(lines)-1)
			break
		}
	}

	return strings.TrimRight(result, "\n")
}
