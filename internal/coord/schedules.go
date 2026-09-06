package coord

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Schedule kinds: how the next firing is computed from the spec.
const (
	ScheduleAt    = "at"    // spec is one RFC3339 instant; fires once
	ScheduleEvery = "every" // spec is a Go duration; fires that long after each firing
	ScheduleCron  = "cron"  // spec is a 5-field cron line (or @daily-style descriptor)
)

// Payload kinds: what a firing does. Neither runs a model in the scheduler.
const (
	PayloadAgent   = "agent"   // materialise one task row; the task runner does the rest
	PayloadCommand = "command" // run a shell command inside the gateway
)

// Schedule run statuses.
const (
	ScheduleRunPending = "pending" // an agent task was created and has not finished
	ScheduleRunOK      = "ok"
	ScheduleRunFailed  = "failed"
	ScheduleRunSkipped = "skipped" // due while the gateway was down and skip_missed was set
)

// Never is the next_run_at of a schedule with no future firing — a one-shot
// that has fired. It sorts after every real timestamp, so the due query never
// returns it, and the column stays NOT NULL.
const Never = "9999-12-31T00:00:00Z"

// Failure ladder (OpenClaw's): after consecutive failures a schedule retries
// sooner than its own cadence, then gives up. A person is told at
// AlertAfterFailures; the schedule turns itself off at DisableAfterFailures.
const (
	AlertAfterFailures   = 2
	DisableAfterFailures = 10
)

// MaxScheduleOutput is how much of a command's output one run keeps.
const MaxScheduleOutput = 8 * 1024

// ErrSystemSchedule means the row is owned by the gateway (a heartbeat) and
// cannot be removed from the CLI; it goes away when its config does.
var ErrSystemSchedule = errors.New("coord: system schedule — change it in config, not here")

// Schedule is one automation: it decides WHEN; schedule_runs and tasks record
// WHAT happened.
type Schedule struct {
	ID                  string          `json:"id"`
	Name                string          `json:"name"`
	CreatedBy           string          `json:"created_by"`
	OwnerAgent          string          `json:"owner_agent,omitempty"` // "" = any gateway fires it
	Kind                string          `json:"kind"`
	Spec                string          `json:"spec"`
	TZ                  string          `json:"tz"`
	PayloadKind         string          `json:"payload_kind"`
	Payload             json.RawMessage `json:"payload"`
	Enabled             bool            `json:"enabled"`
	System              bool            `json:"system"`
	SkipMissed          bool            `json:"skip_missed"`
	NextRunAt           time.Time       `json:"next_run_at"`
	LastRunAt           time.Time       `json:"last_run_at,omitempty"`
	LastStatus          string          `json:"last_status,omitempty"`
	ConsecutiveFailures int             `json:"consecutive_failures"`
	CreatedAt           time.Time       `json:"created_at"`
	UpdatedAt           time.Time       `json:"updated_at"`

	// nextRaw is next_run_at exactly as stored. ClaimSchedule compares on it,
	// so a row written with a different timestamp precision still matches.
	nextRaw string
}

// NextRaw is the stored next_run_at, the token ClaimSchedule needs.
func (s *Schedule) NextRaw() string { return s.nextRaw }

// Done reports a one-shot that has fired (next_run_at = Never).
func (s *Schedule) Done() bool { return s.nextRaw == Never }

// AgentPayload is what an `agent` schedule turns into a task.
type AgentPayload struct {
	Title string `json:"title"`
	Body  string `json:"body,omitempty"`
	To    string `json:"to,omitempty"`    // assign to this agent; "" = owner_agent, or anyone
	Quiet bool   `json:"quiet,omitempty"` // do not deliver the result to the owner's chat
}

// CommandPayload is what a `command` schedule runs.
type CommandPayload struct {
	Cmd      string `json:"cmd"`
	Cwd      string `json:"cwd,omitempty"`
	TimeoutS int    `json:"timeout_s,omitempty"`
}

// ScheduleRun is one firing.
type ScheduleRun struct {
	ID         string    `json:"id"`
	ScheduleID string    `json:"schedule_id"`
	TaskID     string    `json:"task_id,omitempty"`
	StartedAt  time.Time `json:"started_at"`
	EndedAt    time.Time `json:"ended_at,omitempty"`
	Status     string    `json:"status"`
	ExitCode   int       `json:"exit_code"`
	Output     string    `json:"output,omitempty"`
}

