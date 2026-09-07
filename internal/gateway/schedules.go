package gateway

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/ngocp/goterm-control/internal/coord"
	"github.com/ngocp/goterm-control/internal/scheduler"
)

// The schedules.* methods are the dashboard's view of `bomclaw schedule`. They
// write the same rows; the gateway's scheduler loop (any gateway's) does the
// firing on its next tick.

// ScheduleView is a schedule plus its recent runs, for the detail panel.
type ScheduleView struct {
	Schedule *coord.Schedule     `json:"schedule"`
	Runs     []coord.ScheduleRun `json:"runs"`
	When     string              `json:"when"` // human rendering of kind+spec+tz
}

func handleSchedulesList(deps Deps) (json.RawMessage, error) {
	if deps.Coord == nil {
		return nil, errNoCoord()
	}
	all, err := deps.Coord.ListSchedules()
	if err != nil {
		return nil, err
	}
	type row struct {
		coord.Schedule
		When string `json:"when"`
	}
	out := make([]row, 0, len(all))
	for i := range all {
		out = append(out, row{Schedule: all[i], When: scheduler.Describe(&all[i])})
	}
	return json.Marshal(out)
}

type scheduleIDParams struct {
	ID    string `json:"id"` // id or name
	Limit int    `json:"limit,omitempty"`
}

func handleScheduleGet(deps Deps, params json.RawMessage) (json.RawMessage, error) {
	if deps.Coord == nil {
		return nil, errNoCoord()
	}
	var p scheduleIDParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	s, err := deps.Coord.FindSchedule(p.ID)
	if err != nil {
		return nil, err
	}
	runs, err := deps.Coord.ScheduleRuns(s.ID, p.Limit)
	if err != nil {
		return nil, err
	}
	return json.Marshal(ScheduleView{Schedule: s, Runs: runs, When: scheduler.Describe(s)})
}

type scheduleCreateParams struct {
	Name        string          `json:"name"`
	Kind        string          `json:"kind"` // at | every | cron
	Spec        string          `json:"spec"`
	TZ          string          `json:"tz,omitempty"` // default: the machine's zone
	PayloadKind string          `json:"payload_kind"` // agent | command
	Payload     json.RawMessage `json:"payload"`
	OwnerAgent  string          `json:"owner_agent,omitempty"`
	SkipMissed  bool            `json:"skip_missed,omitempty"`
}

func handleScheduleCreate(deps Deps, params json.RawMessage) (json.RawMessage, error) {
	if deps.Coord == nil {
		return nil, errNoCoord()
	}
	var p scheduleCreateParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if p.TZ == "" {
		p.TZ = scheduler.LocalZone()
	}
	next, err := scheduler.NextRun(p.Kind, p.Spec, p.TZ, time.Now())
	if err != nil {
		return nil, err
	}
	if next.IsZero() {
		return nil, fmt.Errorf("%s is already in the past", p.Spec)
	}
	s, err := deps.Coord.CreateSchedule(coord.NewSchedule{
		Name: p.Name, CreatedBy: deps.AgentID, OwnerAgent: p.OwnerAgent,
		Kind: p.Kind, Spec: p.Spec, TZ: p.TZ,
		PayloadKind: p.PayloadKind, Payload: p.Payload,
		SkipMissed: p.SkipMissed, NextRunAt: next,
	})
	if err != nil {
		return nil, err
	}
	return json.Marshal(s)
}

type scheduleToggleParams struct {
	ID      string `json:"id"`
	Enabled bool   `json:"enabled"`
}

func handleScheduleToggle(deps Deps, params json.RawMessage) (json.RawMessage, error) {
	if deps.Coord == nil {
		return nil, errNoCoord()
	}
	var p scheduleToggleParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	s, err := deps.Coord.FindSchedule(p.ID)
	if err != nil {
		return nil, err
	}
	var next time.Time
	if p.Enabled {
		if next, err = scheduler.NextRun(s.Kind, s.Spec, s.TZ, time.Now()); err != nil {
			return nil, err
		}
		if next.IsZero() {
			return nil, fmt.Errorf("%s is a one-shot whose time has passed", s.Name)
		}
	}
	if err := deps.Coord.SetScheduleEnabled(s.ID, p.Enabled, next); err != nil {
		return nil, err
	}
	s, err = deps.Coord.GetSchedule(s.ID)
	if err != nil {
		return nil, err
	}
	return json.Marshal(s)
}

func handleScheduleDelete(deps Deps, params json.RawMessage) (json.RawMessage, error) {
	if deps.Coord == nil {
		return nil, errNoCoord()
	}
	var p scheduleIDParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	s, err := deps.Coord.FindSchedule(p.ID)
	if err != nil {
		return nil, err
	}
	if err := deps.Coord.DeleteSchedule(s.ID); err != nil {
		return nil, err
	}
	return json.Marshal(map[string]bool{"deleted": true})
}

// handleScheduleRun moves the clock to now and pokes the local loop, so a
// "run now" from the dashboard fires within a second rather than a tick.
func handleScheduleRun(deps Deps, params json.RawMessage) (json.RawMessage, error) {
	if deps.Coord == nil {
		return nil, errNoCoord()
	}
	var p scheduleIDParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	s, err := deps.Coord.FindSchedule(p.ID)
	if err != nil {
		return nil, err
	}
	if err := deps.Coord.FireSchedule(s.ID, time.Now()); err != nil {
		return nil, err
	}
	if deps.PokeSchedules != nil {
		deps.PokeSchedules()
	}
	return json.Marshal(map[string]bool{"fired": true})
}
