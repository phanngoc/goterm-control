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
	"github.com/ngocp/goterm-control/internal/memory"
	"github.com/ngocp/goterm-control/internal/session"
	"github.com/ngocp/goterm-control/internal/skills"
	"github.com/ngocp/goterm-control/internal/trace"
)

// verificationsPerSweep bounds how many goals one tick may open readings for.
// A gateway coming back after a night off should not spawn forty at once.
// maxCriteriaAsks is how many times a goal is called back for a definition of
// done before it stops for a person. Two: one repeat is a fair second chance,
// and every further one is a model turn spent learning what the second already
// showed.
const maxCriteriaAsks = 2

const verificationsPerSweep = 5

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
	// Concurrency is how many tasks this agent runs at once (default 1). Chat
	// keeps its own lane regardless; this only widens the task lane, so a long
	// task no longer holds up a short one queued behind it (design P3).
	Concurrency int

	// VerifyGoals turns on the reading of a finished goal by a peer that did
	// not do the work. Off by default: it costs one model turn per goal, the
	// same reason the skill-review turn is off. Turning it on is choosing that
	// "done" should mean checked rather than declared.
	VerifyGoals bool

	// Memory is the agent's own MEMORY.md and daily notes. Nil-safe.
	//
	// It was missing here, and only here. Chat and channel mentions both run
	// through bot.Handler, which injects it; the task lane calls the CLI
	// directly and passed "" for the memory argument that has always been in
	// chat.Client.SendMessage. So the lane doing the heaviest work was the one
	// lane that neither read what the agent knows nor added to it — agent 3,
	// which works almost entirely through tasks, had written zero daily notes.
	//
	// The agent's own, not the project's, even when a task runs inside a
	// project folder: MEMORY.md is what this agent knows, and a project has
	// AGENTS.md for what the project is. Putting one agent's durable memory
	// inside a folder its peers also work in confuses the two.
	Memory *memory.Manager
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

	// slots bounds concurrent runs; runs lets Wait join the ones in flight.
	slots chan struct{}
	runs  sync.WaitGroup

	onEvent func(Event)
	onWake  func(coord.WokenParent)
	onStuck func(coord.Task)

	// renewEvery is how often the lease is pushed out, as a field so a test can
	// watch a lease being lost without waiting two minutes for it.
	renewEvery time.Duration
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
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 1
	}
	return &Runner{
		db: db, llm: llm, rec: rec, cfg: cfg,
		poke:  make(chan struct{}, 1),
		done:  make(chan struct{}),
		slots: make(chan struct{}, cfg.Concurrency),

		renewEvery: renewEvery,
	}
}

// SetWakeListener registers the listener told when a parent task, blocked on
// its children, is put back in the queue by this runner's sweep. The gateway
// uses it to ring the agent the parent is pinned to; this runner pokes itself.
// SetStuckListener registers who to tell when a goal stops and waits for a
// person: it spent its run budget, two waves produced nothing, or the agent
// asked for a decision it cannot make.
//
// Nothing told anyone before. PendingReports only covers terminal states and
// `blocked` is not one, so a goal that stopped at two in the morning waited
// until somebody happened to open a board — which is the difference between
// "runs overnight" and "runs until it needs you, silently".
func (r *Runner) SetStuckListener(fn func(coord.Task)) {
	if r != nil {
		r.onStuck = fn
	}
}