// NewSchedule is what it takes to create one. NextRunAt is computed by the
// caller (internal/scheduler knows the calendar; this package only stores it).
type NewSchedule struct {
	Name        string
	CreatedBy   string
	OwnerAgent  string
	Kind        string
	Spec        string
	TZ          string
	PayloadKind string
	Payload     any // AgentPayload, CommandPayload, or a json.RawMessage
	SkipMissed  bool
	System      bool
	NextRunAt   time.Time
}

// CreateSchedule stores a schedule. It validates shape, not calendar: the spec
// must already have produced NextRunAt.
func (db *DB) CreateSchedule(n NewSchedule) (*Schedule, error) {
	n.Name = strings.TrimSpace(n.Name)
	if n.Name == "" {
		return nil, fmt.Errorf("coord: schedule name is required")
	}
	switch n.Kind {
	case ScheduleAt, ScheduleEvery, ScheduleCron:
	default:
		return nil, fmt.Errorf("coord: schedule kind %q must be at, every or cron", n.Kind)
	}
	if strings.TrimSpace(n.Spec) == "" {
		return nil, fmt.Errorf("coord: schedule spec is required")
	}
	if strings.TrimSpace(n.TZ) == "" {
		return nil, fmt.Errorf("coord: schedule tz is required (an IANA zone such as Asia/Ho_Chi_Minh)")
	}
	if n.NextRunAt.IsZero() {
		return nil, fmt.Errorf("coord: schedule has no first run time")
	}
	payload, err := encodePayload(n.PayloadKind, n.Payload)
	if err != nil {
		return nil, err
	}

	now := time.Now()
	s := &Schedule{
		ID:          "sch_" + uuid.NewString(),
		Name:        n.Name,
		CreatedBy:   n.CreatedBy,
		OwnerAgent:  n.OwnerAgent,
		Kind:        n.Kind,
		Spec:        strings.TrimSpace(n.Spec),
		TZ:          n.TZ,
		PayloadKind: n.PayloadKind,
		Payload:     payload,
		Enabled:     true,
		System:      n.System,
		SkipMissed:  n.SkipMissed,
		NextRunAt:   n.NextRunAt,
		CreatedAt:   now,
		UpdatedAt:   now,
		nextRaw:     ts(n.NextRunAt),
	}
	_, err = db.conn.Exec(`INSERT INTO schedules
		(id, name, created_by, owner_agent, kind, spec, tz, payload_kind, payload,
		 enabled, system, skip_missed, next_run_at, last_run_at, last_status,
		 consecutive_failures, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?, '', '', 0, ?, ?)`,
		s.ID, s.Name, s.CreatedBy, s.OwnerAgent, s.Kind, s.Spec, s.TZ, s.PayloadKind, string(s.Payload),
		b2i(s.System), b2i(s.SkipMissed), s.nextRaw, ts(now), ts(now))
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, fmt.Errorf("coord: a schedule named %q already exists", s.Name)
		}
		return nil, fmt.Errorf("create schedule: %w", err)
	}
	return s, nil
}

// encodePayload checks that a payload has what its kind needs and returns it
// as stored JSON. A schedule that cannot fire is a bug found at 08:00, so the
// required fields are checked here rather than then.
func encodePayload(kind string, payload any) (json.RawMessage, error) {
	var raw []byte
	switch p := payload.(type) {
	case nil:
		return nil, fmt.Errorf("coord: schedule payload is required")
	case json.RawMessage:
		raw = p
	case []byte:
		raw = p
	case string:
		raw = []byte(p)
	default:
		var err error
		if raw, err = json.Marshal(p); err != nil {
			return nil, fmt.Errorf("coord: encode payload: %w", err)
		}
	}
	switch kind {
	case PayloadAgent:
		var p AgentPayload
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, fmt.Errorf("coord: agent payload: %w", err)
		}
		if strings.TrimSpace(p.Title) == "" {
			return nil, fmt.Errorf("coord: agent payload needs a title")
		}
	case PayloadCommand:
		var p CommandPayload
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, fmt.Errorf("coord: command payload: %w", err)
		}
		if strings.TrimSpace(p.Cmd) == "" {
			return nil, fmt.Errorf("coord: command payload needs cmd")
		}
	default:
		return nil, fmt.Errorf("coord: payload kind %q must be agent or command", kind)
	}
	return json.RawMessage(raw), nil
}

