// Package taskrunner lets an agent pick up work its peer left in the shared
// queue and execute it with the same model backend that answers chat.
//
// The loop is deliberately dumb: poll, claim, run, report. The database is the
// source of truth — a missed doorbell only costs latency, never the task. That
// is why the poll exists at all rather than relying on the peer to notify.
//
// A task is many runs. Each claim is one run, capped by Config.Timeout; how the
// run ended (its liveness) is recorded on task_runs, and coord.FinishRun decides
// what that means for the task — done, call me back, blocked, or failed. What
// the agent wrote down (`bomclaw task progress`) and the CLI session it used
// survive between runs, so the next run picks up where this one stopped instead
// of restarting from the original prompt. See docs/design/scheduling-and-long-tasks.md §5.3.
package taskrunner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/ngocp/goterm-control/internal/chat"
	"github.com/ngocp/goterm-control/internal/coord"
	"github.com/ngocp/goterm-control/internal/session"
	"github.com/ngocp/goterm-control/internal/trace"
)

// renewEvery must be comfortably shorter than coord.DefaultLease so a task
// that is genuinely still running is never handed to another agent.
const renewEvery = 2 * time.Minute

// taskChatID namespaces task sessions away from real Telegram chats.
const taskChatID = -1

// Config configures a Runner.
type Config struct {
	AgentID  string
	Model    string
	Interval time.Duration // how often to look for work; 0 disables polling
	Timeout  time.Duration // hard cap on ONE run; the task itself has no cap
}

// Event announces a task run starting or finishing, so the gateway can push
// it to open dashboards the way chat turns already are.
type Event struct {
	TaskID    string
	SessionID string
	Phase     string // "started" | "finished"
}

// Runner claims and executes tasks for one agent.
type Runner struct {
	db     *coord.DB
	llm    chat.Client
	rec    *trace.Recorder
	cfg    Config
	poke   chan struct{}
	closed sync.Once
	done   chan struct{}

	// live holds the session of every run in flight, keyed by task id, so
	// status consumers (tray, dashboard, `bomclaw status`) can see task runs
	// alongside chat turns. Until they could, the tray's awake-while-running
	// mode let the Mac sleep in the middle of a task.
	live sync.Map

	onEvent func(Event)
}

// New builds a Runner. A nil db or llm returns nil, which is a working no-op —
// callers do not have to branch on whether coordination is enabled.
func New(db *coord.DB, llm chat.Client, rec *trace.Recorder, cfg Config) *Runner {
	if db == nil || llm == nil || cfg.Interval <= 0 {
		return nil
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 15 * time.Minute
	}
	return &Runner{
		db: db, llm: llm, rec: rec, cfg: cfg,
		poke: make(chan struct{}, 1),
		done: make(chan struct{}),
	}
}

// SetEventListener registers the single listener for run events. Delivery is
// asynchronous: the listener does network writes and must never slow a run.
func (r *Runner) SetEventListener(fn func(Event)) {
	if r != nil {
		r.onEvent = fn
	}
}

func (r *Runner) emit(taskID, sessionID, phase string) {
	if r.onEvent == nil {
		return
	}
	go r.onEvent(Event{TaskID: taskID, SessionID: sessionID, Phase: phase})
}

// Live returns the sessions of runs in flight right now.
func (r *Runner) Live() []*session.Session {
	if r == nil {
		return nil
	}
	var out []*session.Session
	r.live.Range(func(_, v any) bool {
		out = append(out, v.(*session.Session))
		return true
	})
	return out
}

// Poke asks the runner to look for work right now instead of waiting out the
// interval. Non-blocking and coalescing: several pokes collapse into one pass.
func (r *Runner) Poke() {
	if r == nil {
		return
	}
	select {
	case r.poke <- struct{}{}:
	default:
	}
}

