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
	"github.com/ngocp/goterm-control/internal/session"
)

// Answering a mention.
//
// Posting already recorded the mention and rang the named agent's doorbell.
// What nothing did was convert "you were named" into "run a turn": the
// doorbell wakes the claim loop, and the claim loop only reads the tasks
// table, so an agent sat awake beside an unread mention forever.
//
// The conversion here is deliberately a CHAT turn and not a task. A task is a
// unit of work on a board, with a lease, attempts and a result; answering
// "are you there" is none of those, and a board that fills up with greetings
// stops showing the work. More to the point, every task run is its own prompt:
// an agent answering that way would not remember what it said a minute ago,
// which is the one thing a conversation requires. So a mention runs through
// the same turn engine Telegram and the dashboard use, under a session kept
// per thread — and work that turns out to be real gets a task of its own,
// opened by the agent, which is where the board belongs.
const (
	// channelChatID namespaces channel sessions away from real Telegram chats
	// and from task sessions (-1), so nothing in the session manager collides.
	channelChatID = -2

	// MaxThreadTurns caps one conversation. A names B, B answers naming A,
	// and neither is misbehaving — there is simply nothing inside a
	// conversation that says stop, so the stop has to be a number.
	MaxThreadTurns = 6

	// MentionsPerSweep keeps an agent back from an outage from waking into a
	// hundred turns at once.
	MentionsPerSweep = 5

	// StaleMention is where answering becomes worse than not answering.
	// Replying to yesterday's "are you there" is its own kind of broken.
	StaleMention = 24 * time.Hour

	// mentionPoll is the fallback for a doorbell that never rang — a peer
	// that was down, a poke that lost its race with this process starting.
	mentionPoll = 30 * time.Second

	// MaxThreadMessageRunes is how much of one message travels into the prompt.
	// The old figure was 400, which cut a colleague's analysis off mid-sentence
	// and handed the next agent a question whose premise was missing.
	MaxThreadMessageRunes = 4000

	// MaxThreadContextRunes bounds the whole quoted thread, because a room
	// that has been busy all day must still fit in front of a model.
	MaxThreadContextRunes = 24000

	// mentionTurnTimeout bounds one reply. A channel turn is a conversation,
	// not a task — but conversations here routinely involve reading a few
	// files, and three minutes turned out to be shorter than an ordinary
	// answer: an agent asked to set something up spent its whole budget on
	// the setting up. Eight is long enough for real tool work and still well
	// under the task lane's fifteen, which is where genuinely long work goes.
	//
	// The cost is real and worth stating: the watcher answers one mention at a
	// time, so a turn using its whole budget holds up the next question.
	mentionTurnTimeout = 8 * time.Minute
)

// MentionWatcher answers the mentions addressed to one agent.
type MentionWatcher struct {
	deps Deps
	poke chan struct{}
	mu   sync.Mutex // one turn at a time; the engine queues anyway, this keeps sweeps honest
	live sync.Map   // message id -> *session.Session, for StatusResult.Runs
}

// NewMentionWatcher returns nil when this gateway cannot answer — no shared
// database, or no turn engine — so the caller can start it unconditionally.
func NewMentionWatcher(deps Deps) *MentionWatcher {
	if deps.Coord == nil || deps.Turn == nil || deps.AgentID == "" {
		return nil
	}
	return &MentionWatcher{deps: deps, poke: make(chan struct{}, 1)}
}

// Poke asks for a sweep now. Non-blocking and coalescing: two mentions
// arriving together are one sweep, and a full channel means a sweep is
// already coming.
func (w *MentionWatcher) Poke() {
	if w == nil {
		return
	}
	select {
	case w.poke <- struct{}{}:
	default:
	}
}

