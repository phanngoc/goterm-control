package gateway

import (
	"encoding/json"
	"fmt"

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

// TaskDetail is a task together with its audit trail.
type TaskDetail struct {
	Task   *coord.Task       `json:"task"`
	Events []coord.TaskEvent `json:"events"`
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
	return json.Marshal(TaskDetail{Task: task, Events: events})
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