// Start runs the claim loop until ctx is canceled.
func (r *Runner) Start(ctx context.Context) {
	if r == nil {
		return
	}
	log.Printf("taskrunner: claiming tasks for %s every %s (run cap %s)", r.cfg.AgentID, r.cfg.Interval, r.cfg.Timeout)
	go func() {
		defer close(r.done)
		ticker := time.NewTicker(r.cfg.Interval)
		defer ticker.Stop()
		for {
			r.sweep()
			// Drain the queue before sleeping again: a peer may have left
			// several tasks at once.
			for r.claimAndRun(ctx) {
				if ctx.Err() != nil {
					return
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			case <-r.poke:
			}
		}
	}()
}

// Wait blocks until the loop has stopped. Used by tests.
func (r *Runner) Wait() {
	if r == nil {
		return
	}
	<-r.done
}

// sweep does the housekeeping the queue needs but no single run owns: fail
// tasks that have used every attempt (they used to sit in `working` forever),
// and open up tasks addressed to an agent that has stopped heartbeating.
func (r *Runner) sweep() {
	if ids, err := r.db.ReapExhausted(); err != nil {
		log.Printf("taskrunner: reap: %v", err)
	} else if len(ids) > 0 {
		log.Printf("taskrunner: failed %d exhausted task(s): %v", len(ids), ids)
	}
	if ids, err := r.db.RelaxDeadAssignments(coord.StaleAfter); err != nil {
		log.Printf("taskrunner: relax assignments: %v", err)
	} else if len(ids) > 0 {
		log.Printf("taskrunner: opened %d task(s) whose assignee is gone: %v", len(ids), ids)
	}
}

// claimAndRun takes one task and executes it. It reports whether it found work,
// so the caller can keep draining.
func (r *Runner) claimAndRun(ctx context.Context) bool {
	if ctx.Err() != nil {
		return false
	}
	task, err := r.db.ClaimTask(r.cfg.AgentID)
	if err == coord.ErrNoTask {
		return false
	}
	if err != nil {
		log.Printf("taskrunner: claim: %v", err)
		return false
	}

	log.Printf("taskrunner: claimed %s (attempt %d, run %d): %s", task.ID, task.Attempts, task.Continuations+1, task.Title)
	r.execute(ctx, task)
	return true
}

func (r *Runner) execute(ctx context.Context, task *coord.Task) {
	runCtx, cancel := context.WithTimeout(ctx, r.cfg.Timeout)
	defer cancel()

	// Keep the lease alive for as long as the work actually takes.
	stopRenew := r.renewLease(runCtx, task.ID)
	defer stopRenew()

	// One session per task, kept across runs: the CLI stores the conversation
	// under this id, so resuming it is what makes run 2 remember run 1.
	sess := session.New(taskChatID)
	sess.ID = "task_" + task.ID
	resumed := false
	if ref := coord.ParseSessionRef(task.SessionRef); ref.SessionID != "" {
		// Only the same CLI can resume its own session; a ref from the other
		// backend (after a provider switch) is simply not used.
		if ref.Provider == r.llm.Name() {
			sess.SetSessionID(ref.SessionID)
			sess.SetProvider(ref.Provider)
			if ref.Account != "" {
				sess.SetAccount(ref.Account) // the credential pool honours the pin
			}
			resumed = true
		}
	}

	span := r.rec.StartTrace("task", coord.RunTypeTask, trace.Meta{
		SessionID: sess.ID,
		Model:     r.cfg.Model,
		Provider:  r.llm.Name(),
	})
	prompt := taskPrompt(task, r.cfg.Timeout, resumed)
	span.SetInputs(prompt)
	if tid := span.TraceID(); tid != "" {
		if err := r.db.AttachTrace(task.ID, tid); err != nil {
			log.Printf("taskrunner: attach trace: %v", err)
		}
	}

	run, err := r.db.StartRun(task.ID, r.cfg.AgentID, task.Attempts, span.TraceID())
	if err != nil {
		log.Printf("taskrunner: start run for %s: %v", task.ID, err)
		return
	}

	// Visible while it runs: status, tray (awake), dashboard.
	sess.MarkRunning(truncate(task.Title, 60))
	r.live.Store(task.ID, sess)
	r.emit(task.ID, sess.ID, "started")
	defer func() {
		sess.MarkIdle()
		r.live.Delete(task.ID)
		r.emit(task.ID, sess.ID, "finished")
	}()

	var reply strings.Builder
	todoPending := false
	call := span.Child(r.llm.Name(), coord.RunTypeLLM)
	sendErr := r.llm.SendMessage(runCtx, sess, r.cfg.Model, prompt, "", chat.StreamCallbacks{
		OnText: func(chunk string) { reply.WriteString(chunk) },
		OnToolCall: func(name, input string) {
			sess.NoteTool(name)
			if name == "TodoWrite" && hasPendingTodos(input) {
				todoPending = true
			}
			sp := call.Child(name, coord.RunTypeTool)
			sp.SetInputs(input)
			// Tool results are not paired here: the CLI backends report a
			// result callback per call, and the span closes on the next line.
			sp.End("", nil)
		},
	})
	in, out := sess.Tokens()
	call.EndWithTokens(reply.String(), sendErr, in, out)

	// What did the agent do to the task while it ran? Its own commands
	// (`task done`, `task progress`, `task block`) are the authoritative
	// signals; everything else is inferred from how the run ended.
	after, err := r.db.GetTask(task.ID)
	if err != nil {
		log.Printf("taskrunner: reload %s: %v", task.ID, err)
		after = nil
	}
	outcome := classify(task, after, sendErr, runCtx.Err(), reply.String(), todoPending)
	outcome.SessionRef = coord.SessionRef{
		Provider: r.llm.Name(), SessionID: sess.GetSessionID(), Account: sess.GetAccount(),
	}

	var spanErr error
	if outcome.Liveness == coord.RunFailed || outcome.Liveness == coord.RunTimedOut {
		spanErr = sendErr
		if spanErr == nil {
			spanErr = fmt.Errorf("%s", outcome.Liveness)
		}
	}
	span.End(outcome.Result, spanErr)

	final, finishErr := r.db.FinishRun(run.ID, outcome)
	switch {
	case errors.Is(finishErr, coord.ErrLostLease):
		// Another agent took over and already answered; discarding this
		// result is the correct outcome, not a failure.
		log.Printf("taskrunner: %s run %s: %v", task.ID, outcome.Liveness, finishErr)
	case errors.Is(finishErr, coord.ErrTaskFinished):
		// The agent (or a person) moved the task to a terminal state during the
		// run — `bomclaw task done` from inside it, say. The ledger has the run.
		log.Printf("taskrunner: %s → %s (set during the run)", task.ID, final.State)
	case finishErr != nil:
		log.Printf("taskrunner: finish run %s: %v", run.ID, finishErr)
	default:
		msg := fmt.Sprintf("taskrunner: %s run %s → task %s", task.ID, outcome.Liveness, final.State)
		if final.State == coord.TaskSubmitted {
			msg += fmt.Sprintf(" (continuation %d/%d, pinned to %s)", final.Continuations, final.MaxContinuations, final.AssignedTo)
		}
		if final.FailReason != "" {
			msg += " (" + final.FailReason + ")"
		}
		log.Print(msg)
	}
}

// classify turns how the run ended into a RunOutcome. Order matters: the
// agent's own explicit commands win over anything inferred; then the reasons
// the runtime stopped it; then what the reply looks like.
func classify(before, after *coord.Task, sendErr, ctxErr error, reply string, todoPending bool) coord.RunOutcome {
	if after != nil {
		switch after.State {
		case coord.TaskCompleted:
			return coord.RunOutcome{Liveness: coord.RunCompleted, Result: after.Result}
		case coord.TaskFailed:
			return coord.RunOutcome{Liveness: coord.RunFailed, Result: after.Result, Note: "agent gave up (task fail)"}
		case coord.TaskBlocked:
			return coord.RunOutcome{Liveness: coord.RunBlocked, BlockedOn: after.BlockedOn, Note: after.Checkpoint}
		}
	}
	var checkpoint string
	if after != nil && after.Checkpoint != before.Checkpoint {
		checkpoint = after.Checkpoint
	}
	reply = strings.TrimSpace(reply)

	switch {
	case errors.Is(ctxErr, context.DeadlineExceeded) || errors.Is(sendErr, context.DeadlineExceeded):
		return coord.RunOutcome{Liveness: coord.RunTimedOut, Checkpoint: checkpoint, Result: reply,
			Note: "hit the run time cap"}
	case errors.Is(ctxErr, context.Canceled) || errors.Is(sendErr, context.Canceled):
		return coord.RunOutcome{Liveness: coord.RunCanceled, Checkpoint: checkpoint, Note: "run canceled"}
	case sendErr != nil:
		result := reply
		if result != "" {
			result += "\n\n"
		}
		result += "error: " + sendErr.Error()
		return coord.RunOutcome{Liveness: coord.RunFailed, Checkpoint: checkpoint, Result: result, Note: sendErr.Error()}
	case checkpoint != "":
		// It wrote progress and returned: a long job asking to be called back.
		return coord.RunOutcome{Liveness: coord.RunAdvanced, Checkpoint: checkpoint, Result: reply}
	case reply == "":
		return coord.RunOutcome{Liveness: coord.RunEmpty, Note: "no reply"}
	case todoPending:
		// A plan with unfinished items and no progress note: it described the
		// work instead of doing it. Call it back rather than accept the plan
		// as the deliverable.
		return coord.RunOutcome{Liveness: coord.RunPlanOnly, Result: reply, Note: "TodoWrite left items pending"}
	default:
		// A plain reply with no commands: the reply IS the deliverable. This is
		// the pre-P0 behaviour and what a short task still looks like.
		return coord.RunOutcome{Liveness: coord.RunCompleted, Result: reply}
	}
}

// hasPendingTodos reads a TodoWrite input and reports whether any item is not
// completed. Tolerant of shape: anything unparseable is "no".
func hasPendingTodos(inputJSON string) bool {
	var in struct {
		Todos []struct {
			Status string `json:"status"`
		} `json:"todos"`
	}
	if json.Unmarshal([]byte(inputJSON), &in) != nil {
		return false
	}
	for _, t := range in.Todos {
		if t.Status != "" && t.Status != "completed" {
			return true
		}
	}
	return false
}

// renewLease keeps the claim alive while the task runs, and returns a stop
// function. If the lease is lost the renewal simply stops: FinishRun's fencing
// check is what actually protects the other agent's result.
func (r *Runner) renewLease(ctx context.Context, taskID string) func() {
	stop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(renewEvery)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := r.db.RenewLease(taskID, r.cfg.AgentID); err != nil {
					log.Printf("taskrunner: lease on %s: %v", taskID, err)
					return
				}
			}
		}
	}()
	return sync.OnceFunc(func() { close(stop) })
}