const scheduleCols = `id, name, created_by, owner_agent, kind, spec, tz, payload_kind, payload,
	enabled, system, skip_missed, next_run_at, last_run_at, last_status,
	consecutive_failures, created_at, updated_at`

// GetSchedule fetches by id.
func (db *DB) GetSchedule(id string) (*Schedule, error) {
	s, err := scanSchedule(db.conn.QueryRow(`SELECT `+scheduleCols+` FROM schedules WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("coord: no schedule %s", id)
	}
	return s, err
}

// FindSchedule accepts an id or a name — the CLI takes either.
func (db *DB) FindSchedule(idOrName string) (*Schedule, error) {
	s, err := scanSchedule(db.conn.QueryRow(
		`SELECT `+scheduleCols+` FROM schedules WHERE id = ? OR name = ? LIMIT 1`, idOrName, idOrName))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("coord: no schedule %q", idOrName)
	}
	return s, err
}

// ListSchedules returns every schedule, soonest first; disabled ones last.
func (db *DB) ListSchedules() ([]Schedule, error) {
	rows, err := db.conn.Query(`SELECT ` + scheduleCols + ` FROM schedules
		ORDER BY enabled DESC, next_run_at, name`)
	if err != nil {
		return nil, fmt.Errorf("list schedules: %w", err)
	}
	defer rows.Close()
	out := []Schedule{}
	for rows.Next() {
		s, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	return out, rows.Err()
}

// DueSchedules returns the enabled schedules whose time has come. The caller
// must still win ClaimSchedule before acting on any of them.
func (db *DB) DueSchedules(now time.Time) ([]Schedule, error) {
	rows, err := db.conn.Query(`SELECT `+scheduleCols+` FROM schedules
		WHERE enabled = 1 AND next_run_at <= ? ORDER BY next_run_at`, ts(now))
	if err != nil {
		return nil, fmt.Errorf("due schedules: %w", err)
	}
	defer rows.Close()
	out := []Schedule{}
	for rows.Next() {
		s, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	return out, rows.Err()
}

// ClaimSchedule is the compare-and-set that makes firing exclusive: it moves
// next_run_at from the value this caller saw (seenNext) to next, and reports
// whether this caller was the one to do it. Two gateways that both found the
// schedule due both call this; the UPDATE matches for exactly one of them.
//
// next is what the caller computed from NOW, not from the old mark — that is
// what turns eight hours of downtime into one catch-up run instead of eight.
// A zero next stores Never (a one-shot that has fired).
func (db *DB) ClaimSchedule(id, seenNext string, now, next time.Time) (bool, error) {
	nextRaw := Never
	if !next.IsZero() {
		nextRaw = ts(next)
	}
	res, err := db.conn.Exec(`UPDATE schedules
		SET next_run_at = ?, last_run_at = ?, updated_at = ?
		WHERE id = ? AND enabled = 1 AND next_run_at = ?`,
		nextRaw, ts(now), ts(now), id, seenNext)
	if err != nil {
		return false, fmt.Errorf("claim schedule %s: %w", id, err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// SetScheduleEnabled turns a schedule on or off. Re-enabling takes a fresh
// next_run_at from the caller so a schedule paused for a week does not fire the
// moment it is switched back on; the failure count resets with it.
func (db *DB) SetScheduleEnabled(id string, enabled bool, next time.Time) error {
	now := ts(time.Now())
	var res sql.Result
	var err error
	if enabled {
		if next.IsZero() {
			return fmt.Errorf("coord: enabling a schedule needs its next run time")
		}
		res, err = db.conn.Exec(`UPDATE schedules
			SET enabled = 1, next_run_at = ?, consecutive_failures = 0, updated_at = ?
			WHERE id = ?`, ts(next), now, id)
	} else {
		res, err = db.conn.Exec(`UPDATE schedules SET enabled = 0, updated_at = ? WHERE id = ?`, now, id)
	}
	if err != nil {
		return fmt.Errorf("set schedule %s enabled=%v: %w", id, enabled, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("coord: no schedule %s", id)
	}
	return nil
}

// DeleteSchedule removes a schedule and its run history. System rows refuse.
func (db *DB) DeleteSchedule(id string) error {
	var system int
	err := db.conn.QueryRow(`SELECT system FROM schedules WHERE id = ?`, id).Scan(&system)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("coord: no schedule %s", id)
	}
	if err != nil {
		return fmt.Errorf("delete schedule %s: %w", id, err)
	}
	if system == 1 {
		return ErrSystemSchedule
	}
	tx, err := db.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM schedule_runs WHERE schedule_id = ?`, id); err != nil {
		return fmt.Errorf("delete schedule runs %s: %w", id, err)
	}
	if _, err := tx.Exec(`DELETE FROM schedules WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete schedule %s: %w", id, err)
	}
	return tx.Commit()
}

// FireSchedule moves a schedule's clock to now so the next tick fires it. It
// does not enable a disabled schedule: the due query ignores those, and
// silently re-enabling something a person switched off would be a surprise.
func (db *DB) FireSchedule(id string, now time.Time) error {
	s, err := db.GetSchedule(id)
	if err != nil {
		return err
	}
	if !s.Enabled {
		return fmt.Errorf("coord: schedule %s is disabled — enable it first", s.Name)
	}
	_, err = db.conn.Exec(`UPDATE schedules SET next_run_at = ?, updated_at = ? WHERE id = ?`,
		ts(now), ts(now), id)
	if err != nil {
		return fmt.Errorf("fire schedule %s: %w", id, err)
	}
	return nil
}

// ScheduleSucceeded records a good firing: the failure streak ends. status is
// ScheduleRunOK or ScheduleRunSkipped. A one-shot is switched off here — not at
// claim time — so a one-shot that failed keeps retrying up the ladder.
func (db *DB) ScheduleSucceeded(id, status string, now time.Time) error {
	_, err := db.conn.Exec(`UPDATE schedules
		SET consecutive_failures = 0, last_status = ?, updated_at = ?,
		    enabled = CASE WHEN kind = 'at' THEN 0 ELSE enabled END
		WHERE id = ?`, status, ts(now), id)
	if err != nil {
		return fmt.Errorf("schedule %s succeeded: %w", id, err)
	}
	return nil
}

// ScheduleFailed records a bad firing and applies the ladder: the streak grows,
// the next run moves to now+Backoff(streak), and at DisableAfterFailures the
// schedule switches itself off. It returns the updated row and whether this
// call is the one that disabled it, so the caller can tell a person once.
func (db *DB) ScheduleFailed(id string, now time.Time) (*Schedule, bool, error) {
	tx, err := db.conn.Begin()
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()

	var failures, enabled int
	if err := tx.QueryRow(`SELECT consecutive_failures, enabled FROM schedules WHERE id = ?`, id).
		Scan(&failures, &enabled); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, false, fmt.Errorf("coord: no schedule %s", id)
		}
		return nil, false, fmt.Errorf("schedule %s failed: %w", id, err)
	}
	failures++
	disable := failures >= DisableAfterFailures
	newEnabled := enabled
	if disable {
		newEnabled = 0
	}
	if _, err := tx.Exec(`UPDATE schedules
		SET consecutive_failures = ?, last_status = ?, next_run_at = ?, enabled = ?, updated_at = ?
		WHERE id = ?`,
		failures, ScheduleRunFailed, ts(now.Add(Backoff(failures))), newEnabled, ts(now), id); err != nil {
		return nil, false, fmt.Errorf("schedule %s failed: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	s, err := db.GetSchedule(id)
	if err != nil {
		return nil, false, err
	}
	return s, disable && enabled == 1, nil
}

// Backoff is the retry ladder: how long after the n-th consecutive failure the
// next attempt waits. It overrides the schedule's own cadence — a daily job
// that fails retries within the hour, not tomorrow — and tops out at an hour.
func Backoff(n int) time.Duration {
	ladder := []time.Duration{30 * time.Second, time.Minute, 5 * time.Minute, 15 * time.Minute, time.Hour}
	if n < 1 {
		n = 1
	}
	if n > len(ladder) {
		n = len(ladder)
	}
	return ladder[n-1]
}

// RecordScheduleRun writes one firing. Output is cut to MaxScheduleOutput.
func (db *DB) RecordScheduleRun(r ScheduleRun) (*ScheduleRun, error) {
	if r.ID == "" {
		r.ID = "sr_" + uuid.NewString()
	}
	if r.StartedAt.IsZero() {
		r.StartedAt = time.Now()
	}
	if r.Status == "" {
		return nil, fmt.Errorf("coord: schedule run needs a status")
	}
	r.Output = cutOutput(r.Output, MaxScheduleOutput)
	ended := ""
	if !r.EndedAt.IsZero() {
		ended = ts(r.EndedAt)
	}
	_, err := db.conn.Exec(`INSERT INTO schedule_runs
		(id, schedule_id, task_id, started_at, ended_at, status, exit_code, output)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.ScheduleID, r.TaskID, ts(r.StartedAt), ended, r.Status, r.ExitCode, r.Output)
	if err != nil {
		return nil, fmt.Errorf("record schedule run: %w", err)
	}
	return &r, nil
}

// SettleScheduleRun closes a pending (task-backed) run. It is a compare-and-set
// on status: whichever gateway's tick notices the task has finished settles the
// run, and only the one that flips pending→status applies the outcome to the
// schedule, so a failure is never counted twice.
func (db *DB) SettleScheduleRun(runID, status, output string, now time.Time) (bool, error) {
	res, err := db.conn.Exec(`UPDATE schedule_runs SET status = ?, ended_at = ?, output = ?
		WHERE id = ? AND status = ?`,
		status, ts(now), cutOutput(output, MaxScheduleOutput), runID, ScheduleRunPending)
	if err != nil {
		return false, fmt.Errorf("settle schedule run %s: %w", runID, err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// PendingScheduleRuns lists runs still waiting on their task.
func (db *DB) PendingScheduleRuns() ([]ScheduleRun, error) {
	rows, err := db.conn.Query(`SELECT id, schedule_id, task_id, started_at, ended_at, status, exit_code, output
		FROM schedule_runs WHERE status = ? AND task_id != '' ORDER BY started_at`, ScheduleRunPending)
	if err != nil {
		return nil, fmt.Errorf("pending schedule runs: %w", err)
	}
	defer rows.Close()
	return scanScheduleRuns(rows)
}

// ScheduleRuns lists a schedule's firings, newest first.
func (db *DB) ScheduleRuns(scheduleID string, limit int) ([]ScheduleRun, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := db.conn.Query(`SELECT id, schedule_id, task_id, started_at, ended_at, status, exit_code, output
		FROM schedule_runs WHERE schedule_id = ? ORDER BY started_at DESC LIMIT ?`, scheduleID, limit)
	if err != nil {
		return nil, fmt.Errorf("schedule runs: %w", err)
	}
	defer rows.Close()
	return scanScheduleRuns(rows)
}

func scanScheduleRuns(rows *sql.Rows) ([]ScheduleRun, error) {
	out := []ScheduleRun{}
	for rows.Next() {
		var r ScheduleRun
		var started, ended string
		if err := rows.Scan(&r.ID, &r.ScheduleID, &r.TaskID, &started, &ended, &r.Status, &r.ExitCode, &r.Output); err != nil {
			return nil, fmt.Errorf("scan schedule run: %w", err)
		}
		r.StartedAt, r.EndedAt = parseTS(started), parseTS(ended)
		out = append(out, r)
	}
	return out, rows.Err()
}

func scanSchedule(s scanner) (*Schedule, error) {
	var sc Schedule
	var payload, next, last, created, updated string
	var enabled, system, skip int
	if err := s.Scan(&sc.ID, &sc.Name, &sc.CreatedBy, &sc.OwnerAgent, &sc.Kind, &sc.Spec, &sc.TZ,
		&sc.PayloadKind, &payload, &enabled, &system, &skip, &next, &last, &sc.LastStatus,
		&sc.ConsecutiveFailures, &created, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		return nil, fmt.Errorf("scan schedule: %w", err)
	}
	sc.Payload = json.RawMessage(payload)
	sc.Enabled, sc.System, sc.SkipMissed = enabled == 1, system == 1, skip == 1
	sc.nextRaw = next
	sc.NextRunAt = parseTS(next)
	sc.LastRunAt = parseTS(last)
	sc.CreatedAt, sc.UpdatedAt = parseTS(created), parseTS(updated)
	return &sc, nil
}

// cutOutput keeps the head and the tail of a long output: a `df -h` wants its
// first lines, a failing build its last ones.
func cutOutput(s string, max int) string {
	if len(s) <= max {
		return s
	}
	half := max / 2
	return s[:half] + fmt.Sprintf("\n…[%d bytes cut]…\n", len(s)-2*half) + s[len(s)-half:]
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