// Live is the channel turns running right now. Everything that runs has to be
// visible from one place: the tray reads it to keep the Mac awake, and a reply
// the Mac slept through is a reply nobody got.
func (w *MentionWatcher) Live() []*session.Session {
	if w == nil {
		return nil
	}
	var out []*session.Session
	w.live.Range(func(_, v any) bool {
		out = append(out, v.(*session.Session))
		return true
	})
	return out
}

// Start runs sweeps until ctx ends.
func (w *MentionWatcher) Start(ctx context.Context) {
	if w == nil {
		return
	}
	log.Printf("mentions: answering mentions of %s in channels", w.deps.AgentID)
	go func() {
		t := time.NewTicker(mentionPoll)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			case <-w.poke:
			}
			w.sweep(ctx)
		}
	}()
}

// sweep answers every unread mention of this agent. Each agent reads only its
// own mentions, so two gateways never race for one and no locking is needed
// beyond this process.
func (w *MentionWatcher) sweep(ctx context.Context) {
	w.mu.Lock()
	defer w.mu.Unlock()

	msgs, err := w.deps.Coord.UnreadMentions(coord.MemberAgent, w.deps.AgentID, MentionsPerSweep)
	if err != nil {
		log.Printf("mentions: read unread: %v", err)
		return
	}
	// Oldest first: a conversation answered backwards is not a conversation.
	for i := len(msgs) - 1; i >= 0; i-- {
		if ctx.Err() != nil {
			return
		}
		w.answer(ctx, msgs[i])
	}
}