// taskPrompt turns a queued task into an instruction for ONE run. The framing
// matters: the agent must understand this came from a peer, not from the user
// it chats with; that its reply is the deliverable rather than conversation;
// that the run has a time budget and what to do about it; and, on a later run,
// where the previous one stopped.
func taskPrompt(t *coord.Task, budget time.Duration, resumed bool) string {
	var b strings.Builder
	if t.Continuations > 0 || resumed {
		fmt.Fprintf(&b, "You are continuing a task from the shared queue (run %d).\n\n", t.Continuations+1)
	} else {
		b.WriteString("You have picked up a task from the shared queue.\n\n")
	}
	fmt.Fprintf(&b, "Task id: %s\nRequested by: %s\n", t.ID, t.CreatedBy)
	if t.ParentID != "" {
		fmt.Fprintf(&b, "Parent task: %s\n", t.ParentID)
	}
	fmt.Fprintf(&b, "Context: %s (depth %d of %d)\n\n", t.ContextID, t.Depth, coord.MaxDepth)
	fmt.Fprintf(&b, "## %s\n", t.Title)
	if t.Body != "" {
		b.WriteString("\n")
		b.WriteString(t.Body)
		b.WriteString("\n")
	}
	if t.Checkpoint != "" {
		b.WriteString("\n## Where the previous run stopped\n\n")
		b.WriteString(t.Checkpoint)
		b.WriteString("\n")
		if resumed {
			b.WriteString("\nYour conversation from the previous run has been resumed, so you also " +
				"remember what you did; the note above is the summary you left.\n")
		} else {
			b.WriteString("\nThe previous run's conversation is not available; the note above is all " +
				"that carried over. Pick up from it.\n")
		}
	}

	minutes := int(budget.Round(time.Minute) / time.Minute)
	if minutes < 1 {
		minutes = 1
	}
	fmt.Fprintf(&b, "\n## How this works\n\n"+
		"You have about %d minutes for this run. The task itself is not limited: if you are not done "+
		"when time is getting short, record where you are with\n\n"+
		"    bomclaw task progress --id %s --note \"<what is done, what is left, anything the next run must know>\"\n\n"+
		"and stop. The system will call you back with that note and your conversation. Do NOT loop on your "+
		"own, and do NOT create a task to continue your own work — write progress and return.\n\n"+
		"When the work is finished: `bomclaw task done --id %s --result \"<the deliverable>\"`. Your result is what "+
		"the requesting agent reads, so state what you did and what you found. If you cannot proceed without "+
		"a person: `bomclaw task block --id %s --on human --note \"<exactly what you need>\"` and stop.\n\n"+
		"The final reply is the deliverable, not a plan — do not ask follow-up questions, there is nobody "+
		"waiting to answer them; use `task block` instead.",
		minutes, t.ID, t.ID, t.ID)
	return b.String()
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
