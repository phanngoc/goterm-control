package gateway

import (
	"encoding/json"
	"fmt"
	"log"

	"github.com/ngocp/goterm-control/internal/coord"
)

// errNoCoord explains the one reason every admin method can fail up front.
// Coordination is opt-out, so this only happens when it was turned off or the
// shared database could not be opened at startup.
func errNoCoord() error {
	return fmt.Errorf("coordination database is not available (coord.enabled=false, or it failed to open — see the gateway log)")
}

// --- overview --------------------------------------------------------------

// AdminOverview is everything the admin landing page needs in one round trip.
type AdminOverview struct {
	AgentID string        `json:"agent_id"` // the agent serving this request
	Agents  []coord.Agent `json:"agents"`
	Stats   *coord.Stats  `json:"stats"`
	DBPath  string        `json:"db_path"`
}

func handleAdminOverview(deps Deps) (json.RawMessage, error) {
	if deps.Coord == nil {
		return nil, errNoCoord()
	}
	agents, err := deps.Coord.ListAgents()
	if err != nil {
		return nil, err
	}
	stats, err := deps.Coord.Stats()
	if err != nil {
		return nil, err
	}
	return json.Marshal(AdminOverview{
		AgentID: deps.AgentID,
		Agents:  agents,
		Stats:   stats,
		DBPath:  deps.Coord.Path(),
	})
}

// --- traces ----------------------------------------------------------------

type tracesListParams struct {
	AgentID string `json:"agent_id,omitempty"`
	Status  string `json:"status,omitempty"`
	Search  string `json:"search,omitempty"`
	Limit   int    `json:"limit,omitempty"`
}

func handleTracesList(deps Deps, params json.RawMessage) (json.RawMessage, error) {
	if deps.Coord == nil {
		return nil, errNoCoord()
	}
	var p tracesListParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
	}
	traces, err := deps.Coord.ListTraces(coord.TraceFilter{
		AgentID: p.AgentID, Status: p.Status, Search: p.Search, Limit: p.Limit,
	})
	if err != nil {
		return nil, err
	}
	return json.Marshal(traces)
}

type traceGetParams struct {
	TraceID string `json:"trace_id"`
}

func handleTraceGet(deps Deps, params json.RawMessage) (json.RawMessage, error) {
	if deps.Coord == nil {
		return nil, errNoCoord()
	}
	var p traceGetParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if p.TraceID == "" {
		return nil, fmt.Errorf("trace_id is required")
	}
	runs, err := deps.Coord.GetTrace(p.TraceID)
	if err != nil {
		return nil, err
	}
	return json.Marshal(runs)
}

// --- tasks -----------------------------------------------------------------

type tasksListParams struct {
	State   string `json:"state,omitempty"`
	AgentID string `json:"agent_id,omitempty"`
	Limit   int    `json:"limit,omitempty"`
}

func handleTasksList(deps Deps, params json.RawMessage) (json.RawMessage, error) {
	if deps.Coord == nil {
		return nil, errNoCoord()
	}
	var p tasksListParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
	}
	tasks, err := deps.Coord.ListTasks(coord.TaskFilter{
		State: p.State, AgentID: p.AgentID, Limit: p.Limit,
	})
	if err != nil {
		return nil, err
	}
	return json.Marshal(tasks)
}

type taskIDParams struct {
	ID string `json:"id"`
}

// TaskDetail is a task together with its audit trail and its run ledger: one
// row per claim, each saying how that bounded attempt ended.
type TaskDetail struct {
	Task   *coord.Task       `json:"task"`
	Events []coord.TaskEvent `json:"events"`
	Runs   []coord.TaskRun   `json:"runs"`
}

func handleTaskGet(deps Deps, params json.RawMessage) (json.RawMessage, error) {
	if deps.Coord == nil {
		return nil, errNoCoord()
	}
	var p taskIDParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	task, err := deps.Coord.GetTask(p.ID)
	if err != nil {
		return nil, err
	}
	events, err := deps.Coord.TaskEvents(p.ID)
	if err != nil {
		return nil, err
	}
	runs, err := deps.Coord.TaskRuns(p.ID)
	if err != nil {
		return nil, err
	}
	return json.Marshal(TaskDetail{Task: task, Events: events, Runs: runs})
}

type taskResumeParams struct {
	ID   string `json:"id"`
	More int    `json:"more,omitempty"`
}

// handleTaskResume reopens a task the system gave up on. A person's decision,
// from the dashboard, so it carries no fencing token.
func handleTaskResume(deps Deps, params json.RawMessage) (json.RawMessage, error) {
	if deps.Coord == nil {
		return nil, errNoCoord()
	}
	var p taskResumeParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if err := deps.Coord.ResumeTask(p.ID, deps.AgentID, p.More); err != nil {
		return nil, err
	}
	go NotifyAgents(deps.Coord, "", "", "about resumed "+p.ID)
	return json.Marshal(map[string]bool{"resumed": true})
}

