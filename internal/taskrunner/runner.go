// Package taskrunner lets an agent pick up work its peer left in the shared
// queue and execute it with the same model backend that answers chat.
//
// The loop is deliberately dumb: poll, claim, run, report. The database is the
// source of truth — a missed doorbell only costs latency, never the task. That
// is why the poll exists at all rather than relying on the peer to notify.
package taskrunner

import (
	"context"
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
	Timeout  time.Duration // hard cap on one task's execution
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
	log.Printf("taskrunner: claiming tasks for %s every %s", r.cfg.AgentID, r.cfg.Interval)
	go func() {
		defer close(r.done)
		ticker := time.NewTicker(r.cfg.Interval)
		defer ticker.Stop()
		for {
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

	log.Printf("taskrunner: claimed %s (attempt %d): %s", task.ID, task.Attempts, task.Title)
	r.execute(ctx, task)
	return true
}

func (r *Runner) execute(ctx context.Context, task *coord.Task) {
	runCtx, cancel := context.WithTimeout(ctx, r.cfg.Timeout)
	defer cancel()

	// Keep the lease alive for as long as the work actually takes.
	stopRenew := r.renewLease(runCtx, task.ID)
	defer stopRenew()

	span := r.rec.StartTrace("task", coord.RunTypeTask, trace.Meta{
		SessionID: "task_" + task.ID,
		Model:     r.cfg.Model,
		Provider:  r.llm.Name(),
	})
	span.SetInputs(taskPrompt(task))
	if tid := span.TraceID(); tid != "" {
		if err := r.db.AttachTrace(task.ID, tid); err != nil {
			log.Printf("taskrunner: attach trace: %v", err)
		}
	}

	// A fresh, unregistered session per task: the peer's instruction must not
	// inherit — or pollute — whatever the agent was chatting about.
	sess := session.New(taskChatID)
	sess.ID = "task_" + task.ID

	var reply strings.Builder
	call := span.Child(r.llm.Name(), coord.RunTypeLLM)
	err := r.llm.SendMessage(runCtx, sess, r.cfg.Model, taskPrompt(task), "", chat.StreamCallbacks{
		OnText: func(chunk string) { reply.WriteString(chunk) },
		OnToolCall: func(name, input string) {
			sp := call.Child(name, coord.RunTypeTool)
			sp.SetInputs(input)
			// Tool results are not paired here: the CLI backends report a
			// result callback per call, and the span closes on the next line.
			sp.End("", nil)
		},
	})
	in, out := sess.Tokens()
	call.EndWithTokens(reply.String(), err, in, out)

	state, result := coord.TaskCompleted, reply.String()
	if err != nil {
		state = coord.TaskFailed
		result = fmt.Sprintf("%s\n\nerror: %v", reply.String(), err)
	}
	span.End(result, err)

	if finishErr := r.db.FinishTask(task.ID, r.cfg.AgentID, state, result, task.Attempts); finishErr != nil {
		// ErrLostLease means another agent took over and already answered;
		// discarding this result is the correct outcome, not a failure.
		log.Printf("taskrunner: finish %s: %v", task.ID, finishErr)
		return
	}
	log.Printf("taskrunner: %s → %s", task.ID, state)
}

// renewLease keeps the claim alive while the task runs, and returns a stop
// function. If the lease is lost the renewal simply stops: FinishTask's fencing
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

// taskPrompt turns a queued task into an instruction. The framing matters: the
// agent must understand this came from a peer, not from the user it chats with,
// and that its reply is the deliverable rather than conversation.
func taskPrompt(t *coord.Task) string {
	var b strings.Builder
	b.WriteString("You have picked up a task from the shared queue.\n\n")
	fmt.Fprintf(&b, "Task id: %s\nRequested by: %s\n\n", t.ID, t.CreatedBy)
	fmt.Fprintf(&b, "## %s\n", t.Title)
	if t.Body != "" {
		b.WriteString("\n")
		b.WriteString(t.Body)
		b.WriteString("\n")
	}
	b.WriteString("\nDo the work, then reply with the outcome. Your reply is recorded " +
		"as the task result and is what the requesting agent will read, so state what " +
		"you did and what you found — do not ask follow-up questions, there is nobody " +
		"waiting to answer them.")
	return b.String()
}
