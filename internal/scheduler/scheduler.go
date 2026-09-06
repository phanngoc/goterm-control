// Package scheduler fires schedules from the shared coordination database.
//
// It follows the rule the design calls "automations decide when, tasks record
// what happened": the scheduler itself never runs a model. When a schedule is
// due it either materialises one ordinary task row (payload `agent`) — which
// then goes through claim, lease, fencing and the run cap exactly like a task
// a peer queued — or runs a shell command in-process (payload `command`).
//
// Every gateway on the machine runs one of these against the same database.
// Firing is a compare-and-set on schedules.next_run_at (coord.ClaimSchedule),
// so two gateways that both see a due schedule produce one run. No lock, no
// leader, and a gateway that is down simply does not fire — the other does.
package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ngocp/goterm-control/internal/coord"
)

// Config tunes the loop. Zero values take the defaults noted.
type Config struct {
	AgentID        string
	Tick           time.Duration // how often to look for due schedules; default 30s
	CommandTimeout time.Duration // cap on a command payload without timeout_s; default 60s
	AlertCooldown  time.Duration // min gap between failure alerts for one schedule; default 1h
	// MissedAfter is how late a due schedule may be before it counts as missed
	// (the gateway was down). A missed schedule with skip_missed set re-arms
	// instead of running. Default 2×Tick, so a schedule found on the very next
	// tick is never "missed".
	MissedAfter time.Duration
}

func (c *Config) defaults() {
	if c.Tick <= 0 {
		c.Tick = 30 * time.Second
	}
	if c.CommandTimeout <= 0 {
		c.CommandTimeout = time.Minute
	}
	if c.AlertCooldown <= 0 {
		c.AlertCooldown = time.Hour
	}
	if c.MissedAfter <= 0 {
		c.MissedAfter = 2 * c.Tick
	}
}

// Scheduler is the loop. Create with New, wire SetNotify/SetWake, then Start.
type Scheduler struct {
	db  *coord.DB
	cfg Config

	notify    func(text string)   // deliver a line to the owner (Telegram); nil = log only
	wake      func(t *coord.Task) // ring the runner(s) for a task just created
	now       func() time.Time    // test seam
	poke      chan struct{}       // Poke → run a tick now
	fires     sync.WaitGroup      // in-flight firings and the loop itself
	mu        sync.Mutex          // guards lastAlert
	lastAlert map[string]time.Time
}

// New builds a scheduler. It does nothing until Start.
func New(db *coord.DB, cfg Config) *Scheduler {
	cfg.defaults()
	return &Scheduler{
		db:        db,
		cfg:       cfg,
		now:       time.Now,
		poke:      make(chan struct{}, 1),
		lastAlert: map[string]time.Time{},
	}
}

// SetNotify installs the delivery path for results and alerts.
func (s *Scheduler) SetNotify(fn func(text string)) { s.notify = fn }

// SetWake installs the doorbell rung after an agent task is materialised.
func (s *Scheduler) SetWake(fn func(t *coord.Task)) { s.wake = fn }

// Poke asks for a tick now (a `schedule run-now` from the CLI, for example).
func (s *Scheduler) Poke() {
	if s == nil {
		return // schedules disabled on this gateway; the row still changed
	}
	select {
	case s.poke <- struct{}{}:
	default:
	}
}

// Start runs the loop until ctx ends. The first tick is immediate: after a
// restart, whatever came due while the gateway was down runs (once) right away.
func (s *Scheduler) Start(ctx context.Context) {
	log.Printf("scheduler: firing schedules as %s every %s", s.cfg.AgentID, s.cfg.Tick)
	s.fires.Add(1)
	go func() {
		defer s.fires.Done()
		t := time.NewTicker(s.cfg.Tick)
		defer t.Stop()
		s.Tick(ctx)
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			case <-s.poke:
			}
			s.Tick(ctx)
		}
	}()
}

// Wait blocks until the loop has stopped and every in-flight firing has ended.
func (s *Scheduler) Wait() { s.fires.Wait() }

