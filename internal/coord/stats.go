package coord

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ErrNotFound is returned when a lookup by id matches nothing.
var ErrNotFound = errors.New("coord: not found")

// Stats is the roll-up the admin overview renders.
type Stats struct {
	TaskCounts    map[string]int `json:"task_counts"` // state → count
	OpenTasks     int            `json:"open_tasks"`  // not in a terminal state
	UnreadMsgs    int            `json:"unread_messages"`
	Traces24h     int            `json:"traces_24h"`
	Errors24h     int            `json:"errors_24h"`
	Tokens24h     int            `json:"tokens_24h"`
	ToolCalls24h  int            `json:"tool_calls_24h"`
	AvgDurationMS int            `json:"avg_duration_ms"` // over root runs in the window
}

// Stats computes the dashboard roll-up over the last 24 hours.
func (db *DB) Stats() (*Stats, error) {
	s := &Stats{TaskCounts: map[string]int{}}

	rows, err := db.conn.Query(`SELECT state, count(*) FROM tasks GROUP BY state`)
	if err != nil {
		return nil, fmt.Errorf("task counts: %w", err)
	}
	for rows.Next() {
		var state string
		var n int
		if err := rows.Scan(&state, &n); err != nil {
			rows.Close()
			return nil, err
		}
		s.TaskCounts[state] = n
		switch state {
		case TaskSubmitted, TaskWorking, TaskInputRequired, TaskBlocked:
			s.OpenTasks += n
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if err := db.conn.QueryRow(
		`SELECT count(*) FROM agent_messages WHERE read_at = ''`).Scan(&s.UnreadMsgs); err != nil {
		return nil, fmt.Errorf("unread: %w", err)
	}

	since := ts(time.Now().Add(-24 * time.Hour))
	if err := db.conn.QueryRow(`SELECT
			coalesce(sum(CASE WHEN parent_run_id = '' THEN 1 ELSE 0 END), 0),
			coalesce(sum(CASE WHEN status = 'error' THEN 1 ELSE 0 END), 0),
			coalesce(sum(input_tokens + output_tokens), 0),
			coalesce(sum(CASE WHEN run_type = 'tool' THEN 1 ELSE 0 END), 0),
			coalesce(CAST(avg(CASE WHEN parent_run_id = '' AND duration_ms > 0
			                       THEN duration_ms END) AS INTEGER), 0)
		FROM runs WHERE started_at >= ?`, since).Scan(
		&s.Traces24h, &s.Errors24h, &s.Tokens24h, &s.ToolCalls24h, &s.AvgDurationMS); err != nil {
		return nil, fmt.Errorf("trace stats: %w", err)
	}
	return s, nil
}

// GetTask loads one task by id.
func (db *DB) GetTask(id string) (*Task, error) {
	row := db.conn.QueryRow(`SELECT `+taskCols+` FROM tasks WHERE id = ?`, id)
	t, err := scanTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get task %s: %w", id, err)
	}
	return t, nil
}