type taskCreateParams struct {
	Title      string `json:"title"`
	Body       string `json:"body,omitempty"`
	AssignedTo string `json:"assigned_to,omitempty"`
	Priority   int    `json:"priority,omitempty"`
}

func handleTaskCreate(deps Deps, params json.RawMessage) (json.RawMessage, error) {
	if deps.Coord == nil {
		return nil, errNoCoord()
	}
	var p taskCreateParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	// Depth stays 0: a task created from the dashboard is a human starting a
	// chain, not an agent extending one.
	task, err := deps.Coord.CreateTask(coord.NewTask{
		CreatedBy:  deps.AgentID,
		AssignedTo: p.AssignedTo,
		Title:      p.Title,
		Body:       p.Body,
		Priority:   p.Priority,
	})
	if err != nil {
		return nil, err
	}
	// Ring whoever can take it so the work starts now rather than at the next
	// poll. Best effort: the queue is the source of truth.
	go NotifyTaskCreated(deps.Coord, task)
	return json.Marshal(task)
}

func handleTaskCancel(deps Deps, params json.RawMessage) (json.RawMessage, error) {
	if deps.Coord == nil {
		return nil, errNoCoord()
	}
	var p taskIDParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if err := deps.Coord.CancelTask(p.ID, deps.AgentID); err != nil {
		return nil, err
	}
	return json.Marshal(map[string]bool{"canceled": true})
}

// --- shared notes ----------------------------------------------------------

type notesListParams struct {
	Search          string `json:"search,omitempty"`
	Kind            string `json:"kind,omitempty"`
	IncludeReplaced bool   `json:"include_replaced,omitempty"`
	Limit           int    `json:"limit,omitempty"`
}

func handleNotesList(deps Deps, params json.RawMessage) (json.RawMessage, error) {
	if deps.Coord == nil {
		return nil, errNoCoord()
	}
	var p notesListParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
	}
	var (
		notes []coord.Note
		err   error
	)
	if p.Search != "" {
		notes, err = deps.Coord.SearchNotes(p.Search, deps.AgentID, p.Limit)
	} else {
		notes, err = deps.Coord.ListNotes(coord.NoteFilter{
			Scope: deps.AgentID, Kind: p.Kind,
			IncludeReplaced: p.IncludeReplaced, Limit: p.Limit,
		})
	}
	if err != nil {
		return nil, err
	}
	return json.Marshal(notes)
}

type noteAddParams struct {
	Title      string `json:"title"`
	Body       string `json:"body,omitempty"`
	Kind       string `json:"kind,omitempty"`
	Tags       string `json:"tags,omitempty"`
	Supersedes string `json:"supersedes,omitempty"`
}

func handleNoteAdd(deps Deps, params json.RawMessage) (json.RawMessage, error) {
	if deps.Coord == nil {
		return nil, errNoCoord()
	}
	var p noteAddParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	note, err := deps.Coord.AddNote(coord.NewNote{
		Author: deps.AgentID, Kind: p.Kind, Title: p.Title,
		Body: p.Body, Tags: p.Tags, Supersedes: p.Supersedes,
	})
	if err != nil {
		return nil, err
	}
	// Keep the markdown copy agents read in step with the database. The note
	// is already committed, so a render failure is reported, not fatal.
	if err := deps.Coord.WriteNotesFile(deps.NotesFile); err != nil {
		log.Printf("gateway: note saved but NOTES.md not rewritten: %v", err)
	}
	return json.Marshal(note)
}

// --- inter-agent messages --------------------------------------------------

type messagesListParams struct {
	// Inbox restricts the listing to messages addressed to this agent;
	// otherwise the whole cross-agent conversation is returned.
	Inbox      bool `json:"inbox,omitempty"`
	UnreadOnly bool `json:"unread_only,omitempty"`
	Limit      int  `json:"limit,omitempty"`
}

func handleMessagesList(deps Deps, params json.RawMessage) (json.RawMessage, error) {
	if deps.Coord == nil {
		return nil, errNoCoord()
	}
	var p messagesListParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
	}
	var (
		msgs []coord.Message
		err  error
	)
	if p.Inbox {
		msgs, err = deps.Coord.Inbox(deps.AgentID, p.UnreadOnly, p.Limit)
	} else {
		msgs, err = deps.Coord.RecentMessages(p.Limit)
	}
	if err != nil {
		return nil, err
	}
	return json.Marshal(msgs)
}

type messageSendParams struct {
	To     string `json:"to"`
	Body   string `json:"body"`
	TaskID string `json:"task_id,omitempty"`
}

func handleMessageSend(deps Deps, params json.RawMessage) (json.RawMessage, error) {
	if deps.Coord == nil {
		return nil, errNoCoord()
	}
	var p messageSendParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if p.To == "" || p.Body == "" {
		return nil, fmt.Errorf("to and body are required")
	}
	msg, err := deps.Coord.SendMessage(deps.AgentID, p.To, p.TaskID, p.Body)
	if err != nil {
		return nil, err
	}
	return json.Marshal(msg)
}