// answer runs one turn for one mention and posts the reply where the mention
// was. Every exit path clears the mention: a mention that stays unread after
// being looked at would be answered again on the next sweep, forever.
func (w *MentionWatcher) answer(ctx context.Context, m coord.ChannelMessage) {
	clear := func(why string) {
		if _, err := w.deps.Coord.MarkMentionsRead(coord.MemberAgent, w.deps.AgentID, []string{m.ID}); err != nil {
			log.Printf("mentions: clear %s: %v", m.ID, err)
		}
		if why != "" {
			log.Printf("mentions: %s (%s)", why, m.ID)
		}
	}

	if age := time.Since(m.CreatedAt); age > StaleMention {
		clear(fmt.Sprintf("skipped a mention %s old", age.Round(time.Hour)))
		return
	}

	key := coord.ThreadKey(m)
	mem, err := w.deps.Coord.GetThreadSession(key, w.deps.AgentID)
	if err != nil {
		log.Printf("mentions: thread session %s: %v", key, err)
		return
	}
	if mem.Turns >= MaxThreadTurns {
		clear(fmt.Sprintf("thread %s is at its %d-turn cap", key, MaxThreadTurns))
		return
	}

	sess := session.New(channelChatID)
	sess.ID = "ch_" + key
	// The manager owns the database row, and messages has a foreign key to it.
	// Without this the turn ran, the reply arrived, and both the question and
	// the answer were dropped on the floor with a log line nobody was reading.
	if w.deps.Sessions != nil {
		sess = w.deps.Sessions.Adopt(sess)
	}
	// Resuming is what makes turn 2 remember turn 1. A ref from the other CLI
	// — after a provider switch — is simply not used; only the CLI that owns
	// a session can resume it.
	if mem.SessionID != "" && mem.Provider == w.deps.ProviderName {
		sess.SetSessionID(mem.SessionID)
		sess.SetProvider(mem.Provider)
	}

	turnCtx, cancel := context.WithTimeout(ctx, mentionTurnTimeout)
	defer cancel()

	// Build the prompt BEFORE announcing anything. The progress line is a
	// message from this agent, so posting it first would make the thread look
	// as though this agent had just spoken — and "what was said while you were
	// away" would come back empty every time. A test caught exactly that.
	prompt := w.prompt(m, mem, key)

	// Say something before doing anything. A turn that reads six files takes
	// long enough that a silent thread is indistinguishable from a broken one,
	// and the person watching has no way to tell which. This line is a real
	// message in the thread, and it becomes the answer when the answer exists
	// — progress is worth seeing while it happens and noise the moment it
	// stops, so it is edited rather than added to.
	progress, _, err := w.deps.Coord.PostMessage(coord.NewChannelMessage{
		ChannelID: m.ChannelID, ThreadRoot: key,
		AuthorKind: coord.MemberAgent, AuthorID: w.deps.AgentID,
		Body: workingLine(nil, "", time.Time{}),
	})
	if err != nil {
		log.Printf("mentions: could not open a progress line: %v", err)
		progress = nil // the turn still runs; it just runs unseen
	}

	sink := &replySink{preview: chat.DefaultPreview()}
	if progress != nil {
		sink.onProgress = func(tools []string, partial string, since time.Time) {
			if err := w.deps.Coord.UpdateMessageBody(progress.ID, workingLine(tools, partial, since)); err != nil {
				log.Printf("mentions: progress update: %v", err)
			}
		}
	}

	sess.MarkRunning("channel: " + truncateLine(m.Body, 40))
	w.live.Store(m.ID, sess)
	_, err = w.deps.Turn.RunTurn(turnCtx, sess, sess.ChatID, w.model(), prompt, sink)
	// The engine stops WAITING for a turn when the deadline passes; the turn
	// itself keeps going in its lane. Its sink was still writing into the room
	// afterwards, so a line this code had already closed came back to life
	// saying "working" — and stayed that way. Nothing this turn produces from
	// here is ours to publish.
	sink.stop()
	sess.MarkIdle()
	w.live.Delete(m.ID)
	if err != nil {
		// A turn killed by shutdown is not an answered question. The gateway
		// was restarting — a deploy, usually — and the person asked something
		// that nobody will ever come back to if the mention is marked read
		// here. Leave it: the next gateway sweeps unread mentions at startup
		// and picks it up. Our own timeout is a different thing and does count,
		// because retrying it forever would just burn the same three minutes.
		if errors.Is(err, context.Canceled) && ctx.Err() != nil {
			log.Printf("mentions: %s interrupted by shutdown — leaving it unread for the next run", m.ID)
			w.finishProgress(progress, "")
			return
		}
		log.Printf("mentions: turn for %s: %v", m.ID, err)
		note := "⚠️ lượt này hỏng giữa chừng: " + truncateLine(err.Error(), 200)
		if errors.Is(err, context.DeadlineExceeded) {
			// Say what to do about it. "Deadline exceeded" tells the person
			// nothing they can act on; the shape of the fix is a task, which
			// has five times the budget and survives across runs.
			note = fmt.Sprintf("⌛ hết %s cho một lượt trả lời — việc này dài hơn một câu trả lời. "+
				"Nhờ lại và bảo mở task (`bomclaw task new --thread ...`), task chạy được lâu hơn và "+
				"giữ tiến độ qua nhiều lượt.", mentionTurnTimeout)
		}
		w.finishProgress(progress, note)
		clear("")
		return
	}

	reply := strings.TrimSpace(sink.Text())
	if reply == "" {
		// Nothing to say, so nothing should be left saying it is thinking.
		w.finishProgress(progress, "")
		clear("turn produced nothing to say")
		return
	}

	// The answer takes the place of the progress line: same message, same spot
	// in the thread, so the conversation reads as one reply rather than a
	// running commentary with the point at the end.
	if progress != nil {
		if err := w.deps.Coord.UpdateMessageBody(progress.ID, reply); err != nil {
			log.Printf("mentions: could not land the reply in place: %v", err)
		}
		// Mentions inside the final text still have to ring: the progress line
		// was written before the agent knew what it would say.
		for _, who := range w.mentionedAgents(reply) {
			NotifyAgents(w.deps.Coord, who, w.deps.AgentID, "about a mention in "+m.ChannelID)
		}
	} else if _, wake, err := w.deps.Coord.PostMessage(coord.NewChannelMessage{
		ChannelID:  m.ChannelID,
		ThreadRoot: key,
		AuthorKind: coord.MemberAgent,
		AuthorID:   w.deps.AgentID,
		Body:       reply,
	}); err != nil {
		log.Printf("mentions: post reply to %s: %v", m.ChannelID, err)
	} else {
		for _, who := range wake {
			NotifyAgents(w.deps.Coord, who, "", "about a mention in "+m.ChannelID)
		}
	}

	if err := w.deps.Coord.SaveThreadSession(key, w.deps.AgentID, w.deps.ProviderName, sess.GetSessionID()); err != nil {
		log.Printf("mentions: save thread session %s: %v", key, err)
	}
	clear("")
}

