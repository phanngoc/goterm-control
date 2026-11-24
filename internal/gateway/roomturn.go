package gateway

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/ngocp/goterm-control/internal/chat"
	"github.com/ngocp/goterm-control/internal/coord"
)

// RoomTurn is how one agent turn shows up in a room — whatever brought the
// line in. A mention in a project room, a line the owner typed into their
// Direct room on the dashboard, a message to the agent's Telegram bot: they
// were three code paths with three different results (a thread with a live
// progress card; an answer dumped at the top of the room; a copy of the final
// text and nothing else). Now each is a RoomTurn, and reads the same:
//
//   - the reply lives in the thread of the line it answers, so the room's top
//     level is a list of requests, each with its answer under it;
//   - while the agent works, a progress line in that thread shows its steps;
//   - a turn that turns into work — a tool call, or a long think — becomes a
//     task on the room's board, and its whole session streams into task_log,
//     which is what the "Agent đang làm" tab plays back;
//   - the owner can cut in (WatchInterrupts), the way they can on a task.
//
// The caller owns the session and runs the turn; RoomTurn owns everything the
// room sees about it.
type RoomTurn struct {
	deps       Deps
	root       coord.ChannelMessage // the line being answered
	threadRoot string               // where the answer goes
	trace      func() string

	progress *coord.ChannelMessage
	sink     *replySink
	log      *coord.TaskLogWriter

	taskOnce sync.Once
	mu       sync.Mutex
	task     *coord.Task
	longTurn *time.Timer

	// What the turn worked on, for filing it under a project.
	inputs        []string
	filed         bool
	roomIsProject *bool
	hints         []coord.ProjectHint
	unwatch       chan struct{}
	stopOnce      sync.Once

	// tracking is the card this turn opened in its project's room when it was
	// asked privately (Direct room, Telegram) — see announceInProject.
	tracking *coord.ChannelMessage

	// resume names the CLI session the turn ran in, once it has run. Set by a
	// caller that owns the session; without it a turn that runs out of time
	// cannot be continued and is closed as before.
	resume func() coord.SessionRef
}

// ErrRoomSteered is the cause a room turn is canceled with when the owner
// interrupts it to say something else.
var ErrRoomSteered = errors.New("gateway: the owner interrupted the turn")

// OpenRoomTurn announces a turn answering root, in thread threadRoot. trace
// gives the trace id once the turn has one; nil is fine.
func OpenRoomTurn(deps Deps, root coord.ChannelMessage, threadRoot string, trace func() string) *RoomTurn {
	t := &RoomTurn{deps: deps, root: root, threadRoot: threadRoot, trace: trace, unwatch: make(chan struct{})}
	t.log = coord.NewTurnLogWriter(deps.Coord, root.ID, deps.AgentID)
	t.log.User(orOwner(root.AuthorID), strings.TrimSpace(root.Body))
	t.log.Status("start", fmt.Sprintf("trả lời · %s · %s", deps.AgentID, roomLabel(root.ChannelID)))

	// Say something before doing anything: a silent thread looks broken.
	progress, _, err := deps.Coord.PostMessage(coord.NewChannelMessage{
		ChannelID: root.ChannelID, ThreadRoot: threadRoot,
		AuthorKind: coord.MemberAgent, AuthorID: deps.AgentID,
		Body: workingLine(progressView{}),
	})
	if err != nil {
		log.Printf("room turn: could not open a progress line: %v", err)
		progress = nil // the turn still runs; it just runs unseen
	}
	t.progress = progress

	t.sink = &replySink{preview: chat.DefaultPreview(), tap: t.log}
	if progress != nil {
		t.sink.onProgress = func(p progressView) {
			if err := deps.Coord.UpdateMessageBody(progress.ID, workingLine(p)); err != nil {
				log.Printf("room turn: progress update: %v", err)
			}
		}
	}
	t.sink.onWork = t.OpenTask
	t.sink.onToolInput = t.noteToolInput
	t.longTurn = time.AfterFunc(mentionTaskAfter, t.OpenTask)
	return t
}

func orOwner(id string) string {
	if id == "" {
		return coord.OwnerUserID
	}
	return id
}

func roomLabel(channelID string) string {
	if strings.HasPrefix(channelID, "dm_") {
		return "tin nhắn riêng"
	}
	return channelID
}

// Sink is what the turn streams into.
func (t *RoomTurn) Sink() *replySink { return t.sink }

// Task is the board card this turn became, or nil while it is still just an
// answer.
func (t *RoomTurn) Task() *coord.Task {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.task
}

