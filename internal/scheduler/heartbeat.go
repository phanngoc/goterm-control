package scheduler

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/ngocp/goterm-control/internal/coord"
)

// A heartbeat is a schedule the gateway owns: one row per agent, payload kind
// `heartbeat`, fired only by that agent's gateway. What makes it a heartbeat
// rather than an ordinary agent schedule is what happens BEFORE the task is
// created — a stack of reasons to do nothing, checked in the order of how
// cheap they are — and what happens after: NO_REPLY is delivered to nobody.
//
// OpenClaw's lesson, kept here: a heartbeat is not a liveness ping. It is the
// agent looking at the notes it left itself and acting only when one of them
// asks for it now. An empty notepad costs zero model calls.

// HeartbeatConfig is the gateway's config for its own heartbeat, mirrored from
// config.yaml so this package does not import config.
type HeartbeatConfig struct {
	Enabled     bool
	Every       string // Go duration, e.g. "30m"
	ActiveHours string // "HH:MM-HH:MM" in TZ; "" = always. Overnight ranges wrap.
	TZ          string // IANA zone; "" = the machine's
}

// HeartbeatName is the schedule name for an agent's heartbeat.
func HeartbeatName(agentID string) string { return "heartbeat:" + agentID }

// noReply is what an agent answers when the heartbeat found nothing to do.
const noReply = "NO_REPLY"

// heartbeatMaxContinuations caps a heartbeat task: it is meant to be one look,
// not a long job. Long work belongs in a real task or a schedule.
const heartbeatMaxContinuations = 2

// EnsureHeartbeat brings the agent's heartbeat row in line with config at
// startup. Enabled: create or update it (a changed cadence re-arms from now).
// Disabled: switch an existing row off but keep it, so its run history stays
// readable and `bomclaw heartbeat status` can say "off".
func EnsureHeartbeat(db *coord.DB, agentID string, hc HeartbeatConfig) error {
	name := HeartbeatName(agentID)
	existing, err := db.FindSchedule(name)
	if err != nil {
		existing = nil // not found is the normal first-run case
	}
	if !hc.Enabled {
		if existing != nil && existing.Enabled {
			if err := db.SetScheduleEnabled(existing.ID, false, time.Time{}); err != nil {
				return err
			}
			log.Printf("heartbeat: off (heartbeat.enabled=false) — %s disabled", name)
		}
		return nil
	}
	if hc.Every == "" {
		hc.Every = "30m"
	}
	if hc.TZ == "" {
		hc.TZ = LocalZone()
	}
	if _, err := ParseActiveHours(hc.ActiveHours); err != nil {
		return fmt.Errorf("heartbeat.active_hours: %w", err)
	}
	now := time.Now()
	next, err := NextRun(coord.ScheduleEvery, hc.Every, hc.TZ, now)
	if err != nil {
		return fmt.Errorf("heartbeat.every: %w", err)
	}
	_, changed, err := db.UpsertSystemSchedule(coord.NewSchedule{
		Name: name, CreatedBy: agentID, OwnerAgent: agentID,
		Kind: coord.ScheduleEvery, Spec: hc.Every, TZ: hc.TZ,
		PayloadKind: coord.PayloadHeartbeat,
		Payload:     coord.HeartbeatPayload{ActiveHours: hc.ActiveHours},
		System:      true, SkipMissed: true, // a heartbeat missed during downtime is not owed
		NextRunAt: next,
	})
	if err != nil {
		return err
	}
	if changed {
		log.Printf("heartbeat: every %s, active %s (%s) — first look %s",
			hc.Every, orAny(hc.ActiveHours, "always"), hc.TZ, next.Local().Format("15:04"))
	}
	return nil
}

// activeHours is a parsed "HH:MM-HH:MM" window in minutes since midnight.
type activeHours struct {
	start, end int
	set        bool
}

// ParseActiveHours reads "08:00-23:00". End before start means the window
// wraps past midnight ("22:00-06:00"). Empty means always.
func ParseActiveHours(spec string) (activeHours, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return activeHours{}, nil
	}
	parts := strings.Split(spec, "-")
	if len(parts) != 2 {
		return activeHours{}, fmt.Errorf("%q is not HH:MM-HH:MM", spec)
	}
	var hm [2]int
	for i, p := range parts {
		t, err := time.Parse("15:04", strings.TrimSpace(p))
		if err != nil {
			return activeHours{}, fmt.Errorf("%q is not HH:MM-HH:MM: %w", spec, err)
		}
		hm[i] = t.Hour()*60 + t.Minute()
	}
	if hm[0] == hm[1] {
		return activeHours{}, fmt.Errorf("%q: start and end are the same minute", spec)
	}
	return activeHours{start: hm[0], end: hm[1], set: true}, nil
}