func (w *MentionWatcher) model() string {
	if w.deps.Resolver == nil {
		return ""
	}
	return w.deps.Resolver.Default()
}

// prompt carries the three things the agent cannot look up: what was said and
// by whom, that its reply is posted into this thread rather than spoken to a
// user, and what to do when the ask turns out to be bigger than a reply.
func (w *MentionWatcher) prompt(m coord.ChannelMessage, mem *coord.ThreadSession, key string) string {
	var b strings.Builder
	channel := m.ChannelID
	if c, err := w.deps.Coord.GetChannel(m.ChannelID); err == nil {
		channel = "#" + c.Name
	}

	if mem.Turns > 0 {
		fmt.Fprintf(&b, "You were named again in %s (your turn %d of %d in this thread).\n\n",
			channel, mem.Turns+1, MaxThreadTurns)
	} else {
		fmt.Fprintf(&b, "You were named in %s, a room you share with the other agents and the owner.\n\n", channel)
	}

	b.WriteString(w.threadContext(m, mem))

	fmt.Fprintf(&b, "## %s said\n\n%s\n\n", m.AuthorID, strings.TrimSpace(m.Body))
	b.WriteString(w.threadArtifacts(m))
	b.WriteString(w.schedules())
	b.WriteString(w.roster())

	b.WriteString("## How to answer\n\n")
	b.WriteString("Reply in plain prose. What you write is posted into that thread as your " +
		"message — it is not spoken to a user in a chat window, and nobody sees a preamble " +
		"about what you are about to do.\n")
	b.WriteString("Name an agent with @ only when you actually need it to act: @ is a doorbell " +
		"and wakes that agent.\n")
	fmt.Fprintf(&b, "Anything you produce that outlives this message — a report, a patch, a "+
		"page — attach it with `bomclaw artifact put --task <task> --file <path> --title <what it is>`. "+
		"A path in prose is findable for about a day; an artifact is findable by id, survives the file "+
		"moving, and is what the next agent asked to review it will open.\n")
	fmt.Fprintf(&b, "If this asks for real work — something with steps, or longer than a few "+
		"minutes — say so briefly and open a task for it with `bomclaw task new --thread %s`. "+
		"The --thread is what keeps the work attached to this conversation: the thread shows "+
		"the task it produced, and the result is posted back here when it finishes, so nobody "+
		"has to watch the board for an answer they asked for in a room. The board is for work; "+
		"this room is for talking about it.\n", key)
	return b.String()
}

// threadArtifacts lists what this conversation has already produced.
//
// Without it an agent asked to "review the report" has a filename at best and
// a guess at worst, and a second agent brought in later has neither. With it,
// the work product of the thread is addressable by id: the same id the
// producer wrote it under, readable with one command, and stable even after
// somebody moves the file.
func (w *MentionWatcher) threadArtifacts(m coord.ChannelMessage) string {
	root := m.ThreadRoot
	if root == "" {
		root = m.ID
	}
	rootMsg, err := w.deps.Coord.GetMessage(root)
	if err != nil || rootMsg.TaskID == "" {
		return "" // a thread with no task behind it has produced nothing yet
	}
	task, err := w.deps.Coord.GetTask(rootMsg.TaskID)
	if err != nil {
		return ""
	}
	arts, err := w.deps.Coord.ContextArtifacts(task.ContextID)
	if err != nil || len(arts) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## What this conversation has produced\n\n")
	for _, a := range arts {
		fmt.Fprintf(&b, "- `%s` — %s, %s", a.ID, a.Kind, a.Title)
		if a.Bytes > 0 {
			fmt.Fprintf(&b, " (%d bytes)", a.Bytes)
		}
		b.WriteString("\n")
	}
	b.WriteString("\nRead one with `bomclaw artifact get <id>` — by id, not by path, so it still " +
		"resolves after the file moves. If you are asked to review one, read it first and say what " +
		"is wrong with it; agreeing with a file you have not opened is worse than not answering.\n\n")
	return b.String()
}