// OpenTask records the turn as work on the room's board (see
// coord/mentiontasks.go) and points its session log at it. Once.
func (t *RoomTurn) OpenTask() {
	t.taskOnce.Do(func() {
		traceID := ""
		if t.trace != nil {
			traceID = t.trace()
		}
		task, err := t.deps.Coord.OpenMentionTask(t.root, t.deps.AgentID, traceID)
		if err != nil {
			log.Printf("room turn: task record for %s: %v", t.root.ID, err)
			return
		}
		t.mu.Lock()
		t.task = task
		t.mu.Unlock()
		t.log.Bind(task.ID, "")
		t.fileUnderProject()
	})
}

// A turn asked in a room that is nobody's project — the owner's Direct room,
// Telegram — is filed under the project it turned out to be about, so it
// shows on that project's board (coord.GuessProject). A turn in a project's
// own room stays there.

const maxToolInputs = 200

func (t *RoomTurn) noteToolInput(name, input string) {
	t.mu.Lock()
	if len(t.inputs) < maxToolInputs {
		t.inputs = append(t.inputs, input)
	}
	t.mu.Unlock()
	t.fileUnderProject()
}

// fileUnderProject moves the turn's task to the project its work points at,
// once, as soon as the evidence is clear.
func (t *RoomTurn) fileUnderProject() {
	t.mu.Lock()
	task, filed := t.task, t.filed
	inputs := append([]string(nil), t.inputs...)
	t.mu.Unlock()
	if task == nil || filed {
		return
	}
	if t.roomIsProject == nil {
		own := false
		if c, err := t.deps.Coord.GetChannel(t.root.ChannelID); err == nil && c.Workspace != "" {
			own = true
		}
		hints, _ := t.deps.Coord.ProjectHints()
		t.mu.Lock()
		t.roomIsProject, t.hints = &own, hints
		t.mu.Unlock()
	}
	if *t.roomIsProject {
		t.mu.Lock()
		t.filed = true
		t.mu.Unlock()
		return
	}
	id := coord.GuessProject(t.hints, t.root.Body, inputs)
	if id == "" {
		return
	}
	if err := t.deps.Coord.SetTaskChannel(task.ID, id); err != nil {
		log.Printf("room turn: file %s under %s: %v", task.ID, id, err)
		return
	}
	t.mu.Lock()
	t.filed = true
	t.mu.Unlock()
	t.log.Status("project", "xếp vào dự án "+id)
	log.Printf("room turn: %s filed under %s", task.ID, id)
	t.announceInProject(id, task)
}

// Work asked for privately still happens in a project, and the team follows a
// project in its room. Filing the task under the project put it on the board,
// but the room itself stayed silent: an agent could rebuild and publish the
// film site from a DM and ch_phim-ngan-25d showed only "Đã publish bản mới",
// with no thread saying who asked for what (2026-11-18).
//
// So the turn opens a thread in the project's room — the same "nhận việc"
// card a queued task opens, marked as coming from a private message — and
// says how it ended there. The conversation itself stays in the Direct room:
// the room gets the work, not the owner's private words beyond the request.
func (t *RoomTurn) announceInProject(channelID string, task *coord.Task) {
	body := fmt.Sprintf("📋 %s nhận việc `%s`\n%s\n_giao bởi %s_",
		t.deps.AgentID, task.ID, coord.Quiet(oneLineOf(task.Title)), orOwner(t.root.AuthorID))
	msg, _, err := t.deps.Coord.PostMessage(coord.NewChannelMessage{
		ChannelID: channelID, AuthorKind: coord.MemberAgent, AuthorID: t.deps.AgentID,
		Body: body, TaskID: task.ID,
	})
	if err != nil {
		log.Printf("room turn: could not open %s's thread in %s: %v", task.ID, channelID, err)
		return
	}
	t.post(channelID, msg.ID, fmt.Sprintf("_việc này được giao qua %s — %s đang làm, kết quả sẽ báo ở đây_",
		roomLabel(t.root.ChannelID), t.deps.AgentID))
	t.mu.Lock()
	t.tracking = msg
	t.mu.Unlock()
}

// reportInProject says how the turn ended in the project thread, when there
// is one. The full answer stays where it was asked; the room gets enough to
// know the state of the work.
func (t *RoomTurn) reportInProject(body string) {
	t.mu.Lock()
	m := t.tracking
	t.mu.Unlock()
	if m == nil || strings.TrimSpace(body) == "" {
		return
	}
	t.post(m.ChannelID, m.ID, body)
}

func (t *RoomTurn) post(channelID, root, body string) {
	if _, _, err := t.deps.Coord.PostMessage(coord.NewChannelMessage{
		ChannelID: channelID, ThreadRoot: root, AuthorKind: coord.MemberAgent, AuthorID: t.deps.AgentID, Body: body,
	}); err != nil {
		log.Printf("room turn: post to %s: %v", channelID, err)
	}
}