// Tick is one pass: fire what is due, then settle agent runs whose task has
// finished. Firings run in their own goroutines so a slow command does not
// hold the next tick; Wait joins them. Exported so tests can drive the clock.
func (s *Scheduler) Tick(ctx context.Context) {
	now := s.now()
	due, err := s.db.DueSchedules(now)
	if err != nil {
		log.Printf("scheduler: %v", err)
	}
	for i := range due {
		sc := due[i]
		if sc.OwnerAgent != "" && sc.OwnerAgent != s.cfg.AgentID {
			continue
		}
		next, err := NextRun(sc.Kind, sc.Spec, sc.TZ, now)
		if err != nil {
			// A spec that no longer parses cannot be scheduled. Disable it
			// rather than log the same error every 30 seconds forever.
			log.Printf("scheduler: %s: %v — disabling", sc.Name, err)
			_ = s.db.SetScheduleEnabled(sc.ID, false, time.Time{})
			s.alert(&sc, fmt.Sprintf("⛔ schedule %s disabled: %v", sc.Name, err), true)
			continue
		}
		won, err := s.db.ClaimSchedule(sc.ID, sc.NextRaw(), now, next)
		if err != nil {
			log.Printf("scheduler: %v", err)
			continue
		}
		if !won {
			continue // the other gateway has it
		}
		missed := now.Sub(sc.NextRunAt) > s.cfg.MissedAfter
		s.fires.Add(1)
		go func() {
			defer s.fires.Done()
			s.fire(ctx, &sc, now, missed)
		}()
	}
	s.settle(ctx, now)
}

// fire is one firing of a schedule this gateway won.
func (s *Scheduler) fire(ctx context.Context, sc *coord.Schedule, now time.Time, missed bool) {
	if missed && sc.SkipMissed {
		log.Printf("scheduler: %s was due %s ago while no gateway ran; skip_missed — re-armed for %s",
			sc.Name, now.Sub(sc.NextRunAt).Round(time.Second), nextOf(s.db, sc.ID))
		_, _ = s.db.RecordScheduleRun(coord.ScheduleRun{
			ScheduleID: sc.ID, StartedAt: now, EndedAt: now, Status: coord.ScheduleRunSkipped,
			Output: fmt.Sprintf("due %s, found %s; skip_missed", sc.NextRunAt.Format(time.RFC3339), now.Format(time.RFC3339)),
		})
		_ = s.db.ScheduleSucceeded(sc.ID, coord.ScheduleRunSkipped, now)
		return
	}
	if missed {
		log.Printf("scheduler: %s was due %s ago while no gateway ran — catching up once",
			sc.Name, now.Sub(sc.NextRunAt).Round(time.Second))
	}
	switch sc.PayloadKind {
	case coord.PayloadAgent:
		s.fireAgent(sc, now)
	case coord.PayloadCommand:
		s.fireCommand(ctx, sc, now)
	default:
		s.failed(sc, now, fmt.Sprintf("unknown payload kind %q", sc.PayloadKind))
	}
}

// fireAgent materialises the task. The schedule run stays pending until the
// task reaches a terminal state; settle() closes it.
func (s *Scheduler) fireAgent(sc *coord.Schedule, now time.Time) {
	var p coord.AgentPayload
	if err := json.Unmarshal(sc.Payload, &p); err != nil {
		s.failed(sc, now, "agent payload: "+err.Error())
		return
	}
	to := p.To
	if to == "" {
		to = sc.OwnerAgent
	}
	task, err := s.db.CreateTask(coord.NewTask{
		CreatedBy:  "schedule:" + sc.Name,
		AssignedTo: to,
		Title:      p.Title,
		Body:       agentBody(sc, &p),
		Kind:       coord.KindScheduled,
		ScheduleID: sc.ID,
	})
	if err != nil {
		s.failed(sc, now, "create task: "+err.Error())
		return
	}
	if _, err := s.db.RecordScheduleRun(coord.ScheduleRun{
		ScheduleID: sc.ID, TaskID: task.ID, StartedAt: now, Status: coord.ScheduleRunPending,
	}); err != nil {
		log.Printf("scheduler: %s: %v", sc.Name, err)
	}
	log.Printf("scheduler: %s → task %s (%s)", sc.Name, task.ID, orAny(to, "any agent"))
	if s.wake != nil {
		s.wake(task)
	}
}