// schedules tells the agent it can put work on a clock, and whether anything
// on this machine would actually run it.
//
// Asked to "check the price every five minutes", an agent that has not been
// told about schedules will either refuse or promise to remember — and then
// not, because it does not run between turns. The command has existed since
// P1a; nothing ever said so in a prompt.
//
// The second half matters as much: a schedule row is inert unless some gateway
// has schedules.enabled. Letting an agent create one into a machine where
// nothing fires it is worse than refusing, because it looks like it worked.
func (w *MentionWatcher) schedules() string {
	var b strings.Builder
	b.WriteString("## Work on a clock\n\n")
	if !w.deps.SchedulesRun {
		// Accurate rather than sweeping: this gateway knows its own config and
		// not its peers'. A row created here is not wrong, it is waiting — and
		// saying which of those it is beats guessing for the whole machine.
		b.WriteString("This gateway does not fire schedules (`schedules.enabled` is off here). " +
			"You can still create one — `bomclaw schedule add` writes to the shared database — but it " +
			"waits for a gateway that does run them. Say that when you create one, so nobody is left " +
			"expecting it to go off.\n\n")
		return b.String()
	}
	b.WriteString("`bomclaw schedule add --name <short-name> --every 5m --agent-task \"<what to do>\" " +
		"[--body \"<detail>\"] [--to <agent>]` puts work on a clock. `--cron \"0 8 * * 1-5\"` for a " +
		"time of day, `--at <RFC3339>` for once.\n")
	b.WriteString("A schedule does not run a model by itself: at each tick it creates an ordinary " +
		"task, and whichever agent claims it does the work. So write the task title as an " +
		"instruction someone else could follow — the agent that claims it will not have this " +
		"conversation.\n")
	b.WriteString("`bomclaw schedule list|show|disable|remove` for the rest. Tell the person the " +
		"name you gave it, so they can turn it off without asking you.\n\n")
	return b.String()
}

// roster is who else is here, generated from the agents table.
//
// It used to be a paragraph in each agent's config, hand-written and copied.
// That shape guarantees drift and duly delivered it: agent 1 and agent 2 were
// still describing a two-agent machine months after the third arrived, and
// agent 1 said so out loud in a thread — "bomclaw3 là agent nào thì em vẫn
// chưa biết". A roster that has to be edited in three files when a fourth
// agent appears is a roster that will be wrong.
//
// Generated, it cannot drift: the same table `bomclaw agents` reads, which
// every gateway writes to at startup with its own provider and model.
func (w *MentionWatcher) roster() string {
	agents, err := w.deps.Coord.ListAgents()
	if err != nil || len(agents) <= 1 {
		return ""
	}
	var lines []string
	for _, a := range agents {
		if a.ID == w.deps.AgentID {
			continue
		}
		state := "online"
		if !a.Online {
			state = "offline right now"
		}
		backend := a.Provider
		if a.Model != "" {
			backend = fmt.Sprintf("%s · %s", a.Provider, a.Model)
		}
		lines = append(lines, fmt.Sprintf("- **%s** — %s (%s)", a.ID, backend, state))
	}
	if len(lines) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## The other agents on this machine\n\n")
	for _, l := range lines {
		b.WriteString(l)
		b.WriteString("\n")
	}
	b.WriteString("\nThey run different backends, so they are good at different things and cost " +
		"different amounts. Name one with @ to bring it into this thread — it arrives having read " +
		"the conversation. Hand work over with `bomclaw task new --to <agent>`; " +
		"`bomclaw agents` is the same list, live.\n\n")
	return b.String()
}

