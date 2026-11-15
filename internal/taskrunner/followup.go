package taskrunner

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/ngocp/goterm-control/internal/chat"
	"github.com/ngocp/goterm-control/internal/coord"
	"github.com/ngocp/goterm-control/internal/session"
	"github.com/ngocp/goterm-control/internal/tools"
	"github.com/ngocp/goterm-control/internal/trace"
)

// Follow-ups: the owner talking into a task's session from the board.
//
// A task that is still going reads them in its next run (followUpSection). A
// task that has finished has no next run, so the agent that ran it answers
// with one turn of its own, resumed in the same CLI session — the way a chat
// continues — and the task stays finished.

// followUpSection is what a run's prompt says about lines the owner typed
// into the task while it waited or ran.
func followUpSection(fs []coord.FollowUp) string {
	if len(fs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n## The owner wrote to you about this task (from the dashboard)\n\n")
	for _, f := range fs {
		fmt.Fprintf(&b, "- %s: %s\n", f.CreatedAt.Local().Format("15:04"), strings.TrimSpace(f.Body))
	}
	b.WriteString("\nThese came after the brief above and take precedence over it where they disagree. " +
		"Act on them in this run, and say in your reply what you did about each.\n")
	return b.String()
}

// claimFollowUpAndStart answers one follow-up on a finished task, if this
// agent ran one and a slot is free. Same shape as claimAndStart.
func (r *Runner) claimFollowUpAndStart(ctx context.Context) bool {
	if ctx.Err() != nil {
		return false
	}
	select {
	case r.slots <- struct{}{}:
	default:
		return false
	}
	f, task, err := r.db.ClaimFollowUp(r.cfg.AgentID, func(taskID string) bool {
		_, busy := r.live.Load(taskID)
		return busy
	})
	if err != nil {
		<-r.slots
		if err != coord.ErrNoTask {
			log.Printf("taskrunner: claim follow-up: %v", err)
		}
		return false
	}
	log.Printf("taskrunner: follow-up %s on %s (%s): %s", f.ID, task.ID, task.State, truncate(f.Body, 80))
	r.runs.Add(1)
	go func() {
		defer func() {
			<-r.slots
			r.runs.Done()
			r.Poke()
		}()
		r.answerFollowUp(ctx, f, task)
	}()
	return true
}

// answerFollowUp runs one turn in a finished task's session.
func (r *Runner) answerFollowUp(ctx context.Context, f *coord.FollowUp, task *coord.Task) {
	runCtx, cancel := context.WithTimeout(ctx, r.cfg.Timeout)
	defer cancel()

	sess := session.New(taskChatID)
	sess.ID = coord.TaskSessionID(task.ID)
	workspace, _, err := r.db.EnsureTaskWorkspace(task)
	if err != nil {
		log.Printf("taskrunner: follow-up %s: %v — running in this agent's own workspace", f.ID, err)
	}
	sess.SetWorkspace(workspace)
	sess.SetEnv("BOMCLAW_TASK_ID", task.ID)
	if task.ChannelID != "" {
		sess.SetEnv("BOMCLAW_TASK_CHANNEL", task.ChannelID)
	}
	resumed := false
	if ref := coord.ParseSessionRef(task.SessionRef); ref.Provider == r.llm.Name() && ref.SessionID != "" {
		sess.SetSessionID(ref.SessionID)
		sess.SetProvider(ref.Provider)
		if ref.Account != "" {
			sess.SetAccount(ref.Account)
		}
		resumed = true
	}
	prompt := followUpPrompt(task, f, resumed)

	span := r.rec.StartTrace("task follow-up", coord.RunTypeTask, trace.Meta{
		SessionID: sess.ID, Model: r.cfg.Model, Provider: r.llm.Name(),
	})
	span.SetInputs(prompt)

	slog := coord.NewTaskLogWriter(r.db, task.ID, f.ID, r.cfg.AgentID)
	how := "tiếp tục phiên cũ"
	if !resumed {
		how = "phiên mới (phiên cũ không mở lại được)"
	}
	slog.Status("start", fmt.Sprintf("trả lời tin nhắn · %s · %s · %s", r.cfg.AgentID, r.llm.Name(), how))

	sess.MarkRunning(truncate("↩ "+task.Title, 60))
	r.live.Store(task.ID, sess)
	r.emit(task.ID, sess.ID, "started")
	defer func() {
		sess.MarkIdle()
		r.live.Delete(task.ID)
		r.emit(task.ID, sess.ID, "finished")
	}()

	var reply strings.Builder
	call := span.Child(r.llm.Name(), coord.RunTypeLLM)
	sendErr := r.llm.SendMessage(runCtx, sess, r.cfg.Model, prompt, "", chat.StreamCallbacks{
		OnText: func(chunk string) {
			reply.WriteString(chunk)
			slog.Text(chunk)
		},
		OnThinking: slog.Thinking,
		OnToolCall: func(name, input string) {
			label := chat.ToolLabel(name, input)
			sess.NoteTool(label)
			slog.Tool(label, input)
			sp := call.Child(name, coord.RunTypeTool)
			sp.SetInputs(input)
			sp.End("", nil)
		},
		OnToolResult: func(name string, res tools.ToolResult) {
			slog.ToolResult(name, res.Output, res.IsError)
		},
	})
	in, out := sess.Tokens()
	call.EndWithTokens(reply.String(), sendErr, in, out)
	span.End(reply.String(), sendErr)
	slog.Close()

	if ref := (coord.SessionRef{Provider: r.llm.Name(), SessionID: sess.GetSessionID(), Account: sess.GetAccount()}); ref.SessionID != "" {
		if err := r.db.SetTaskSessionRef(task.ID, ref); err != nil {
			log.Printf("taskrunner: follow-up %s: keep session: %v", f.ID, err)
		}
	}
	state := coord.FollowUpAnswered
	if sendErr != nil {
		state = coord.FollowUpFailed
		slog.Status("end:failed", sendErr.Error())
	} else {
		slog.Status("end:answered", "")
	}
	if err := withRetry("finish follow-up "+f.ID, func() error { return r.db.FinishFollowUp(f.ID, state) }); err != nil {
		log.Printf("taskrunner: %v", err)
	}
}

// followUpPrompt frames the owner's line for a turn on a finished task.
//
// A resumed session already remembers the work, so it gets the line and the
// rules. A fresh one — the CLI could not reopen the old conversation — gets
// what the task was and how it ended, or it would answer blind.
func followUpPrompt(t *coord.Task, f *coord.FollowUp, resumed bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[Owner, from the dashboard, about task %s — it is %s]\n\n", t.ID, t.State)
	b.WriteString(strings.TrimSpace(f.Body))
	b.WriteString("\n\n---\n")
	if !resumed {
		fmt.Fprintf(&b, "Context (your earlier conversation on this task could not be reopened):\n"+
			"Task: %s\n", t.Title)
		if t.Body != "" {
			fmt.Fprintf(&b, "Brief:\n%s\n", truncate(t.Body, 3000))
		}
		if t.Result != "" {
			fmt.Fprintf(&b, "Result you reported:\n%s\n", truncate(t.Result, 3000))
		}
		b.WriteString("\n")
	}
	b.WriteString("This is a follow-up turn, not a new run of the task: the task stays " + t.State +
		". Do not run `bomclaw task done/progress/block` for it. Answer the owner directly — " +
		"in their language, short, readable. If they ask for more work, do it now in this turn " +
		"and say what changed and where.")
	return b.String()
}