// contains reports whether the wall-clock minute of t (in its own location)
// falls inside the window. The end is exclusive.
func (a activeHours) contains(t time.Time) bool {
	if !a.set {
		return true
	}
	m := t.Hour()*60 + t.Minute()
	if a.start < a.end {
		return m >= a.start && m < a.end
	}
	return m >= a.start || m < a.end // wraps midnight
}

// fireHeartbeat is the heartbeat's firing: skip for the cheapest reason
// available, otherwise create the task. A skip records no run — forty-eight
// "skipped" rows a day would bury the runs that matter — but it does set
// last_status so `heartbeat status` can show why nothing happened.
func (s *Scheduler) fireHeartbeat(sc *coord.Schedule, now time.Time) {
	var p coord.HeartbeatPayload
	if err := json.Unmarshal(sc.Payload, &p); err != nil {
		s.failed(sc, now, "heartbeat payload: "+err.Error())
		return
	}
	skip := func(why string) {
		log.Printf("heartbeat: %s — skipped (%s)", sc.Name, why)
		_ = s.db.ScheduleSucceeded(sc.ID, coord.ScheduleRunSkipped, now)
	}

	loc, err := LoadZone(sc.TZ)
	if err != nil {
		s.failed(sc, now, err.Error())
		return
	}
	hours, err := ParseActiveHours(p.ActiveHours)
	if err != nil {
		s.failed(sc, now, err.Error())
		return
	}
	if !hours.contains(now.In(loc)) {
		skip("outside active hours " + p.ActiveHours)
		return
	}
	scratch, err := s.db.Scratch(sc.OwnerAgent)
	if err != nil {
		s.failed(sc, now, err.Error())
		return
	}
	if strings.TrimSpace(scratch) == "" {
		skip("scratch is empty")
		return
	}
	if s.busy != nil && s.busy() {
		skip("agent is busy")
		return
	}
	open, err := s.db.OpenScheduledTask(sc.ID)
	if err != nil {
		s.failed(sc, now, err.Error())
		return
	}
	if open != nil {
		skip("previous heartbeat task " + open.ID + " is still " + open.State)
		return
	}

	task, err := s.db.CreateTask(coord.NewTask{
		CreatedBy:        "heartbeat",
		AssignedTo:       sc.OwnerAgent,
		Title:            "Heartbeat: look at the scratchpad",
		Body:             heartbeatBody(scratch),
		Kind:             coord.KindHeartbeat,
		ScheduleID:       sc.ID,
		MaxContinuations: heartbeatMaxContinuations,
	})
	if err != nil {
		s.failed(sc, now, "create task: "+err.Error())
		return
	}
	if _, err := s.db.RecordScheduleRun(coord.ScheduleRun{
		ScheduleID: sc.ID, TaskID: task.ID, StartedAt: now, Status: coord.ScheduleRunPending,
	}); err != nil {
		log.Printf("heartbeat: %s: %v", sc.Name, err)
	}
	log.Printf("heartbeat: %s → task %s (%d bytes of scratch)", sc.Name, task.ID, len(scratch))
	if s.wake != nil {
		s.wake(task)
	}
}

// heartbeatBody is the prompt (design §5.6). It is the task body; the runner
// wraps it with the usual task mechanics (`task done`, time budget).
func heartbeatBody(scratch string) string {
	return "This is a heartbeat, not a request from a person. Below are the notes you left " +
		"yourself to follow up on. Read them and act ONLY if one of them needs doing right now.\n\n" +
		"Rules:\n" +
		"- Recurring work is a schedule, not a note: `bomclaw schedule add ...`. Do not keep it here.\n" +
		"- Do not re-derive old work from earlier conversations; the notes are the whole brief.\n" +
		"- When you have handled something, rewrite the notes without it: " +
		"`bomclaw heartbeat scratch --set \"<what is still open>\"` (or `--clear` when nothing is).\n" +
		"- If nothing needs doing now, finish with the result exactly `" + noReply + "` " +
		"(`bomclaw task done --id <task id> --result " + noReply + "`). Nobody is paged for that.\n" +
		"- Anything else you return is sent to the owner on Telegram, so make it worth reading.\n\n" +
		"## Scratchpad\n\n" + strings.TrimSpace(scratch) + "\n"
}

// deliverHeartbeat sends the heartbeat's finding unless it found nothing.
func (s *Scheduler) deliverHeartbeat(sc *coord.Schedule, task *coord.Task) {
	result := strings.TrimSpace(task.Result)
	if isNoReply(result) {
		log.Printf("heartbeat: %s — nothing to report", sc.Name)
		return
	}
	if s.notify == nil {
		return
	}
	s.notify(fmt.Sprintf("💓 %s\n\n%s", sc.OwnerAgent, truncate(result, 3500)))
}

// isNoReply accepts the bare token and the ways a model dresses it up
// (backticks, a trailing explanation). An empty result is also nothing.
func isNoReply(result string) bool {
	r := strings.TrimSpace(result)
	r = strings.Trim(r, "`\"'*")
	return r == "" || strings.HasPrefix(strings.ToUpper(r), noReply)
}
