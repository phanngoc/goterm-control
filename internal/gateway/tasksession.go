package gateway

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/ngocp/goterm-control/internal/coord"
)

// A task's session on the board: read it as it streams, and talk into it.
// See coord/tasklog.go for where the lines come from and where they go.

type taskLogParams struct {
	ID    string `json:"id"`
	After int64  `json:"after,omitempty"` // last entry id the caller already has
}

// TaskSession is one poll of a task's session.
type TaskSession struct {
	Entries   []coord.TaskLogEntry `json:"entries"`
	FollowUps []coord.FollowUp     `json:"followups"`
	// Running is true while a run or a follow-up turn is open on any agent —
	// read from the shared rows, so the board knows for peers too.
	Running bool `json:"running"`
	// Legacy is set when the entries were rebuilt from traces: runs from
	// before the log existed, which recorded tool calls and the final reply
	// but not the words in between.
	Legacy bool `json:"legacy,omitempty"`
}

func handleTaskLog(deps Deps, params json.RawMessage) (json.RawMessage, error) {
	if deps.Coord == nil {
		return nil, errNoCoord()
	}
	var p taskLogParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if p.ID == "" {
		return nil, fmt.Errorf("id is required")
	}
	entries, err := deps.Coord.TaskLog(p.ID, p.After, 1000)
	if err != nil {
		return nil, err
	}
	followUps, err := deps.Coord.FollowUps(p.ID)
	if err != nil {
		return nil, err
	}
	runs, err := deps.Coord.TaskRuns(p.ID)
	if err != nil {
		return nil, err
	}
	out := TaskSession{Entries: entries, FollowUps: followUps}
	for _, r := range runs {
		if r.Liveness == coord.RunRunning {
			out.Running = true
		}
	}
	for _, f := range followUps {
		if f.State == coord.FollowUpRunning {
			out.Running = true
		}
	}
	// Nothing logged at all, on a first read: the task ran before the log
	// existed, or on a binary without it. Its traces still say what it did.
	if p.After == 0 && !hasRunEntries(entries) && len(runs) > 0 {
		if rebuilt := entriesFromTraces(deps.Coord, p.ID, runs); len(rebuilt) > 0 {
			out.Entries = mergeByTime(rebuilt, entries)
			out.Legacy = true
		}
	}
	return json.Marshal(out)
}

// hasRunEntries: anything beyond the owner's own lines.
func hasRunEntries(es []coord.TaskLogEntry) bool {
	for _, e := range es {
		if e.Kind != coord.LogUser {
			return true
		}
	}
	return false
}

// entriesFromTraces rebuilds a session from the trace spans of each run. The
// ids are negative so a poller's "after" cursor never skips a real entry.
func entriesFromTraces(db *coord.DB, taskID string, runs []coord.TaskRun) []coord.TaskLogEntry {
	var out []coord.TaskLogEntry
	var id int64
	add := func(e coord.TaskLogEntry) {
		id--
		e.ID, e.TaskID = id, taskID
		out = append(out, e)
	}
	for i, r := range runs {
		var spans []coord.Run
		if r.TraceID != "" {
			spans, _ = db.GetTrace(r.TraceID)
		}
		// The trace opens a moment before the run row is written; the divider
		// goes above everything the run did, not between its first steps.
		start := r.StartedAt
		for _, s := range spans {
			if !s.StartedAt.IsZero() && s.StartedAt.Before(start) {
				start = s.StartedAt
			}
		}
		add(coord.TaskLogEntry{RunID: r.ID, AgentID: r.AgentID, Kind: coord.LogStatus, Label: "start",
			Content: fmt.Sprintf("lượt %d · %s", i+1, r.AgentID), CreatedAt: start.Add(-time.Millisecond)})
		for _, s := range spans {
			switch {
			case s.Depth == 0 && s.Inputs != "":
				add(coord.TaskLogEntry{RunID: r.ID, AgentID: r.AgentID, Kind: coord.LogPrompt, Content: s.Inputs, CreatedAt: s.StartedAt})
			case s.RunType == coord.RunTypeTool:
				add(coord.TaskLogEntry{RunID: r.ID, AgentID: r.AgentID, Kind: coord.LogTool, Label: s.Name, Content: s.Inputs, CreatedAt: s.StartedAt})
			case s.RunType == coord.RunTypeLLM && s.Outputs != "":
				at := s.EndedAt
				if at.IsZero() {
					at = s.StartedAt
				}
				add(coord.TaskLogEntry{RunID: r.ID, AgentID: r.AgentID, Kind: coord.LogText, Content: s.Outputs, CreatedAt: at})
			}
		}
		if r.Liveness != coord.RunRunning {
			at := r.EndedAt
			if at.IsZero() {
				at = r.StartedAt
			}
			add(coord.TaskLogEntry{RunID: r.ID, AgentID: r.AgentID, Kind: coord.LogStatus, Label: "end:" + r.Liveness, Content: r.Note, CreatedAt: at})
		}
	}
	// A tool span ends the moment it starts, so its LLM parent — which ends
	// last — sorts after them: the reply lands under the tools it followed.
	return mergeByTime(out, nil)
}

// mergeByTime interleaves two lists by time, stable within each.
func mergeByTime(a, b []coord.TaskLogEntry) []coord.TaskLogEntry {
	all := append(append([]coord.TaskLogEntry{}, a...), b...)
	// insertion sort: these lists are short, and stability matters
	for i := 1; i < len(all); i++ {
		for j := i; j > 0 && all[j].CreatedAt.Before(all[j-1].CreatedAt); j-- {
			all[j], all[j-1] = all[j-1], all[j]
		}
	}
	return all
}

