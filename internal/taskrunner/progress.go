package taskrunner

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/ngocp/goterm-control/internal/chat"
	"github.com/ngocp/goterm-control/internal/coord"
	"github.com/ngocp/goterm-control/internal/reporter"
)

// Saying what a task is doing, in the room that asked for it.
//
// A task opened from a conversation runs for minutes and the thread shows
// nothing until it is over. That is the same silence a channel turn had before
// it started reporting progress — except worse, because a task is the long
// kind of work, so the silence lasts longer and the person watching has more
// reason to wonder whether anything is happening at all.
//
// One line per RUN, edited in place and removed when the run ends. Not kept
// between runs on purpose: between runs nothing IS running, and a line that
// still said "working" would be a lie the rest of the time. The report the
// reporter posts when the task finishes is the permanent record; this is only
// the window while it happens.
// progressPrefix is coord.ProgressPrefix; the startup sweep matches the same.
const progressPrefix = coord.ProgressPrefix

// progressLine owns the message a run keeps updated.
type progressLine struct {
	db        *coord.DB
	messageID string
	channelID string
	rootID    string
	ownThread bool // the thread is this task's own, not its parent's
	taskID    string
	agent     string
	title     string
	started   time.Time

	mu       sync.Mutex
	tools    []string
	thinking strings.Builder
	todos    []coord.ProgressTodo
	last     time.Time
	reply    string
	preview  *chat.StreamPreview
	trailing *time.Timer // a redraw owed once the throttle window ends
	closed   bool
}

const progressEvery = 3 * time.Second

// openProgress puts a line in the thread where this task is talked about, and
// opens that thread first when there is none. Nil only when the task belongs to
// no room at all — one queued from the CLI or a schedule outside a project has
// nowhere to report and must not invent somewhere. Every method below
// tolerates nil.
//
// Until 2026-11-05 only a task bound to a thread spoke, and most work is not:
// a task an agent queues with `task new`, a verify, a rework. Those ran in
// silence while the room they belonged to showed nothing — a team whose
// members only speak when spoken to.
func openProgress(db *coord.DB, task *coord.Task) *progressLine {
	if db == nil || task == nil {
		return nil
	}
	rootID, channelID, err := db.ConversationThread(task)
	if err != nil {
		log.Printf("taskrunner: thread of %s: %v", task.ID, err)
		return nil
	}
	own := false
	switch {
	case rootID != "":
		own = threadIsTasks(db, rootID, task.ID)
		// Saying "I'm on it" where the work was asked, once — not on every
		// continuation, which would fill the thread with the same line.
		if !own && task.Attempts <= 1 && task.Continuations == 0 {
			post(db, channelID, rootID, task.ClaimedBy, claimLine(task))
		}
	case task.ChannelID != "" && task.Kind != coord.KindMention:
		root, _, err := db.PostMessage(coord.NewChannelMessage{
			ChannelID: task.ChannelID, AuthorKind: coord.MemberAgent, AuthorID: task.ClaimedBy,
			Body: claimLine(task), TaskID: task.ID,
		})
		if err != nil {
			log.Printf("taskrunner: could not open a thread for %s: %v", task.ID, err)
			return nil
		}
		rootID, channelID, own = root.ID, task.ChannelID, true
	default:
		return nil
	}

	// Sweep a line left behind by a run that died with the gateway. Without
	// this, a crash leaves the room with an agent permanently about to finish.
	if thread, err := db.ThreadMessages(rootID); err == nil {
		for _, m := range thread {
			if m.AuthorKind == coord.MemberAgent && strings.HasPrefix(m.Body, progressPrefix) &&
				strings.Contains(m.Body, task.ID) {
				if err := db.DeleteMessage(m.ID); err != nil {
					log.Printf("taskrunner: stale progress line: %v", err)
				}
			}
		}
	}

	p := &progressLine{
		db: db, channelID: channelID, rootID: rootID, ownThread: own,
		taskID: task.ID, agent: task.ClaimedBy,
		title: task.Title, started: time.Now(), preview: chat.DefaultPreview(),
	}
	msg, _, err := db.PostMessage(coord.NewChannelMessage{
		ChannelID: channelID, ThreadRoot: rootID,
		AuthorKind: coord.MemberAgent, AuthorID: task.ClaimedBy,
		Body: p.body(),
	})
	if err != nil {
		log.Printf("taskrunner: could not open a progress line: %v", err)
		return nil
	}
	p.messageID = msg.ID
	return p
}

