package gateway

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

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
	// not a task: if it needs longer than this it needs a task.
	mentionTurnTimeout = 3 * time.Minute
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

	sink := &replySink{}
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
	sess.MarkIdle()
	w.live.Delete(m.ID)
	if err != nil {
		log.Printf("mentions: turn for %s: %v", m.ID, err)
		w.finishProgress(progress, "⚠️ lượt này hỏng giữa chừng: "+truncateLine(err.Error(), 200))
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
	b.WriteString(w.roster())

	b.WriteString("## How to answer\n\n")
	b.WriteString("Reply in plain prose. What you write is posted into that thread as your " +
		"message — it is not spoken to a user in a chat window, and nobody sees a preamble " +
		"about what you are about to do.\n")
	b.WriteString("Name an agent with @ only when you actually need it to act: @ is a doorbell " +
		"and wakes that agent.\n")
	fmt.Fprintf(&b, "If this asks for real work — something with steps, or longer than a few "+
		"minutes — say so briefly and open a task for it with `bomclaw task new --thread %s`. "+
		"The --thread is what keeps the work attached to this conversation: the thread shows "+
		"the task it produced, and the result is posted back here when it finishes, so nobody "+
		"has to watch the board for an answer they asked for in a room. The board is for work; "+
		"this room is for talking about it.\n", key)
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
}

const progressEvery = 2 * time.Second

func (r *replySink) Write(chunk string) {
	r.mu.Lock()
	r.b.WriteString(chunk)
	r.mu.Unlock()
	r.report(false)
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
	if r.onProgress == nil {
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
	b.WriteString("⏳ _đang làm_")
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
		b.WriteString(truncateRunes(p, 600))
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
