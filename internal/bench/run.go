package bench

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ngocp/goterm-control/internal/coord"
)

// Options configure one bench run.
type Options struct {
	To       string        // agent every case is assigned to; "" = any agent may claim
	Label    string        // free text recorded in the report, e.g. "before prompt change"
	Timeout  time.Duration // how long to wait for the whole suite; default 30m
	Poll     time.Duration // how often to look; default 5s
	WorkRoot string        // where case directories go; default a temp dir
	// OnCreated is called for every queued task — the CLI rings the agents with
	// it so a run does not wait out a poll interval. Optional.
	OnCreated func(*coord.Task)
	// Log receives one line per event. Optional.
	Log func(format string, args ...any)
}

func (o *Options) defaults() {
	if o.Timeout <= 0 {
		o.Timeout = 30 * time.Minute
	}
	if o.Poll <= 0 {
		o.Poll = 5 * time.Second
	}
	if o.Log == nil {
		o.Log = func(string, ...any) {}
	}
}

type pending struct {
	c    Case
	dir  string
	task *coord.Task
	done *CaseResult
}

// Run queues every case, waits for each to settle, checks it and scores it.
// A case that has not settled by the deadline is canceled and scored as a
// timeout; a case that asks a person is canceled once its question is read,
// since nobody is going to answer it.
func Run(ctx context.Context, db *coord.DB, s *Suite, o Options) (*Report, error) {
	o.defaults()
	if err := s.Validate(); err != nil {
		return nil, err
	}
	root := o.WorkRoot
	if root == "" {
		var err error
		if root, err = os.MkdirTemp("", "bomclaw-bench-"); err != nil {
			return nil, err
		}
	}
	rep := &Report{Suite: s.Name, Agent: o.To, Label: o.Label, Started: time.Now()}

	var all []*pending
	for _, c := range s.Cases {
		p := &pending{c: c, dir: filepath.Join(root, c.ID)}
		all = append(all, p)
		if err := os.MkdirAll(p.dir, 0o755); err != nil {
			p.done = &CaseResult{ID: c.ID, Kind: c.Kind, Outcome: "error", Error: err.Error()}
			continue
		}
		if c.Setup != "" {
			if out, err := shell(ctx, p.dir, expand(c.Setup, p.dir)); err != nil {
				p.done = &CaseResult{ID: c.ID, Kind: c.Kind, Outcome: "error",
					Error: "setup: " + err.Error() + ": " + out}
				continue
			}
		}
		t, err := db.CreateTask(coord.NewTask{
			CreatedBy: Creator, AssignedTo: o.To,
			Title: expand(c.Title, p.dir), Body: expand(c.Body, p.dir),
			Acceptance: expand(c.Acceptance, p.dir),
		})
		if err != nil {
			p.done = &CaseResult{ID: c.ID, Kind: c.Kind, Outcome: "error", Error: err.Error()}
			continue
		}
		p.task = t
		o.Log("queued %s → %s", c.ID, t.ID)
		if o.OnCreated != nil {
			o.OnCreated(t)
		}
	}

	deadline := time.Now().Add(o.Timeout)
	tick := time.NewTicker(o.Poll)
	defer tick.Stop()
	for {
		open := 0
		for _, p := range all {
			if p.done != nil {
				continue
			}
			t, err := db.GetTask(p.task.ID)
			if err != nil {
				open++
				continue
			}
			outcome, settled := settledOutcome(t)
			if !settled {
				open++
				continue
			}
			p.done = finish(ctx, db, p, t, outcome)
			o.Log("%s: %s (behaved=%v)", p.c.ID, p.done.Outcome, p.done.Behaved)
		}
		if open == 0 {
			break
		}
		if time.Now().After(deadline) {
			for _, p := range all {
				if p.done == nil {
					_, _ = db.CancelTaskTree(p.task.ID, Creator)
					t, err := db.GetTask(p.task.ID)
					if err != nil {
						t = p.task
					}
					p.done = finish(ctx, db, p, t, OutcomeTimeout)
					o.Log("%s: timeout", p.c.ID)
				}
			}
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-tick.C:
		}
	}

	for _, p := range all {
		rep.Results = append(rep.Results, *p.done)
	}
	rep.Finished = time.Now()
	rep.Summary = Summarize(rep.Results)
	return rep, nil
}

// settledOutcome says whether the bench has seen the end of a task: a terminal
// state, or a stop that waits on a person. A block on children is still work.
func settledOutcome(t *coord.Task) (string, bool) {
	switch t.State {
	case coord.TaskCompleted, coord.TaskFailed, coord.TaskCanceled, coord.TaskRejected:
		return t.State, true
	case coord.TaskBlocked:
		if t.BlockedOn == coord.BlockedOnHuman {
			return OutcomeAsked, true
		}
	}
	return "", false
}

func finish(ctx context.Context, db *coord.DB, p *pending, t *coord.Task, outcome string) *CaseResult {
	tree, _ := db.ListTasks(coord.TaskFilter{ContextID: t.ContextID, Limit: 500})
	runs := 0
	for _, x := range tree {
		rs, _ := db.TaskRuns(x.ID)
		runs += len(rs)
	}
	checkOK, out := false, ""
	if p.c.Check != "" {
		var err error
		out, err = shell(ctx, p.dir, expand(p.c.Check, p.dir))
		checkOK = err == nil
	}
	r := score(p.c, t, outcome, tree, runs, checkOK)
	if !checkOK {
		r.CheckOutput = clip(out, 400)
	}
	// Nobody is going to answer a bench question. Cancel only after reading it:
	// the cancel clears the question column.
	if outcome == OutcomeAsked {
		_, _ = db.CancelTaskTree(t.ID, Creator)
	}
	return &r
}

func shell(ctx context.Context, dir, script string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", script)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "BENCH_DIR="+dir)
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err := cmd.Run()
	return strings.TrimSpace(buf.String()), err
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
