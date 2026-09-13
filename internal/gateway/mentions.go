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
	// Resuming is what makes turn 2 remember turn 1. A ref from the other CLI
	// — after a provider switch — is simply not used; only the CLI that owns
	// a session can resume it.
	if mem.SessionID != "" && mem.Provider == w.deps.ProviderName {
		sess.SetSessionID(mem.SessionID)
		sess.SetProvider(mem.Provider)
	}

	turnCtx, cancel := context.WithTimeout(ctx, mentionTurnTimeout)
	defer cancel()

	sink := &replySink{}
	sess.MarkRunning("channel: " + truncateLine(m.Body, 40))
	w.live.Store(m.ID, sess)
	_, err = w.deps.Turn.RunTurn(turnCtx, sess, sess.ChatID, w.model(), w.prompt(m, mem, key), sink)
	sess.MarkIdle()
	w.live.Delete(m.ID)
	if err != nil {
		log.Printf("mentions: turn for %s: %v", m.ID, err)
		clear("")
		return
	}

	reply := strings.TrimSpace(sink.Text())
	if reply == "" {
		clear("turn produced nothing to say")
		return
	}

	// The reply belongs where the mention was: in the thread if the mention was
	// in one, otherwise starting a thread under it — so the main line stays
	// readable and the exchange stays in one place. That place is the same key
	// the session is kept under, which is what makes the next turn remember.
	if _, wake, err := w.deps.Coord.PostMessage(coord.NewChannelMessage{
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

	// A thread has a history the model may not have seen — it may have been
	// answered by another agent, or by this one before a restart.
	if m.ThreadRoot != "" {
		if thread, err := w.deps.Coord.ThreadMessages(m.ThreadRoot); err == nil && len(thread) > 1 {
			b.WriteString("## The thread so far\n\n")
			for _, t := range thread {
				if t.ID == m.ID {
					continue
				}
				fmt.Fprintf(&b, "- %s: %s\n", t.AuthorID, truncateLine(t.Body, 400))
			}
			b.WriteString("\n")
		}
	}

	fmt.Fprintf(&b, "## %s said\n\n%s\n\n", m.AuthorID, strings.TrimSpace(m.Body))

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

// replySink collects a turn's text. A channel has no stream to write to: the
// reply is one message, posted when the turn is done.
type replySink struct {
	mu sync.Mutex
	b  strings.Builder
}

func (r *replySink) Write(chunk string) {
	r.mu.Lock()
	r.b.WriteString(chunk)
	r.mu.Unlock()
}

func (r *replySink) Text() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.b.String()
}

func (r *replySink) NoteTool(string)          {}
func (r *replySink) Flush()                   {}
func (r *replySink) SendPhoto(string, string) {}
func (r *replySink) Finalize()                {}

func truncateLine(s string, n int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}