// agentBody is the task body a scheduled agent task carries: the payload body
// plus a line saying where it came from, so the agent does not go looking for
// a requester to report to.
func agentBody(sc *coord.Schedule, p *coord.AgentPayload) string {
	var b strings.Builder
	if p.Body != "" {
		b.WriteString(p.Body)
		b.WriteString("\n\n")
	}
	fmt.Fprintf(&b, "(Created by schedule %q — %s. Nobody is waiting in a chat: "+
		"finish with `bomclaw task done --result ...` and your result is delivered to the owner", sc.Name, Describe(sc))
	if p.Quiet {
		b.WriteString(" — or rather recorded, this schedule is quiet")
	}
	b.WriteString(".)")
	return b.String()
}

// fireCommand runs the shell command with a timeout and records the outcome.
func (s *Scheduler) fireCommand(ctx context.Context, sc *coord.Schedule, now time.Time) {
	var p coord.CommandPayload
	if err := json.Unmarshal(sc.Payload, &p); err != nil {
		s.failed(sc, now, "command payload: "+err.Error())
		return
	}
	timeout := s.cfg.CommandTimeout
	if p.TimeoutS > 0 {
		timeout = time.Duration(p.TimeoutS) * time.Second
	}
	out, code, err := runCommand(ctx, sc, &p, timeout)
	ended := s.now()
	status := coord.ScheduleRunOK
	if err != nil || code != 0 {
		status = coord.ScheduleRunFailed
		if err != nil {
			out = strings.TrimRight(out, "\n") + "\n[" + err.Error() + "]"
		}
	}
	if _, rerr := s.db.RecordScheduleRun(coord.ScheduleRun{
		ScheduleID: sc.ID, StartedAt: now, EndedAt: ended, Status: status, ExitCode: code, Output: out,
	}); rerr != nil {
		log.Printf("scheduler: %s: %v", sc.Name, rerr)
	}
	if status == coord.ScheduleRunOK {
		log.Printf("scheduler: %s ok in %s", sc.Name, ended.Sub(now).Round(time.Millisecond))
		_ = s.db.ScheduleSucceeded(sc.ID, coord.ScheduleRunOK, ended)
		return
	}
	s.applyFailure(sc, ended, fmt.Sprintf("exit %d", code), out)
}

// runCommand executes `sh -c cmd` in its own process group so a timeout kills
// what the command spawned, not just the shell.
func runCommand(ctx context.Context, sc *coord.Schedule, p *coord.CommandPayload, timeout time.Duration) (string, int, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", p.Cmd)
	if p.Cwd != "" {
		cmd.Dir = expandHome(p.Cwd)
	}
	cmd.Env = append(os.Environ(), "BOMCLAW_SCHEDULE="+sc.Name, "BOMCLAW_SCHEDULE_ID="+sc.ID)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		var exit *exec.ExitError
		switch {
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			return string(out), -1, fmt.Errorf("timed out after %s", timeout)
		case errors.As(err, &exit):
			code = exit.ExitCode()
			err = nil
		default:
			return string(out), -1, err
		}
	}
	return string(out), code, err
}

// settle closes pending agent runs whose task has reached a terminal state.
// Any gateway may do it; the compare-and-set in SettleScheduleRun makes sure
// only one applies the outcome.
func (s *Scheduler) settle(ctx context.Context, now time.Time) {
	pending, err := s.db.PendingScheduleRuns()
	if err != nil {
		log.Printf("scheduler: %v", err)
		return
	}
	for _, run := range pending {
		if ctx.Err() != nil {
			return
		}
		task, err := s.db.GetTask(run.TaskID)
		if err != nil {
			// The task row is gone; nothing will ever finish it.
			if won, _ := s.db.SettleScheduleRun(run.ID, coord.ScheduleRunFailed, "task missing: "+err.Error(), now); won {
				if sc, gerr := s.db.GetSchedule(run.ScheduleID); gerr == nil {
					s.applyFailure(sc, now, "task missing", "")
				}
			}
			continue
		}
		var status string
		switch task.State {
		case coord.TaskCompleted:
			status = coord.ScheduleRunOK
		case coord.TaskFailed, coord.TaskCanceled, coord.TaskRejected:
			status = coord.ScheduleRunFailed
		default:
			continue // still working, blocked, or waiting to be claimed
		}
		won, err := s.db.SettleScheduleRun(run.ID, status, task.Result, now)
		if err != nil {
			log.Printf("scheduler: %v", err)
			continue
		}
		if !won {
			continue
		}
		sc, err := s.db.GetSchedule(run.ScheduleID)
		if err != nil {
			continue // schedule deleted under a live run; the run is closed, that is enough
		}
		if status == coord.ScheduleRunOK {
			_ = s.db.ScheduleSucceeded(sc.ID, coord.ScheduleRunOK, now)
			s.deliver(sc, task)
			continue
		}
		reason := task.State
		if task.FailReason != "" {
			reason += " (" + task.FailReason + ")"
		}
		s.applyFailure(sc, now, "task "+reason, task.Result)
	}
}