func threadIsTasks(db *coord.DB, rootID, taskID string) bool {
	own, _, err := db.TaskThread(taskID)
	return err == nil && own == rootID
}

// claimLine is the agent picking the work up, in the room's words.
func claimLine(t *coord.Task) string {
	verb := "nhận việc"
	switch {
	case t.Kind == coord.KindVerify:
		verb = "kiểm tra"
	case strings.HasPrefix(t.Title, "Rework:"):
		verb = "làm lại"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "📋 %s %s `%s`\n%s", t.ClaimedBy, verb, reporter.ShortID(t.ID), coord.Quiet(oneLine(t.Title)))
	if t.CreatedBy != "" && t.CreatedBy != t.ClaimedBy {
		fmt.Fprintf(&b, "\n_giao bởi %s_", t.CreatedBy)
	}
	return b.String()
}

func oneLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

func post(db *coord.DB, channelID, rootID, author, body string) {
	if _, _, err := db.PostMessage(coord.NewChannelMessage{
		ChannelID: channelID, ThreadRoot: rootID,
		AuthorKind: coord.MemberAgent, AuthorID: author, Body: body,
	}); err != nil {
		log.Printf("taskrunner: post to thread %s: %v", rootID, err)
	}
}

// Finish says how the run ended, in the thread, after the live line is gone.
// The live line is deleted on purpose (see above), so without this a room
// watching a task saw it start and then saw nothing at all.
//
// A finished task the reporter will announce in this same thread is left to
// it: two copies of one result is noise. Everything else is said here —
// including what the reporter never covers: a verify, a sub-task, work an
// agent queued for itself, a task created by "system" or "owner".
func (p *progressLine) Finish(t *coord.Task, reporterPosts bool) {
	if p == nil || t == nil {
		return
	}
	var body string
	switch t.State {
	case coord.TaskCompleted, coord.TaskFailed, coord.TaskRejected:
		if p.ownThread && reporterPosts {
			return
		}
		body = reporter.Format(t)
	case coord.TaskBlocked, coord.TaskInputRequired:
		body = fmt.Sprintf("⏸ `%s` dừng, chờ %s", t.ID, orDash(t.BlockedOn))
		if q := strings.TrimSpace(firstNonEmpty(t.Question, t.Checkpoint)); q != "" {
			body += "\n\n" + truncate(q, 1500)
		}
	case coord.TaskSubmitted, coord.TaskWorking:
		body = fmt.Sprintf("↻ `%s` xong lượt %d, còn tiếp", t.ID, t.Continuations)
		if c := strings.TrimSpace(t.Checkpoint); c != "" {
			body += "\n\n" + truncate(c, 1500)
		}
	default: // canceled: the person who canceled it knows
		return
	}
	post(p.db, p.channelID, p.rootID, p.agent, coord.Quiet(body))
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

// Tool records what the run just reached for and updates the room. A new tool
// reports at once; that is the part worth watching, and it changes on the
// order of seconds rather than tokens.
func (p *progressLine) Tool(name string) {
	if p == nil || name == "" {
		return
	}
	p.mu.Lock()
	if len(p.tools) == 0 || p.tools[len(p.tools)-1] != name {
		p.tools = append(p.tools, name)
	}
	p.mu.Unlock()
	p.redraw()
}

// Thinking records what the model is weighing. An empty chunk ends a block.
func (p *progressLine) Thinking(chunk string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	if chunk == "" {
		p.thinking.WriteString("\n\n")
	} else {
		p.thinking.WriteString(chunk)
	}
	p.mu.Unlock()
	p.redraw()
}

// Todos records the model's plan from a TodoWrite call's input.
func (p *progressLine) Todos(inputJSON string) {
	if p == nil {
		return
	}
	var in struct {
		Todos []struct {
			Content    string `json:"content"`
			Status     string `json:"status"`
			ActiveForm string `json:"activeForm"`
		} `json:"todos"`
	}
	if json.Unmarshal([]byte(inputJSON), &in) != nil || len(in.Todos) == 0 {
		return
	}
	todos := make([]coord.ProgressTodo, 0, len(in.Todos))
	for _, t := range in.Todos {
		todos = append(todos, coord.ProgressTodo{Content: t.Content, Status: t.Status, Active: t.ActiveForm})
	}
	p.mu.Lock()
	p.todos = todos
	p.mu.Unlock()
	p.redraw()
}

// redraw writes the line now if the last write is old enough, and otherwise
// owes one for the end of the window. Without the owed write, a step reached
// two seconds after the previous one was never shown: an eleven-minute Bash
// call sat under the name of the command before it for all eleven minutes.
func (p *progressLine) redraw() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	wait := progressEvery - time.Since(p.last)
	if wait > 0 {
		if p.trailing == nil {
			p.trailing = time.AfterFunc(wait, func() {
				p.mu.Lock()
				p.trailing = nil
				p.mu.Unlock()
				p.redraw()
			})
		}
		p.mu.Unlock()
		return
	}
	p.last = time.Now()
	body := p.bodyLocked()
	p.mu.Unlock()
	if err := p.db.UpdateMessageBody(p.messageID, body); err != nil {
		log.Printf("taskrunner: progress update: %v", err)
	}
}