// threadContext is what the agent needs to read before answering, and it is a
// different thing depending on whether it has been here before.
//
// An agent named into a thread for the first time has no CLI session for it, so
// this prompt is the ONLY thing it will ever know about the conversation. It
// gets the whole thread, generously: being handed "@you what do you think" with
// four hundred characters of somebody else's analysis is how you get an agent
// confidently answering a question nobody asked.
//
// An agent that has spoken here resumes its own session and remembers its own
// turns. Re-pasting the whole thread at it every time is not free and not
// clarifying — it needs what happened WHILE IT WAS AWAY, which is everything
// after its own last message.
func (w *MentionWatcher) threadContext(m coord.ChannelMessage, mem *coord.ThreadSession) string {
	if m.ThreadRoot == "" {
		return "" // a line that starts a thread has nothing behind it
	}
	thread, err := w.deps.Coord.ThreadMessages(m.ThreadRoot)
	if err != nil || len(thread) <= 1 {
		return ""
	}

	newHere := mem.Turns == 0
	from := 0
	if !newHere {
		for i := len(thread) - 1; i >= 0; i-- {
			if thread[i].AuthorKind == coord.MemberAgent && thread[i].AuthorID == w.deps.AgentID {
				from = i + 1
				break
			}
		}
	}

	var lines []string
	for _, t := range thread[from:] {
		if t.ID == m.ID {
			continue // it is quoted on its own below
		}
		who := t.AuthorID
		if t.AuthorKind == coord.MemberUser {
			who = "the owner"
		}
		lines = append(lines, fmt.Sprintf("**%s:** %s", who, truncateRunes(strings.TrimSpace(t.Body), MaxThreadMessageRunes)))
	}
	if len(lines) == 0 {
		return ""
	}

	// A long thread is trimmed from the FRONT: the oldest messages are the
	// ones a reader can most afford to lose, and dropping the newest would
	// hide the turn this one is answering.
	dropped := 0
	for total(lines) > MaxThreadContextRunes && len(lines) > 1 {
		lines = lines[1:]
		dropped++
	}

	var b strings.Builder
	if newHere {
		b.WriteString("## The conversation you have just been brought into\n\n" +
			"You have not spoken here before, so this is all of it. Read it before answering: " +
			"the question below assumes it.\n\n")
	} else {
		b.WriteString("## What was said while you were away\n\n" +
			"You remember your own side of this thread. These are the messages since your last one.\n\n")
	}
	if dropped > 0 {
		fmt.Fprintf(&b, "_(%d earlier message(s) omitted for length)_\n\n", dropped)
	}
	for _, l := range lines {
		b.WriteString(l)
		b.WriteString("\n\n")
	}
	return b.String()
}

func total(lines []string) int {
	n := 0
	for _, l := range lines {
		n += len([]rune(l))
	}
	return n
}

func truncateRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// replySink collects a turn's text and, if the caller asked for one, reports
// progress as it goes.
//
// Throttled on purpose. The interesting thing about a long turn is WHICH tools
// it is reaching for, and that changes on the order of seconds; writing the
// room a new line per token would cost a database write per token to tell the
// reader something they cannot read that fast anyway.
type replySink struct {
	mu      sync.Mutex
	b       strings.Builder
	tools   []string
	started time.Time
	last    time.Time

	// onProgress is called with the tools used so far and the reply as it
	// stands. Nil when nobody is watching.
	onProgress func(tools []string, partial string, since time.Time)

	preview *chat.StreamPreview
	done    bool
}

