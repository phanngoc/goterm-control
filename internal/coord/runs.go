package coord

import (
	"fmt"
	"strings"
	"time"
)

// Run types, mirroring LangSmith's vocabulary where it fits this system.
const (
	RunTypeChain  = "chain"  // one agent turn end-to-end
	RunTypeLLM    = "llm"    // a call into the claude/codex CLI
	RunTypeTool   = "tool"   // a tool the model invoked
	RunTypeMemory = "memory" // a memory flush / injection
	RunTypeTask   = "task"   // execution of a handed-off task
)

// Run statuses.
const (
	StatusPending = "pending"
	StatusSuccess = "success"
	StatusError   = "error"
)

// Run is one node of a trace. A trace is every run sharing a trace_id; the
// tree is rebuilt by sorting on DottedOrder, which embeds the whole ancestry.
type Run struct {
	ID           string    `json:"id"`
	TraceID      string    `json:"trace_id"`
	ParentRunID  string    `json:"parent_run_id,omitempty"`
	DottedOrder  string    `json:"dotted_order"`
	AgentID      string    `json:"agent_id"`
	SessionID    string    `json:"session_id,omitempty"`
	ChatID       int64     `json:"chat_id,omitempty"`
	Name         string    `json:"name"`
	RunType      string    `json:"run_type"`
	Status       string    `json:"status"`
	StartedAt    time.Time `json:"started_at"`
	EndedAt      time.Time `json:"ended_at,omitempty"`
	DurationMS   int64     `json:"duration_ms"`
	Inputs       string    `json:"inputs,omitempty"`
	Outputs      string    `json:"outputs,omitempty"`
	Error        string    `json:"error,omitempty"`
	InputTokens  int       `json:"input_tokens"`
	OutputTokens int       `json:"output_tokens"`
	Model        string    `json:"model,omitempty"`
	Provider     string    `json:"provider,omitempty"`
	Tags         string    `json:"tags,omitempty"`

	// Depth is derived from DottedOrder on read so a client can indent the
	// waterfall without parsing the key itself.
	Depth int `json:"depth"`
}

// DottedOrderSegment builds one path segment: a sortable timestamp joined to
// the run id. Concatenating a parent's dotted_order with a child's segment
// yields a key that sorts children under their parent, in start order.
//
// The timestamp layout deliberately carries no "." — the dot is the segment
// separator, so a fractional-second dot inside a segment would corrupt both
// the depth count and any path-prefix matching.
func DottedOrderSegment(start time.Time, id string) string {
	return start.UTC().Format("20060102T150405") +
		fmt.Sprintf("%09d", start.UTC().Nanosecond()) + "Z" + id
}

// InsertRun writes a run at start time, with status pending.
func (db *DB) InsertRun(r *Run) error {
	_, err := db.conn.Exec(`INSERT INTO runs
		(id, trace_id, parent_run_id, dotted_order, agent_id, session_id, chat_id,
		 name, run_type, status, started_at, ended_at, duration_ms,
		 inputs, outputs, error, input_tokens, output_tokens, model, provider, tags)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', 0, ?, '', '', 0, 0, ?, ?, ?)`,
		r.ID, r.TraceID, r.ParentRunID, r.DottedOrder, r.AgentID, r.SessionID, r.ChatID,
		r.Name, r.RunType, StatusPending, ts(r.StartedAt),
		r.Inputs, r.Model, r.Provider, r.Tags)
	if err != nil {
		return fmt.Errorf("insert run %s: %w", r.ID, err)
	}
	return nil
}

// EndRun closes a run out. An empty errMsg means success.
func (db *DB) EndRun(id string, end time.Time, outputs, errMsg string, inTok, outTok int) error {
	status := StatusSuccess
	if errMsg != "" {
		status = StatusError
	}
	_, err := db.conn.Exec(`UPDATE runs SET
			status = ?, ended_at = ?,
			duration_ms = CAST((julianday(?) - julianday(started_at)) * 86400000 AS INTEGER),
			outputs = ?, error = ?, input_tokens = ?, output_tokens = ?
		WHERE id = ?`,
		status, ts(end), ts(end), outputs, errMsg, inTok, outTok, id)
	if err != nil {
		return fmt.Errorf("end run %s: %w", id, err)
	}
	return nil
}

// TraceFilter narrows a trace listing.
type TraceFilter struct {
	AgentID string
	Status  string // "", "success", "error", "pending"
	Search  string // substring of the root run name or inputs
	Limit   int
}

// TraceSummary is a root run plus rolled-up numbers for its whole tree.
type TraceSummary struct {
	Run
	SpanCount   int `json:"span_count"`
	ErrorCount  int `json:"error_count"`
	ToolCount   int `json:"tool_count"`
	TotalTokens int `json:"total_tokens"`
}