type taskFollowUpParams struct {
	ID      string `json:"id"`
	Message string `json:"message"`
	// Interrupt stops the run in flight so the agent picks this up now,
	// in the same session, instead of after the run ends.
	Interrupt bool `json:"interrupt,omitempty"`
}

// handleTaskFollowUp is the owner typing into a task's session.
//
// A task waiting on a person is answered by it: the same as the "send &
// unblock" box, so there is one place to type. Anything else is queued for
// whoever will read it next — the next run, or the agent that finished it —
// and that agent is rung so it does not wait out its poll.
func handleTaskFollowUp(deps Deps, params json.RawMessage) (json.RawMessage, error) {
	if deps.Coord == nil {
		return nil, errNoCoord()
	}
	var p taskFollowUpParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if p.ID == "" {
		return nil, fmt.Errorf("id is required")
	}
	task, err := deps.Coord.GetTask(p.ID)
	if err != nil {
		return nil, err
	}
	// A room turn (a mention, a direct line) is not a run the task runner
	// can continue: the conversation is the thread. The owner's line goes
	// there, and the next turn — same session — answers it.
	if task.Kind == coord.KindMention {
		return threadFollowUp(deps, task, p)
	}
	// Interrupting only means something to a task that is running.
	interrupt := p.Interrupt && task.State == coord.TaskWorking
	f, err := deps.Coord.AddFollowUp(p.ID, "owner", p.Message, interrupt)
	if err != nil {
		return nil, err
	}
	how := "queued"
	ring := task.ClaimedBy
	switch {
	case task.State == coord.TaskBlocked && task.BlockedOn == coord.BlockedOnHuman:
		if err := deps.Coord.UnblockTask(p.ID, deps.AgentID, f.Body); err != nil {
			return nil, err
		}
		_ = deps.Coord.MarkFollowUpAnswered(f.ID, task.AssignedTo)
		f.State = coord.FollowUpAnswered
		how = "unblocked"
		ring = task.AssignedTo
	case coord.IsTerminalState(task.State):
		how = "followup-turn"
	case interrupt:
		how = "interrupt"
	default:
		how = "next-run"
		if task.State == coord.TaskSubmitted {
			ring = task.AssignedTo
		}
	}
	go NotifyAgents(deps.Coord, ring, "", "about a follow-up on "+p.ID)
	return json.Marshal(map[string]any{"followup": f, "delivery": how})
}

// threadFollowUp posts the owner's line into the thread a room task belongs
// to, addressed to its agent; with interrupt, the turn running now stops.
func threadFollowUp(deps Deps, task *coord.Task, p taskFollowUpParams) (json.RawMessage, error) {
	root, channelID, err := deps.Coord.ConversationThread(task)
	if err != nil {
		return nil, err
	}
	if root == "" {
		return nil, fmt.Errorf("task %s has no thread to continue", task.ID)
	}
	agent := task.ClaimedBy
	if agent == "" {
		agent = task.AssignedTo
	}
	interrupt := p.Interrupt && task.State == coord.TaskWorking
	if interrupt {
		if err := deps.Coord.AddInterrupt(task.ID, "owner", p.Message); err != nil {
			return nil, err
		}
	}
	msg, wake, err := deps.Coord.PostMessage(coord.NewChannelMessage{
		ChannelID: channelID, ThreadRoot: root,
		AuthorKind: coord.MemberUser, AuthorID: coord.OwnerUserID, Body: p.Message,
		Notify: []coord.Member{{Kind: coord.MemberAgent, ID: agent}},
	})
	if err != nil {
		return nil, err
	}
	for _, who := range wake {
		go NotifyAgents(deps.Coord, who, "", "about a reply in "+channelID)
	}
	how := "thread"
	if interrupt {
		how = "interrupt"
	}
	return json.Marshal(map[string]any{"message": msg, "delivery": how})
}

type threadLogParams struct {
	RootID string `json:"root_id"`
	After  int64  `json:"after,omitempty"`
}

// ThreadSession is the session behind a conversation thread: every turn an
// agent took in it, one log, and which of them is running.
type ThreadSession struct {
	Entries []coord.TaskLogEntry `json:"entries"`
	Tasks   []*coord.Task        `json:"tasks"`
	Running string               `json:"running,omitempty"` // the task of the turn in flight
}

func handleThreadLog(deps Deps, params json.RawMessage) (json.RawMessage, error) {
	if deps.Coord == nil {
		return nil, errNoCoord()
	}
	var p threadLogParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if p.RootID == "" {
		return nil, fmt.Errorf("root_id is required")
	}
	ids, err := deps.Coord.ThreadTaskIDs(p.RootID)
	if err != nil {
		return nil, err
	}
	out := ThreadSession{Tasks: []*coord.Task{}}
	for _, id := range ids {
		t, err := deps.Coord.GetTask(id)
		if err != nil {
			continue
		}
		out.Tasks = append(out.Tasks, t)
		if t.State == coord.TaskWorking {
			out.Running = t.ID
		}
	}
	if out.Entries, err = deps.Coord.TaskLogOf(ids, p.After, 1000); err != nil {
		return nil, err
	}
	return json.Marshal(out)
}