// stop ends progress reporting. A turn whose caller has given up keeps running
// in its lane, and anything it writes after that belongs to nobody.
func (r *replySink) stop() {
	r.mu.Lock()
	r.done = true
	r.mu.Unlock()
}

const progressEvery = 2 * time.Second

// Write redraws when the answer has grown by enough to be worth reading again
// — a character threshold rather than a timer, because a timer fires mid-word
// as often as not and a reader learns more from a paragraph landing whole.
func (r *replySink) Write(chunk string) {
	r.mu.Lock()
	r.b.WriteString(chunk)
	grown := r.preview.Ready(r.b.String())
	r.mu.Unlock()
	r.report(grown)
}

// NoteTool is the part worth watching: "reading the log" says more about what
// a turn is doing than the half-sentence it has written so far, so a new tool
// always reports immediately rather than waiting out the interval.
func (r *replySink) NoteTool(label string) {
	if label == "" {
		return
	}
	r.mu.Lock()
	if len(r.tools) == 0 || r.tools[len(r.tools)-1] != label {
		r.tools = append(r.tools, label)
	}
	r.mu.Unlock()
	r.report(true)
}

func (r *replySink) report(now bool) {
	r.mu.Lock()
	if r.onProgress == nil || r.done {
		r.mu.Unlock()
		return
	}
	if r.started.IsZero() {
		r.started = time.Now()
	}
	if !now && time.Since(r.last) < progressEvery {
		r.mu.Unlock()
		return
	}
	r.last = time.Now()
	tools := append([]string(nil), r.tools...)
	partial := r.b.String()
	started := r.started
	fn := r.onProgress
	r.mu.Unlock()

	fn(tools, partial, started)
}

func (r *replySink) Text() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.b.String()
}

func (r *replySink) Flush()                   {}
func (r *replySink) SendPhoto(string, string) {}
func (r *replySink) Finalize()                {}

// workingLine is what the room sees while the agent is still working.
func workingLine(tools []string, partial string, since time.Time) string {
	var b strings.Builder
	b.WriteString(coord.ProgressPrefix + "_đang làm_")
	if !since.IsZero() {
		fmt.Fprintf(&b, " · %s", time.Since(since).Round(time.Second))
	}
	if len(tools) > 0 {
		// The last few, newest last: what it is doing now matters more than
		// what it did first, and the whole list gets long on a real task.
		from := 0
		if len(tools) > 5 {
			from = len(tools) - 5
		}
		fmt.Fprintf(&b, " · %s", strings.Join(tools[from:], " → "))
	}
	if p := strings.TrimSpace(partial); p != "" {
		b.WriteString("\n\n")
		b.WriteString(chat.DefaultPreview().Cut(p))
	}
	return b.String()
}

// finishProgress replaces the progress line with its final form, or removes it
// when there is nothing to replace it with. A thread must never be left with
// an agent that is permanently about to say something.
func (w *MentionWatcher) finishProgress(progress *coord.ChannelMessage, body string) {
	if progress == nil {
		return
	}
	if strings.TrimSpace(body) == "" {
		if err := w.deps.Coord.DeleteMessage(progress.ID); err != nil {
			log.Printf("mentions: could not remove the progress line: %v", err)
		}
		return
	}
	if err := w.deps.Coord.UpdateMessageBody(progress.ID, body); err != nil {
		log.Printf("mentions: could not close the progress line: %v", err)
	}
}

// mentionedAgents finds the peers named in a finished reply. The progress line
// was posted before the agent knew what it would write, so its @ names were
// not there to be resolved at post time.
func (w *MentionWatcher) mentionedAgents(body string) []string {
	agents, err := w.deps.Coord.ListAgents()
	if err != nil {
		return nil
	}
	var out []string
	for _, a := range agents {
		if a.ID == w.deps.AgentID {
			continue
		}
		if strings.Contains(body, "@"+a.ID) {
			out = append(out, a.ID)
		}
	}
	return out
}

func truncateLine(s string, n int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}