// Text shows the answer as it forms. The threshold is a character count, not a
// timer: a timer fires mid-word as often as not, and the point of watching is
// to see the reasoning arrive in readable pieces.
func (p *progressLine) Text(chunk string) {
	if p == nil || chunk == "" {
		return
	}
	p.mu.Lock()
	p.reply += chunk
	grown := p.preview.Ready(p.reply)
	body := p.bodyLocked()
	p.mu.Unlock()

	if !grown {
		return
	}
	if err := p.db.UpdateMessageBody(p.messageID, body); err != nil {
		log.Printf("taskrunner: progress update: %v", err)
	}
}

// Close removes the line. The result belongs to the reporter, which posts it
// when the task itself finishes rather than when one run of it does.
func (p *progressLine) Close() {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.closed = true
	if p.trailing != nil {
		p.trailing.Stop()
	}
	p.mu.Unlock()
	if err := p.db.DeleteMessage(p.messageID); err != nil {
		log.Printf("taskrunner: could not close the progress line: %v", err)
	}
}

func (p *progressLine) body() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.bodyLocked()
}

func (p *progressLine) bodyLocked() string {
	return coord.WithProgress(p.markdownLocked(), p.dataLocked())
}

// dataLocked is the run as the dashboard draws it.
func (p *progressLine) dataLocked() coord.Progress {
	d := coord.Progress{
		Agent:     p.agent,
		Label:     "đang chạy",
		Task:      p.taskID,
		Since:     p.started,
		Steps:     append([]string(nil), p.tools...),
		StepCount: len(p.tools),
		Thinking:  lastParagraph(p.thinking.String(), 480),
		Todos:     p.todos,
	}
	if r := strings.TrimSpace(p.reply); r != "" {
		d.Partial = p.preview.Cut(r)
	}
	return d
}

// lastParagraph is the newest paragraph of s, at most max runes from its end.
func lastParagraph(s string, max int) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	paras := strings.Split(s, "\n\n")
	last := strings.TrimSpace(paras[len(paras)-1])
	if r := []rune(last); len(r) > max {
		last = "…" + string(r[len(r)-max:])
	}
	return last
}

func (p *progressLine) markdownLocked() string {
	var b strings.Builder
	b.WriteString(progressPrefix)
	fmt.Fprintf(&b, "_đang chạy_ `%s` · %s", p.taskID, time.Since(p.started).Round(time.Second))
	if len(p.tools) > 0 {
		from := 0
		if len(p.tools) > 5 {
			from = len(p.tools) - 5
		}
		fmt.Fprintf(&b, " · %s", strings.Join(p.tools[from:], " → "))
	}
	if t := strings.TrimSpace(p.title); t != "" {
		fmt.Fprintf(&b, "\n\n%s", coord.Quiet(t))
	}
	// What it is actually saying, once it starts saying anything. This is the
	// part a person waiting wants; the tool names above are context for it.
	if r := strings.TrimSpace(p.reply); r != "" {
		fmt.Fprintf(&b, "\n\n%s", p.preview.Cut(r))
	}
	return b.String()
}