// ListTraces returns root runs, newest first, with per-trace aggregates.
func (db *DB) ListTraces(f TraceFilter) ([]TraceSummary, error) {
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}

	where := []string{"r.parent_run_id = ''"}
	args := []any{}
	if f.AgentID != "" {
		where = append(where, "r.agent_id = ?")
		args = append(args, f.AgentID)
	}
	if f.Status != "" {
		where = append(where, "r.status = ?")
		args = append(args, f.Status)
	}
	if f.Search != "" {
		where = append(where, "(r.name LIKE ? OR r.inputs LIKE ?)")
		like := "%" + f.Search + "%"
		args = append(args, like, like)
	}
	args = append(args, limit)

	// The aggregates come from a correlated subquery per root run rather than a
	// GROUP BY over the join: root rows stay one-to-one, so paging by limit
	// counts traces and not spans.
	q := `SELECT
			r.id, r.trace_id, r.parent_run_id, r.dotted_order, r.agent_id,
			r.session_id, r.chat_id, r.name, r.run_type, r.status,
			r.started_at, r.ended_at, r.duration_ms, r.inputs, r.outputs, r.error,
			r.input_tokens, r.output_tokens, r.model, r.provider, r.tags,
			(SELECT count(*) FROM runs s WHERE s.trace_id = r.trace_id),
			(SELECT count(*) FROM runs s WHERE s.trace_id = r.trace_id AND s.status = 'error'),
			(SELECT count(*) FROM runs s WHERE s.trace_id = r.trace_id AND s.run_type = 'tool'),
			(SELECT coalesce(sum(s.input_tokens + s.output_tokens), 0) FROM runs s WHERE s.trace_id = r.trace_id)
		FROM runs r
		WHERE ` + strings.Join(where, " AND ") + `
		ORDER BY r.started_at DESC
		LIMIT ?`

	rows, err := db.conn.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("list traces: %w", err)
	}
	defer rows.Close()

	out := []TraceSummary{}
	for rows.Next() {
		var t TraceSummary
		var started, ended string
		if err := rows.Scan(
			&t.ID, &t.TraceID, &t.ParentRunID, &t.DottedOrder, &t.AgentID,
			&t.SessionID, &t.ChatID, &t.Name, &t.RunType, &t.Status,
			&started, &ended, &t.DurationMS, &t.Inputs, &t.Outputs, &t.Error,
			&t.InputTokens, &t.OutputTokens, &t.Model, &t.Provider, &t.Tags,
			&t.SpanCount, &t.ErrorCount, &t.ToolCount, &t.TotalTokens,
		); err != nil {
			return nil, fmt.Errorf("scan trace: %w", err)
		}
		t.StartedAt = parseTS(started)
		t.EndedAt = parseTS(ended)
		out = append(out, t)
	}
	return out, rows.Err()
}

// GetTrace returns every run in a trace, already ordered so a client can render
// the tree by walking the slice and indenting by Depth.
func (db *DB) GetTrace(traceID string) ([]Run, error) {
	rows, err := db.conn.Query(`SELECT
			id, trace_id, parent_run_id, dotted_order, agent_id, session_id, chat_id,
			name, run_type, status, started_at, ended_at, duration_ms,
			inputs, outputs, error, input_tokens, output_tokens, model, provider, tags
		FROM runs WHERE trace_id = ? ORDER BY dotted_order`, traceID)
	if err != nil {
		return nil, fmt.Errorf("get trace %s: %w", traceID, err)
	}
	defer rows.Close()

	out := []Run{}
	for rows.Next() {
		var r Run
		var started, ended string
		if err := rows.Scan(
			&r.ID, &r.TraceID, &r.ParentRunID, &r.DottedOrder, &r.AgentID,
			&r.SessionID, &r.ChatID, &r.Name, &r.RunType, &r.Status,
			&started, &ended, &r.DurationMS,
			&r.Inputs, &r.Outputs, &r.Error, &r.InputTokens, &r.OutputTokens,
			&r.Model, &r.Provider, &r.Tags,
		); err != nil {
			return nil, fmt.Errorf("scan run: %w", err)
		}
		r.StartedAt = parseTS(started)
		r.EndedAt = parseTS(ended)
		r.Depth = strings.Count(r.DottedOrder, ".")
		out = append(out, r)
	}
	return out, rows.Err()
}

// PurgeRunsBefore drops traces older than cutoff. Traces are debug data, not
// the conversation record — without a trim the file grows without bound.
func (db *DB) PurgeRunsBefore(cutoff time.Time) (int64, error) {
	res, err := db.conn.Exec(`DELETE FROM runs WHERE started_at < ?`, ts(cutoff))
	if err != nil {
		return 0, fmt.Errorf("purge runs: %w", err)
	}
	return res.RowsAffected()
}