// deliver sends a finished agent task's result to the owner unless the payload
// asked for quiet. The result IS the point of a scheduled task; a report nobody
// reads is a wasted model call.
func (s *Scheduler) deliver(sc *coord.Schedule, task *coord.Task) {
	var p coord.AgentPayload
	_ = json.Unmarshal(sc.Payload, &p)
	if p.Quiet || s.notify == nil {
		return
	}
	result := strings.TrimSpace(task.Result)
	if result == "" {
		result = "(finished without a result)"
	}
	s.notify(fmt.Sprintf("📅 %s\n\n%s", sc.Name, truncate(result, 3500)))
}

// failed records a firing that never got off the ground (bad payload, task
// creation refused) and applies the ladder.
func (s *Scheduler) failed(sc *coord.Schedule, now time.Time, why string) {
	_, _ = s.db.RecordScheduleRun(coord.ScheduleRun{
		ScheduleID: sc.ID, StartedAt: now, EndedAt: now, Status: coord.ScheduleRunFailed, ExitCode: -1, Output: why,
	})
	s.applyFailure(sc, now, why, "")
}

// applyFailure climbs the ladder and tells the owner when the design says to:
// at AlertAfterFailures with a cooldown, and always when the schedule turns
// itself off.
func (s *Scheduler) applyFailure(sc *coord.Schedule, now time.Time, why, output string) {
	updated, disabled, err := s.db.ScheduleFailed(sc.ID, now)
	if err != nil {
		log.Printf("scheduler: %s failed (%s) and the failure could not be recorded: %v", sc.Name, why, err)
		return
	}
	log.Printf("scheduler: %s failed (%s) — %d in a row, next try %s", sc.Name, why,
		updated.ConsecutiveFailures, updated.NextRunAt.Local().Format("15:04:05"))
	tail := strings.TrimSpace(output)
	if tail != "" {
		tail = "\n\n" + truncate(tail, 1200)
	}
	if disabled {
		s.alert(updated, fmt.Sprintf("⛔ schedule %s disabled after %d consecutive failures (last: %s). "+
			"Fix it, then `bomclaw schedule enable %s`.%s", sc.Name, updated.ConsecutiveFailures, why, sc.Name, tail), true)
		return
	}
	if updated.ConsecutiveFailures >= coord.AlertAfterFailures {
		s.alert(updated, fmt.Sprintf("⚠️ schedule %s has failed %d times in a row (last: %s). Retrying at %s; it turns itself off at %d.%s",
			sc.Name, updated.ConsecutiveFailures, why, updated.NextRunAt.Local().Format("15:04"), coord.DisableAfterFailures, tail), false)
	}
}

// alert delivers a failure line, at most once per cooldown per schedule unless
// force (the disable notice always goes out).
func (s *Scheduler) alert(sc *coord.Schedule, text string, force bool) {
	if s.notify == nil {
		log.Printf("scheduler: %s", text)
		return
	}
	now := s.now()
	s.mu.Lock()
	last, seen := s.lastAlert[sc.ID]
	if !force && seen && now.Sub(last) < s.cfg.AlertCooldown {
		s.mu.Unlock()
		return
	}
	s.lastAlert[sc.ID] = now
	s.mu.Unlock()
	s.notify(text)
}

func nextOf(db *coord.DB, id string) string {
	sc, err := db.GetSchedule(id)
	if err != nil || sc.Done() {
		return "never"
	}
	return sc.NextRunAt.Local().Format(time.RFC3339)
}

func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

func orAny(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