func oneLineOf(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

// WatchInterrupts cancels the turn when the owner interrupts it from the
// board. Only a turn that became a task can be interrupted: the interrupt is
// addressed to the task.
func (t *RoomTurn) WatchInterrupts(cancel context.CancelCauseFunc) {
	go func() {
		tick := time.NewTicker(steerPoll)
		defer tick.Stop()
		for {
			select {
			case <-t.unwatch:
				return
			case <-tick.C:
				if task := t.Task(); task != nil && t.deps.Coord.WantsInterrupt(task.ID) {
					// The line itself is already in the thread as a mention;
					// the next sweep answers it in the same session.
					_, _ = t.deps.Coord.TakeFollowUps(task.ID, t.deps.AgentID)
					log.Printf("room turn: %s interrupted by the owner", t.root.ID)
					cancel(ErrRoomSteered)
					return
				}
			}
		}
	}()
}

// steerPoll is how often a turn in flight checks for an interruption.
var steerPoll = 2 * time.Second

func (t *RoomTurn) stop() {
	t.stopOnce.Do(func() {
		// The engine stops WAITING for a turn when the deadline passes; the
		// turn itself keeps going in its lane. Nothing it produces from here
		// is ours to publish.
		t.sink.stop()
		t.longTurn.Stop()
		close(t.unwatch)
		t.log.Close()
	})
}

// ContinueAsTask lets a turn that runs out of time carry on as queued work
// instead of failing: ref is read when the turn ends, and names the CLI
// session the task runner should resume.
func (t *RoomTurn) ContinueAsTask(ref func() coord.SessionRef) { t.resume = ref }

// handOff moves the turn's task into the queue (coord.HandOffMentionTask) and
// returns the line the thread shows in place of the progress line, or ""
// when the task could not be handed off and the turn should close as before.
func (t *RoomTurn) handOff(partial string) string {
	task := t.Task()
	if task == nil || t.resume == nil {
		return ""
	}
	checkpoint := fmt.Sprintf("The %s reply window ran out mid-work; this run continues it. "+
		"Finish the job, then reply as you would have in the thread — your reply is posted there.",
		mentionTurnTimeout)
	if partial = strings.TrimSpace(partial); partial != "" {
		// The tail: where it stopped matters more than how it began.
		if r := []rune(partial); len(r) > 1500 {
			partial = "…" + string(r[len(r)-1500:])
		}
		checkpoint += "\n\nThe end of what you had written so far:\n\n" + partial
	}
	moved, err := t.deps.Coord.HandOffMentionTask(task.ID, t.deps.AgentID, t.resume(), checkpoint)
	if err != nil {
		log.Printf("room turn: hand off %s: %v", task.ID, err)
		return ""
	}
	t.mu.Lock()
	t.task = moved
	t.mu.Unlock()
	if t.deps.PokeTasks != nil {
		t.deps.PokeTasks()
	}
	return fmt.Sprintf("↻ hết %s cho một lượt trả lời — việc chưa xong nên chuyển sang chạy nền "+
		"như task `%s`, tiếp tục đúng phiên này. Kết quả sẽ báo vào thread này.",
		mentionTurnTimeout, task.ID)
}

// Abandon is for a turn killed by shutdown: nothing is said, the progress
// line goes, and an open task stays open for the next gateway to answer.
func (t *RoomTurn) Abandon() {
	t.stop()
	t.log.Status("end:abandoned", "") // the thread must not show it running
	t.finishProgress("")
}

// Finish lands the turn: the answer in place of the progress line (or a note
// saying why there is none), the task closed, the log ended. It returns the
// text that landed.
func (t *RoomTurn) Finish(reply string, turnErr error) string {
	t.stop()
	reply = strings.TrimSpace(reply)

	if turnErr != nil {
		note := "⚠️ lượt này hỏng giữa chừng: " + truncateLine(turnErr.Error(), 200)
		label := "end:failed"
		switch {
		case errors.Is(turnErr, ErrRoomSteered):
			note = "■ _bạn đã dừng lượt này để chỉ hướng — agent trả lời tin tiếp theo của bạn_"
			label = "end:steered"
		case errors.Is(turnErr, context.DeadlineExceeded):
			// Work that outgrew a reply is not a failure: when the turn is
			// already a task, the task carries on in the queue.
			if moved := t.handOff(reply); moved != "" {
				t.log.Status("end:handed_off", "")
				t.finishProgress(moved)
				t.reportInProject("↻ việc dài hơn một lượt trả lời — đã chuyển sang hàng đợi, chạy tiếp")
				return moved
			}
			// Say what to do about it: the shape of the fix is a task, which
			// has five times the budget and survives across runs.
			note = fmt.Sprintf("⌛ hết %s cho một lượt trả lời — việc này dài hơn một câu trả lời. "+
				"Bấm “Giao việc” khi gửi (hoặc nhờ agent mở task), task chạy được lâu hơn và "+
				"giữ tiến độ qua nhiều lượt.", mentionTurnTimeout)
		}
		if partial := reply; partial != "" && errors.Is(turnErr, ErrRoomSteered) {
			note = partial + "\n\n" + note
		}
		t.closeTask(errors.Is(turnErr, ErrRoomSteered), note)
		t.log.Status(label, "")
		t.finishProgress(note)
		if errors.Is(turnErr, ErrRoomSteered) {
			t.reportInProject("■ dừng giữa chừng — người giao đổi hướng")
		} else {
			t.reportInProject("⚠️ không xong: " + truncateLine(note, 300))
		}
		return note
	}
	if reply == "" {
		// Nothing to say, so nothing should be left saying it is thinking.
		t.closeTask(false, "the turn produced no reply")
		t.log.Status("end:empty", "")
		t.finishProgress("")
		return ""
	}
	t.closeTask(true, reply)
	t.log.Status("end:completed", "")
	t.reportInProject("✅ xong — " + truncateLine(reply, projectReportLimit))
	final := reply
	if task := t.Task(); task != nil {
		final += fmt.Sprintf("\n\n_📋 task `%s` trên board_", task.ID)
	}
	if t.progress != nil {
		t.finishProgress(final)
		// Mentions inside the final text still have to ring: the progress
		// line was written before the agent knew what it would say.
		for _, who := range mentionedIn(t.deps.Coord, t.deps.AgentID, final) {
			NotifyAgents(t.deps.Coord, who, t.deps.AgentID, "about a mention in "+t.root.ChannelID)
		}
	} else if _, wake, err := t.deps.Coord.PostMessage(coord.NewChannelMessage{
		ChannelID: t.root.ChannelID, ThreadRoot: t.threadRoot,
		AuthorKind: coord.MemberAgent, AuthorID: t.deps.AgentID, Body: final,
	}); err != nil {
		log.Printf("room turn: post reply to %s: %v", t.root.ChannelID, err)
	} else {
		for _, who := range wake {
			NotifyAgents(t.deps.Coord, who, "", "about a mention in "+t.root.ChannelID)
		}
	}
	return final
}

// projectReportLimit is how much of the answer the project room gets: enough
// to know what was done; the whole of it is in the Direct thread and the
// task's log.
const projectReportLimit = 600

func (t *RoomTurn) closeTask(ok bool, result string) {
	task := t.Task()
	if task == nil {
		return
	}
	if err := t.deps.Coord.CloseMentionTask(task.ID, t.deps.AgentID, ok, truncateLine(result, 4000)); err != nil {
		log.Printf("room turn: close task %s: %v", task.ID, err)
	}
}

// finishProgress replaces the progress line with its final form, or removes
// it when there is nothing to replace it with. A thread must never be left
// with an agent that is permanently about to say something.
func (t *RoomTurn) finishProgress(body string) {
	if t.progress == nil {
		return
	}
	if strings.TrimSpace(body) == "" {
		if err := t.deps.Coord.DeleteMessage(t.progress.ID); err != nil {
			log.Printf("room turn: could not remove the progress line: %v", err)
		}
		return
	}
	if err := t.deps.Coord.UpdateMessageBody(t.progress.ID, body); err != nil {
		log.Printf("room turn: could not close the progress line: %v", err)
	}
}

// mentionedIn finds the peers named in a finished reply.
func mentionedIn(db *coord.DB, self, body string) []string {
	agents, err := db.ListAgents()
	if err != nil {
		return nil
	}
	var out []string
	for _, a := range agents {
		if a.ID != self && strings.Contains(body, "@"+a.ID) {
			out = append(out, a.ID)
		}
	}
	return out
}

// RoomTurnHook is the same thing for a turn that runs outside this package —
// the Telegram bot's. It returns the sink to tee the turn into and the
// function that lands it.
func RoomTurnHook(deps Deps) func(root coord.ChannelMessage, trace func() string) (chat.TurnSink, func(reply string, err error)) {
	return func(root coord.ChannelMessage, trace func() string) (chat.TurnSink, func(string, error)) {
		t := OpenRoomTurn(deps, root, coord.ThreadKey(root), trace)
		return t.Sink(), func(reply string, err error) { t.Finish(reply, err) }
	}
}