func (r *Runner) SetWakeListener(fn func(coord.WokenParent)) {
	if r != nil {
		r.onWake = fn
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
	log.Printf("taskrunner: claiming tasks for %s every %s (run cap %s, %d at a time)",
		r.cfg.AgentID, r.cfg.Interval, r.cfg.Timeout, r.cfg.Concurrency)
	go func() {
		defer close(r.done)
		ticker := time.NewTicker(r.cfg.Interval)
		defer ticker.Stop()
		for {
			r.sweep()
			// Drain the queue before sleeping again: a peer may have left
			// several tasks at once. Each claim runs in its own goroutine up
			// to Concurrency; a run ending pokes the loop so the next queued
			// task is picked up at once, not at the next tick.
			for r.claimAndStart(ctx) {
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

// Wait blocks until the loop has stopped and every run in flight has ended.
func (r *Runner) Wait() {
	if r == nil {
		return
	}
	<-r.done
	r.runs.Wait()
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
	// Nothing live can be older than one run cap plus one lease; anything
	// still "running" past that has no process behind it.
	if ids, err := r.db.ReapOrphanRuns(r.cfg.Timeout + coord.DefaultLease); err != nil {
		log.Printf("taskrunner: reap orphan runs: %v", err)
	} else if len(ids) > 0 {
		log.Printf("taskrunner: closed %d orphan run(s) as lost: %v", len(ids), ids)
	}
	r.wakeParents()
	r.sweepVerifications()
}

// wakeParents returns parents whose children have all finished to the queue
// and rings whoever should run them next. Called from the sweep and right
// after each run this agent finishes, so a child completing here wakes its
// parent now rather than at the next tick.
func (r *Runner) wakeParents() {
	woken, stalled, err := r.db.WakeParents(time.Now())
	if err != nil {
		log.Printf("taskrunner: wake parents: %v", err)
		return
	}
	for _, w := range woken {
		log.Printf("taskrunner: %s: children finished — back in the queue for %s", w.TaskID, orAny(w.AssignedTo, "any agent"))
		if w.AssignedTo == "" || w.AssignedTo == r.cfg.AgentID {
			r.Poke()
		}
		if r.onWake != nil {
			go r.onWake(w)
		}
	}
	for _, sgoal := range stalled {
		log.Printf("taskrunner: %s stopped and is waiting on a person", sgoal.ID)
		r.stuck(sgoal)
	}
}

// stuck tells whoever is listening that a goal has stopped for a person.
func (r *Runner) stuck(t coord.Task) {
	if r == nil || r.onStuck == nil {
		return
	}
	go r.onStuck(t)
}

// claimAndStart takes one task if a slot is free and runs it in the
// background. It reports whether it started something, so the loop can keep
// draining while slots remain.
func (r *Runner) claimAndStart(ctx context.Context) bool {
	if ctx.Err() != nil {
		return false
	}
	select {
	case r.slots <- struct{}{}:
	default:
		return false // every slot busy; a run ending pokes us
	}
	task, err := r.db.ClaimTask(r.cfg.AgentID)
	if err != nil {
		<-r.slots
		if err != coord.ErrNoTask {
			log.Printf("taskrunner: claim: %v", err)
		}
		return false
	}
	log.Printf("taskrunner: claimed %s (attempt %d, run %d): %s", task.ID, task.Attempts, task.Continuations+1, task.Title)
	r.runs.Add(1)
	go func() {
		defer func() {
			<-r.slots
			r.runs.Done()
			r.Poke() // a slot opened; look for the next task now
		}()
		r.execute(ctx, task)
	}()
	return true
}

// withRetry runs a ledger write that must not be dropped on a transient
// database error. The shared file has several writers; even with immediate
// transactions a busy timeout can still fire under load, and losing the close
// of a run leaves it "running" forever. Semantic outcomes (lease lost, task
// finished, not found) are returned at once — retrying them is meaningless.
func withRetry(what string, fn func() error) error {
	delay := 100 * time.Millisecond
	var err error
	for i := 0; i < 6; i++ {
		err = fn()
		if err == nil || !isTransient(err) {
			return err
		}
		log.Printf("taskrunner: %s: %v — retrying in %s", what, err, delay)
		time.Sleep(delay)
		if delay < 2*time.Second {
			delay *= 2
		}
	}
	return err
}

func isTransient(err error) bool {
	switch {
	case errors.Is(err, coord.ErrLostLease), errors.Is(err, coord.ErrTaskFinished), errors.Is(err, coord.ErrNotFound):
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "SQLITE_BUSY") || strings.Contains(msg, "database is locked") ||
		strings.Contains(msg, "SQLITE_LOCKED")
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

func orAny(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func (r *Runner) execute(ctx context.Context, task *coord.Task) {
	timeoutCtx, cancelTimeout := context.WithTimeout(ctx, r.cfg.Timeout)
	defer cancelTimeout()
	// Layered so losing the lease can stop the work and say why. The cause is
	// what tells `classify` the difference between this and a shutdown.
	runCtx, abort := context.WithCancelCause(timeoutCtx)
	defer abort(nil)

	// Keep the lease alive for as long as the work actually takes — and stop
	// the work when we can no longer hold it.
	stopRenew := r.renewLease(runCtx, task.ID, abort)
	defer stopRenew()

	// One session per task, kept across runs: the CLI stores the conversation
	// under this id, so resuming it is what makes run 2 remember run 1.
	sess := session.New(taskChatID)
	sess.ID = coord.TaskSessionID(task.ID)
	// Where this run happens. Work filed under a project runs in that project's
	// folder, the same as a turn in its room. Everything else runs in the task
	// tree's own shared folder — because a delegated task is rarely one agent:
	// agent2 hands one piece to agent1 and another to agent3, and "where the
	// agent lives" puts the three of them on three different disks with nothing
	// but prose to carry a file between them.
	workspace, shared, err := r.db.EnsureTaskWorkspace(task)
	if err != nil {
		// Not fatal. A run in the agent's own workspace is worse than a shared
		// one and better than a task that cannot start at all.
		log.Printf("taskrunner: %s: %v — running in this agent's own workspace", task.ID, err)
	}
	sess.SetWorkspace(workspace)
	// What the CLI this run spawns should know about the work it is inside.
	//
	// The project is the one that matters: `bomclaw task sub` already inherits
	// it from the parent row, but `bomclaw task new --to <peer>` — which is how
	// an agent hands a whole piece over — opens a ROOT task, with nothing to
	// inherit from. Without this the work leaves the project silently, and the
	// board it was filed on stops showing it.
	sess.SetEnv("BOMCLAW_TASK_ID", task.ID)
	if task.ChannelID != "" {
		sess.SetEnv("BOMCLAW_TASK_CHANNEL", task.ChannelID)
	}
	resumed := false
	if ref := coord.ParseSessionRef(task.SessionRef); ref.Provider != "" {
		// Only the same CLI can resume its own session; a ref from the other
		// backend (after a provider switch) is simply not used.
		if ref.Provider == r.llm.Name() {
			sess.SetSessionID(ref.SessionID)
			sess.SetProvider(ref.Provider)
			if ref.Account != "" {
				sess.SetAccount(ref.Account) // the credential pool honours the pin
			}
			resumed = ref.SessionID != ""
		}
	}

	span := r.rec.StartTrace("task", coord.RunTypeTask, trace.Meta{
		SessionID: sess.ID,
		Model:     r.cfg.Model,
		Provider:  r.llm.Name(),
	})
	// What the parent's children came back with, and what a peer said about
	// this task while nobody was running it, both belong in front of the model.
	children, _ := r.db.Children(task.ID)
	inbox := r.taskMail(task.ID)
	prompt := taskPrompt(task, r.cfg.Timeout, resumed, children, inbox, r.peers(), workspace, r.db.RunspacePath(task.ContextID), shared)

	// Same rule as the chat lane: only a brand-new session. A resumed one
	// already carries this, and injecting it again pollutes the context.
	memoryContext := ""
	if !resumed && r.cfg.Memory.Enabled() {
		memoryContext = r.cfg.Memory.BuildContext(time.Now())
	}
	span.SetInputs(prompt)
	if tid := span.TraceID(); tid != "" {
		if err := r.db.AttachTrace(task.ID, tid); err != nil {
			log.Printf("taskrunner: attach trace: %v", err)
		}
	}

	var run *coord.TaskRun
	if err := withRetry("start run "+task.ID, func() (e error) {
		run, e = r.db.StartRun(task.ID, r.cfg.AgentID, task.Attempts, span.TraceID())
		return e
	}); err != nil {
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
	// Say what is happening in the room that asked for it. Nil when this task
	// did not come from a conversation, and every call below tolerates that.
	progress := openProgress(r.db, task)
	defer progress.Close()

	sendErr := r.llm.SendMessage(runCtx, sess, r.cfg.Model, prompt, memoryContext, chat.StreamCallbacks{
		OnText: func(chunk string) {
			reply.WriteString(chunk)
			progress.Text(chunk)
		},
		OnToolCall: func(name, input string) {
			// The label, not the bare name: the board is the screen built for
			// watching work, and "Bash" tells a watcher less than
			// "Bash(cd ../goterm-workspace)" does.
			label := chat.ToolLabel(name, input)
			sess.NoteTool(label)
			progress.Tool(label)
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
	// context.Cause, not runCtx.Err(): both a deadline and a lost lease report
	// themselves as a plain cancellation on the context, and they are not the
	// same event. Cause still yields DeadlineExceeded for the timeout, so the
	// existing branches keep working.
	outcome := classify(task, after, sendErr, context.Cause(runCtx), reply.String(), todoPending,
		isGoal(task, workspace, shared))
	outcome.SessionRef = coord.SessionRef{
		Provider: r.llm.Name(), SessionID: sess.GetSessionID(), Account: sess.GetAccount(),
	}

	if errors.Is(sendErr, chat.ErrSessionNotFound) {
		outcome.ResetSession = true
		outcome.SessionRef.SessionID = ""
	}

	var spanErr error
	if outcome.Liveness == coord.RunFailed || outcome.Liveness == coord.RunTimedOut {
		spanErr = sendErr
		if spanErr == nil {
			spanErr = fmt.Errorf("%s", outcome.Liveness)
		}
	}
	span.End(outcome.Result, spanErr)

	var final *coord.Task
	finishErr := withRetry("finish run "+run.ID, func() (e error) {
		final, e = r.db.FinishRun(run.ID, outcome)
		return e
	})
	// A child finishing may complete its parent's set; check now so the parent
	// does not wait for the next tick.
	if task.ParentID != "" {
		r.wakeParents()
	}
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
		// The other door a goal stops at: it spent its budget, or the agent
		// asked for a decision only a person can make. One caller, so no race.
		if final.State == coord.TaskBlocked && final.BlockedOn == coord.BlockedOnHuman {
			r.stuck(*final)
		}
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
func classify(before, after *coord.Task, sendErr, ctxErr error, reply string, todoPending, goal bool) coord.RunOutcome {
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
	case errors.Is(ctxErr, ErrLeaseLost):
		// Canceled, not failed: the attempt is refunded, which is right —
		// losing a lease is not the agent's mistake. FinishRun will usually
		// reject this outcome anyway, because whoever took the lease is the
		// one the fence now recognises.
		return coord.RunOutcome{Liveness: coord.RunCanceled, Checkpoint: checkpoint,
			Note: "lease lost to another agent; run stopped"}
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
	case goal && before.Acceptance == "" && before.Continuations >= maxCriteriaAsks:
		// Asked and asked and not answered. Every callback is a model turn, and
		// an agent that will not write a definition of done after this many is
		// not going to — the continuation ceiling would spend twenty of them
		// finding that out. Stopping for a person is the honest end: the owner
		// can write the criteria themselves in the unblock note, which is
		// exactly what the note is for.
		return coord.RunOutcome{Liveness: coord.RunBlocked, BlockedOn: coord.BlockedOnHuman,
			Result: reply,
			Note: fmt.Sprintf("asked %d times for a definition of done and got none; "+
				"unblock with the criteria in the note and it will carry on", before.Continuations)}
	case goal && before.Acceptance == "":
		// It was asked, in this same prompt, to write what done means before
		// starting — and it did neither that nor any of the commands that
		// finish a task. Letting the reply stand as the deliverable is how a
		// goal gets marked complete by an agent saying "starting now", which
		// is exactly what happened the first time this ran on real work.
		//
		// So it is called back with the ask repeated. The continuation ceiling
		// bounds it: an agent that will not write criteria fails loudly rather
		// than succeeding silently.
		return coord.RunOutcome{Liveness: coord.RunPlanOnly, Result: reply, Checkpoint: checkpoint,
			Note: "no acceptance criteria written; a goal does not finish on a reply alone"}
	case before.Acceptance != "":
		// A task with a bar written on it does not get to finish by talking.
		// Everywhere else this system already refuses to read completion out of
		// prose — classify believes what the agent TYPED, not what it said —
		// and this default branch was the one hole left: any non-empty reply
		// became `completed`. For a short task that is convenient. For a goal
		// with criteria it means "done because it said something".
		//
		// So it is called back instead, with the criteria in front of it, until
		// it types `task done`. The continuation ceiling is what stops this
		// being a loop.
		return coord.RunOutcome{Liveness: coord.RunAdvanced, Result: reply,
			Checkpoint: checkpoint,
			Note:       "reply only; a task with acceptance criteria ends with `task done`"}
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
// ErrLeaseLost is why a run was stopped when its lease could no longer be
// renewed. It is a cancellation cause rather than an error return: the run is
// already in flight by then, and the only way to stop it is through its
// context.
var ErrLeaseLost = errors.New("taskrunner: lease lost")

// renewLease holds the task's lease for as long as the run takes, and aborts
// the run when it can no longer hold it.
//
// Stopping the renewal without stopping the work was the bug: the lease lapsed,
// a peer claimed the task and started running it, and this agent carried on
// calling tools against the same files. The database rejected the loser's
// RESULT — FinishRun is fenced on claimed_by and attempts — but nothing
// rejected its WRITES, and since 2026-09-20 both agents stand in the same
// shared run folder. It has happened 8 times in 170 runs.
func (r *Runner) renewLease(ctx context.Context, taskID string, abort func(error)) func() {
	stop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(r.renewEvery)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := r.db.RenewLease(taskID, r.cfg.AgentID); err != nil {
					log.Printf("taskrunner: lease on %s: %v — stopping this run", taskID, err)
					abort(ErrLeaseLost)
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
// taskMail returns the unread messages peers attached to this task (`bomclaw
// msg --task T`) and marks them read — the "comment" wake source of §5.4. Only
// mail addressed to the task is taken: general messages belong to the chat.
func (r *Runner) taskMail(taskID string) []coord.Message {
	all, err := r.db.Inbox(r.cfg.AgentID, true, 50)
	if err != nil {
		return nil
	}
	var mine []coord.Message
	var ids []string
	for _, m := range all {
		if m.TaskID == taskID {
			mine = append(mine, m)
			ids = append(ids, m.ID)
		}
	}
	if len(ids) > 0 {
		_, _ = r.db.MarkRead(r.cfg.AgentID, ids)
	}
	return mine
}

// peers is who else could take work, read from the agents table rather than
// from a paragraph someone remembered to update. An agent that does not know a
// colleague exists cannot hand anything to it — which is how a machine ends up
// running three agents and using two.
func (r *Runner) peers() []coord.Agent {
	all, err := r.db.ListAgents()
	if err != nil {
		return nil
	}
	out := make([]coord.Agent, 0, len(all))
	for _, a := range all {
		if a.ID != r.cfg.AgentID {
			out = append(out, a)
		}
	}
	return out
}

func taskPrompt(t *coord.Task, budget time.Duration, resumed bool, children []coord.Task, inbox []coord.Message, peers []coord.Agent, workspace, scratch string, shared bool) string {
	var b strings.Builder
	if t.Continuations > 0 || resumed {
		fmt.Fprintf(&b, "You are continuing a task from the shared queue (run %d).\n\n", t.Continuations+1)
	} else {
		b.WriteString("You have picked up a task from the shared queue.\n\n")
	}
	fmt.Fprintf(&b, "Task id: %s\nRequested by: %s\n", t.ID, t.CreatedBy)
	if t.ParentID != "" {
		fmt.Fprintf(&b, "Parent task: %s (your result is gathered by it — state findings, not plans)\n", t.ParentID)
	}
	fmt.Fprintf(&b, "Context: %s (depth %d of %d)\n", t.ContextID, t.Depth, coord.MaxDepth)
	if workspace != "" {
		if shared {
			// Saying it is shared is the whole point. An agent that thinks the
			// folder is its own will not look in it for a peer's work, and will
			// write a path into a message instead of just leaving the file.
			fmt.Fprintf(&b, "Working directory: %s\n"+
				"This folder is shared by every task in this context, so a peer working on a "+
				"sibling task is in the same directory. Leave files here for each other rather "+
				"than describing where they are. It is scratch and it ages out — see below for "+
				"what to do with anything the work produced.\n", workspace)
		} else {
			// The root of a tree stands in the project, and its children stand
			// somewhere else. Without this second line it looks around the
			// project folder, sees none of their work, and concludes they did
			// nothing.
			fmt.Fprintf(&b, "Working directory: %s (this project's folder — assemble the deliverable here)\n",
				workspace)
			if scratch != "" {
				fmt.Fprintf(&b, "Your sub-tasks work in %s instead, and hand each other files there. "+
					"Read what they produced from that folder; put what is finished into the project.\n",
					scratch)
			}
		}
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "## %s\n", t.Title)
	switch {
	case t.Acceptance != "":
		// The bar was stored, printed in two places, and never shown to the
		// one agent whose work is measured against it — only to its parent,
		// about its children. Nobody was asked for criteria, so nobody wrote
		// any: not one task in the first eighty-nine had them.
		b.WriteString("\n**Accepted when:**\n")
		b.WriteString(t.Acceptance)
		b.WriteString("\n")
	case isGoal(t, workspace, shared):
		// A goal that arrived without a bar. Asking whoever opened it helped,
		// but it cannot be relied on, and a goal with no definition of done
		// finishes when somebody says it is finished. So the first thing the
		// agent doing it writes is what done means — in this same run, before
		// the work, costing nothing extra.
		fmt.Fprintf(&b, "\n**This goal has no definition of done yet. Write one first:**\n\n"+
			"    bomclaw task accept --id %s --acceptance \"1) …; 2) …; 3) …\"\n\n"+
			"Numbered and checkable, as the person who asked would judge them — not as a "+
			"restatement of the title. They are what you are measured against, what you re-read "+
			"between rounds of work to decide whether you are finished, and what a peer reads "+
			"this against at the end. You can only write them once, so write the bar you would "+
			"be willing to be held to.\n", t.ID)
	}
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

	if len(children) > 0 {
		b.WriteString("\n## Your child tasks\n\n")
		for i, c := range children {
			fmt.Fprintf(&b, "%d. [%s] %s (%s)", i+1, c.State, c.Title, c.ID)
			if c.FailReason != "" {
				fmt.Fprintf(&b, " — %s", c.FailReason)
			}
			b.WriteString("\n")
			if res := strings.TrimSpace(c.Result); res != "" {
				b.WriteString("   " + strings.ReplaceAll(truncate(res, 1500), "\n", "\n   ") + "\n")
			}
		}
	}
	if len(peers) > 0 {
		list := make([]skills.Peer, 0, len(peers))
		for _, p := range peers {
			list = append(list, skills.Peer{
				ID: p.ID, Provider: p.Provider, Model: p.Model,
				Workspace: p.Workspace, Online: p.Online,
			})
		}
		b.WriteString("\n")
		b.WriteString(skills.Roster(list))
		fmt.Fprintf(&b, "\nThe lines under each name are its skills — what it is actually set up to do, "+
			"read from its own toolkit rather than described here, so they cannot go stale. "+
			"`bomclaw task new --to <agent>` hands a piece over; omit --to and any of them may take it.\n"+
			"When you write to one of them about THIS work, add `--task %s`:\n\n"+
			"    bomclaw msg --to <agent> --task %s \"<what you need or what you are handing over>\"\n\n"+
			"Without it the message still arrives, but it is filed nowhere — and the person "+
			"reading this task later sees the work and not the conversation that shaped it.\n", t.ID, t.ID)
	}

	if len(inbox) > 0 {
		b.WriteString("\n## Messages about this task\n\n")
		for _, m := range inbox {
			fmt.Fprintf(&b, "- from %s: %s\n", m.FromAgent, strings.TrimSpace(m.Body))
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
		"Work a peer could do in parallel can be split off: `bomclaw task sub --parent %s --title \"<piece>\" "+
		"[--body ...] [--to <agent>]` (at most %d unfinished at once, depth %d of %d here), then "+
		"`bomclaw task block --id %s --on children` and stop. You are called back with every child's result "+
		"once they have all finished.\n\n"+
		"Two things that sound alike and are not. Do NOT create a task to carry on your own turn — "+
		"that is what `task progress` is for, and the system calls you back. But when you HAVE been "+
		"called back with a wave of results and the goal is not met yet, splitting the remainder into "+
		"another wave is the right move, not a loop: gather, see what is left, split again, and stop "+
		"when the criteria are met.\n\n"+
		"Anything the work PRODUCED — a report, a patch, a page, a file somebody will open — is filed "+
		"with the task, not described in prose:\n\n"+
		"    bomclaw artifact put --task %s --title \"<what it is>\" --file <path>\n"+
		"    bomclaw artifact put --task %s --title \"<what it is>\" --kind link --url <url>\n\n"+
		"The working directory above is shared inside this task tree and nowhere else: it is where you and "+
		"your peers hand files to each other while the work is in progress, and it is deleted once the tree "+
		"has been finished a while. A path written into a sentence outlives neither — the person reading this "+
		"is looking at a board and not a terminal, and nothing survives the file being moved. An artifact is "+
		"found by id, shows up beside the task, and can be handed to a child task as an input.\n\n"+
		"When the work is finished: `bomclaw task done --id %s --result \"<the deliverable>\"`. Your result is what "+
		"the requesting agent reads, so state what you did and what you found. If you cannot proceed without "+
		"a person: `bomclaw task block --id %s --on human --note \"<exactly what you need>\"` and stop.\n\n"+
		"%s"+
		"The final reply is the deliverable, not a plan — do not ask follow-up questions, there is nobody "+
		"waiting to answer them; use `task block` instead.",
		minutes, t.ID, t.ID, coord.MaxOpenChildren, t.Depth, coord.MaxDepth, t.ID,
		t.ID, t.ID, t.ID, t.ID, acceptanceClause(t))
	return b.String()
}

// acceptanceClause is what a task with a bar written on it has to answer for.
//
// Two sentences rather than a gate: refusing `task done` for a missing line
// would hard-fail an agent mid-turn, and that trade was already judged and
// rejected — a brief of its own is what enforces a child standing alone, not a
// checklist. What changed is that the criteria now reach the agent at all, so
// asking it to answer them is asking for something it can see.
func acceptanceClause(t *coord.Task) string {
	if t.Acceptance == "" {
		return ""
	}
	return "This task carries acceptance criteria, listed above. Your result must answer each of " +
		"them in turn — what you did about it and whether it is met. A criterion you could not " +
		"meet is a fact worth reporting, not a reason to stay silent: say so and say why.\n\n"
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// sweepVerifications opens the reading of a finished goal, and turns a negative
// reading into the work it asks for.
//
// Off unless configured: it costs one model turn per goal, which is the same
// reason the skill-review turn is off by default. Turning it on is choosing
// that "done" should mean checked rather than declared.
//
// Every gateway runs this. Opening a verification is guarded by NOT EXISTS plus
// a parent link, and the follow-up by a compare-and-set, so three sweeps
// produce one of each.
func (r *Runner) sweepVerifications() {
	if !r.cfg.VerifyGoals || r.db == nil {
		return
	}
	goals, err := r.db.NeedsVerification(verificationsPerSweep)
	if err != nil {
		log.Printf("taskrunner: goals awaiting verification: %v", err)
	}
	if len(goals) > 0 {
		peers, err := r.db.ListAgents()
		if err != nil {
			log.Printf("taskrunner: peers for verification: %v", err)
			peers = nil
		}
		for _, g := range goals {
			v, err := r.db.OpenVerification(g, peers)
			if err != nil {
				log.Printf("taskrunner: verify %s: %v", g.ID, err)
				continue
			}
			if v == nil {
				// Nobody else to ask. Said once per goal rather than every
				// sweep would need a marker; this is rare enough to repeat.
				log.Printf("taskrunner: %s finished with criteria but there is no peer to check it", g.ID)
				continue
			}
			log.Printf("taskrunner: %s → %s reads it against its criteria", g.ID, v.AssignedTo)
		}
	}

	rejected, err := r.db.RejectedVerifications(verificationsPerSweep)
	if err != nil {
		log.Printf("taskrunner: rejected verifications: %v", err)
		return
	}
	for _, v := range rejected {
		f, err := r.db.FollowUpRejection(v, time.Now())
		if err != nil {
			log.Printf("taskrunner: follow up %s: %v", v.ID, err)
			continue
		}
		if f != nil {
			log.Printf("taskrunner: %s rejected → %s for %s", v.ID, f.ID, f.AssignedTo)
		}
	}
}

// isGoal reports whether this task is the kind of work that needs a definition
// of done: a root task filed under a project.
//
// Not every root task is a goal. "Summarise this log" answers in a sentence and
// should keep doing so — asking it for numbered criteria, and then refusing its
// answer, would be machinery charging rent on the simplest thing the system
// does. A task filed under a project is work somebody organised, which is where
// the criteria earn their cost.
//
// Read from the workspace decision rather than re-derived: TaskWorkspace
// returns the project folder and shared=false for exactly this case, so the two
// cannot drift apart into disagreeing about what a goal is.
func isGoal(t *coord.Task, workspace string, shared bool) bool {
	return t != nil && t.ParentID == "" && t.Kind != coord.KindVerify &&
		workspace != "" && !shared
}
