package coord

import (
	"fmt"
	"sort"
	"time"
)

// The agents' economics: what each one burns, what it gets done, and how good
// a third party says the result is.
//
// Every number comes from rows the system already writes for other reasons —
// runs (each LLM call, with its tokens and the CLI's cost), task_runs (each
// attempt at a task), tasks (the outcome) and verifications (the judge's
// grade) — so the report cannot drift from what happened, and nothing has to
// remember to log it.
//
// Two denominators, kept apart on purpose:
//   - completion rate counts what an agent could have finished: completed over
//     completed + failed + rejected. A task the owner cancelled is not the
//     agent's miss, so it is shown but left out.
//   - quality is the judge's: verifications of goals this agent did, pass rate
//     and mean score. Self-declared "done" is the completion rate; a peer's
//     reading against the criteria is this.

// AgentEconomics is one agent's line in the report.
type AgentEconomics struct {
	AgentID string `json:"agent_id"`

	// Burn: every LLM call the agent made in the window.
	Turns        int     `json:"turns"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	CacheRead    int     `json:"cache_read_tokens"`
	CacheWrite   int     `json:"cache_write_tokens"`
	CostUSD      float64 `json:"cost_usd"`
	TaskCostUSD  float64 `json:"task_cost_usd"` // the part spent running tasks
	ChatCostUSD  float64 `json:"chat_cost_usd"` // chat, mentions, everything else

	// Outcomes: tasks this agent carried that ended in the window.
	Completed int `json:"completed"`
	Failed    int `json:"failed"`
	Rejected  int `json:"rejected"`
	Canceled  int `json:"canceled"`
	Open      int `json:"open"`
	Runs      int `json:"runs"` // attempts across those tasks

	// Quality, as the judge read it.
	Verified  int     `json:"verified"`
	Passed    int     `json:"passed"`
	MeanScore float64 `json:"mean_score"` // 0 when nothing was scored
	Scored    int     `json:"scored"`

	// Judging done for others: not this agent's output, but its cost.
	Judged int `json:"judged"`
}

// CompletionRate is completed over what could have been completed; -1 when
// nothing ended.
func (a AgentEconomics) CompletionRate() float64 {
	d := a.Completed + a.Failed + a.Rejected
	if d == 0 {
		return -1
	}
	return float64(a.Completed) / float64(d)
}

// CostPerCompleted is the task spend per finished task; -1 with none.
func (a AgentEconomics) CostPerCompleted() float64 {
	if a.Completed == 0 {
		return -1
	}
	return a.TaskCostUSD / float64(a.Completed)
}

// Tokens is everything the model processed: fresh input, cache traffic and
// output. Codex reports its cached part inside input, so its cache read is
// not added twice — see SetRunUsage's callers.
func (a AgentEconomics) Tokens() int {
	return a.InputTokens + a.CacheWrite + a.CacheRead + a.OutputTokens
}

// ProjectEconomics is the spend and outcome of one channel's tasks.
type ProjectEconomics struct {
	ChannelID string  `json:"channel_id"`
	Tasks     int     `json:"tasks"`
	Completed int     `json:"completed"`
	CostUSD   float64 `json:"cost_usd"`
	MeanScore float64 `json:"mean_score"`
}

// Economics is the whole report.
type Economics struct {
	Since    time.Time          `json:"since"`
	Judge    string             `json:"judge"`
	Agents   []AgentEconomics   `json:"agents"`
	Projects []ProjectEconomics `json:"projects"`
}

// EconomicsSince builds the report for everything after since. Bench tasks are
// left out: they are measurements of the agents, not work for anyone.
func (db *DB) EconomicsSince(since time.Time) (*Economics, error) {
	out := &Economics{Since: since, Judge: db.Judge()}
	by := map[string]*AgentEconomics{}
	get := func(id string) *AgentEconomics {
		if id == "" {
			id = "(unassigned)"
		}
		a := by[id]
		if a == nil {
			a = &AgentEconomics{AgentID: id}
			by[id] = a
		}
		return a
	}
	from := ts(since)

	// Burn. A run belongs to task work when its trace is one a task attempt
	// recorded; everything else is conversation.
	rows, err := db.conn.Query(`SELECT r.agent_id, COUNT(*),
			SUM(r.input_tokens), SUM(r.output_tokens), SUM(r.cache_read_tokens), SUM(r.cache_write_tokens),
			SUM(r.cost_usd),
			SUM(CASE WHEN r.trace_id IN (SELECT trace_id FROM task_runs WHERE trace_id <> '') THEN r.cost_usd ELSE 0 END)
		FROM runs r
		WHERE r.run_type = ? AND r.started_at >= ?
		GROUP BY r.agent_id`, RunTypeLLM, from)
	if err != nil {
		return nil, fmt.Errorf("economics: burn: %w", err)
	}
	for rows.Next() {
		var id string
		var n, in, outTok, cr, cw int
		var cost, taskCost float64
		if err := rows.Scan(&id, &n, &in, &outTok, &cr, &cw, &cost, &taskCost); err != nil {
			rows.Close()
			return nil, err
		}
		a := get(id)
		a.Turns, a.InputTokens, a.OutputTokens, a.CacheRead, a.CacheWrite = n, in, outTok, cr, cw
		a.CostUSD, a.TaskCostUSD, a.ChatCostUSD = cost, taskCost, cost-taskCost
	}
	rows.Close()

	// Outcomes: tasks that ended (or still stand open) in the window, credited
	// to whoever carried them.
	rows, err = db.conn.Query(`SELECT CASE WHEN claimed_by <> '' THEN claimed_by ELSE assigned_to END, state, COUNT(*),
			SUM((SELECT COUNT(*) FROM task_runs tr WHERE tr.task_id = t.id))
		FROM tasks t
		WHERE kind NOT IN ('bench', ?) AND updated_at >= ?
		GROUP BY 1, 2`, KindVerify, from)
	if err != nil {
		return nil, fmt.Errorf("economics: outcomes: %w", err)
	}
	for rows.Next() {
		var id, state string
		var n, runs int
		if err := rows.Scan(&id, &state, &n, &runs); err != nil {
			rows.Close()
			return nil, err
		}
		a := get(id)
		a.Runs += runs
		switch state {
		case TaskCompleted:
			a.Completed += n
		case TaskFailed:
			a.Failed += n
		case TaskRejected:
			a.Rejected += n
		case TaskCanceled:
			a.Canceled += n
		default:
			a.Open += n
		}
	}
	rows.Close()

	// Quality: each verification credits the goal's doer with the verdict and
	// the judge with the reading.
	rows, err = db.conn.Query(`SELECT CASE WHEN g.claimed_by <> '' THEN g.claimed_by ELSE g.assigned_to END,
			CASE WHEN v.claimed_by <> '' THEN v.claimed_by ELSE v.assigned_to END,
			v.state, v.score
		FROM tasks v JOIN tasks g ON g.id = v.parent_id
		WHERE v.kind = ? AND v.state IN (?, ?) AND v.updated_at >= ?`,
		KindVerify, TaskCompleted, TaskRejected, from)
	if err != nil {
		return nil, fmt.Errorf("economics: verdicts: %w", err)
	}
	sums := map[string]int{}
	for rows.Next() {
		var doer, judge, state string
		var score int
		if err := rows.Scan(&doer, &judge, &state, &score); err != nil {
			rows.Close()
			return nil, err
		}
		a := get(doer)
		a.Verified++
		if state == TaskCompleted {
			a.Passed++
		}
		if score > 0 {
			a.Scored++
			sums[a.AgentID] += score
		}
		get(judge).Judged++
	}
	rows.Close()
	for id, s := range sums {
		a := by[id]
		a.MeanScore = float64(s) / float64(a.Scored)
	}

	for _, a := range by {
		out.Agents = append(out.Agents, *a)
	}
	sort.Slice(out.Agents, func(i, j int) bool { return out.Agents[i].CostUSD > out.Agents[j].CostUSD })

	// Projects: what each room's tasks cost and how they came out.
	rows, err = db.conn.Query(`SELECT t.channel_id, COUNT(*),
			SUM(CASE WHEN t.state = ? THEN 1 ELSE 0 END),
			COALESCE(SUM((SELECT SUM(r.cost_usd) FROM runs r WHERE r.run_type = ? AND r.trace_id IN
				(SELECT trace_id FROM task_runs tr WHERE tr.task_id = t.id AND tr.trace_id <> ''))), 0),
			COALESCE((SELECT AVG(v.score) FROM tasks v JOIN tasks g ON g.id = v.parent_id
				WHERE v.kind = ? AND v.score > 0 AND g.channel_id = t.channel_id AND v.updated_at >= ?), 0)
		FROM tasks t
		WHERE t.kind NOT IN ('bench', ?) AND t.channel_id <> '' AND t.updated_at >= ?
		GROUP BY t.channel_id
		ORDER BY 4 DESC`, TaskCompleted, RunTypeLLM, KindVerify, from, KindVerify, from)
	if err != nil {
		return nil, fmt.Errorf("economics: projects: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var p ProjectEconomics
		if err := rows.Scan(&p.ChannelID, &p.Tasks, &p.Completed, &p.CostUSD, &p.MeanScore); err != nil {
			return nil, err
		}
		out.Projects = append(out.Projects, p)
	}
	return out, rows.Err()
}
