package gateway

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/ngocp/goterm-control/internal/coord"
)

// Carrying a bound channel out to Telegram.
//
// Why a watcher and not a hook on the post: seven places write a line into a
// room — the dashboard's RPC, an agent answering a mention, an agent's own CLI,
// the task reporter, the "I opened a task for this" note, DMs, progress lines —
// and they are spread across three processes. A hook in gateway's post handler
// would forward the one the dashboard wrote and silently miss an agent's reply,
// which is the line the owner most wants. Putting it a layer down in
// coord.PostMessage is worse: that is the storage layer, and `bomclaw ch post`
// runs it in a process with no Telegram bot at all.
//
// So the table is the seam. Whoever wrote the line, it is a row; the watcher
// reads rows.

const (
	// forwardPoll is how quickly a line leaves the room. Most writers are in
	// other processes, so a poke cannot be relied on — this interval is the
	// real latency, and "a few seconds" is the bar the issue set.
	forwardPoll = 10 * time.Second

	// ForwardsPerSweep keeps a gateway that was down from firing a hundred
	// notifications the moment it comes back.
	ForwardsPerSweep = 10

	// MaxForwardRunes is how much of one line travels. Telegram's own cap is
	// 4096 bytes, and an agent's answer can be far longer than that; past this
	// the dashboard is the place to read it.
	MaxForwardRunes = 2800
)

// TelegramSender delivers one line and reports which Telegram message it
// became. That id is the whole return path: a reply quoting it is how an
// answer from the phone finds the thread it belongs to.
type TelegramSender interface {
	SendChannelLine(chatID int64, text string) (int64, error)
}

// ForwardWatcher pushes bound channels' lines to Telegram.
type ForwardWatcher struct {
	deps Deps
	send TelegramSender
	poke chan struct{}
	mu   sync.Mutex
}

// NewForwardWatcher returns nil unless this gateway can actually deliver.
//
// Every gateway builds one, and that is fine: a binding names the agent whose
// bot carries it, so each watcher only ever sees its own rooms. There is no
// race to lose and no line that two bots could both send.
func NewForwardWatcher(deps Deps, send TelegramSender) *ForwardWatcher {
	if deps.Coord == nil || send == nil || deps.AgentID == "" {
		return nil
	}
	return &ForwardWatcher{deps: deps, send: send, poke: make(chan struct{}, 1)}
}

// Poke asks for a sweep now. Non-blocking and coalescing, like the mention
// watcher's: a full channel means one is already on the way.
func (w *ForwardWatcher) Poke() {
	if w == nil {
		return
	}
	select {
	case w.poke <- struct{}{}:
	default:
	}
}

// Start runs the sweep loop until ctx ends.
func (w *ForwardWatcher) Start(ctx context.Context) {
	if w == nil {
		return
	}
	log.Println("forward: carrying bound channels to Telegram")
	go func() {
		t := time.NewTicker(forwardPoll)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			case <-w.poke:
			}
			w.sweep()
		}
	}()
}

func (w *ForwardWatcher) sweep() {
	w.mu.Lock()
	defer w.mu.Unlock()

	pending, err := w.deps.Coord.PendingForwards(w.deps.AgentID, coord.ProgressPrefix, ForwardsPerSweep)
	if err != nil {
		log.Printf("forward: read pending: %v", err)
		return
	}
	for _, f := range pending {
		w.deliver(f)
	}
}

// deliver decides about one line and settles it either way.
//
// "Settles" is the important half: a line the mode filters out is stamped as
// handled with no Telegram id, because the mode will not have changed its mind
// by the next sweep and re-asking forever is how a poll loop becomes a
// treadmill.
func (w *ForwardWatcher) deliver(f coord.Forward) {
	if f.Mode == coord.ForwardMentions {
		follows, err := w.deps.Coord.OwnerFollows(f.MessageID, f.ThreadRoot)
		if err != nil {
			log.Printf("forward: %s: %v", f.MessageID, err)
			return // a read that failed is not a decision; try again next sweep
		}
		if !follows {
			if err := w.deps.Coord.MarkForwarded(w.deps.AgentID, f.MessageID, 0); err != nil {
				log.Printf("forward: settle %s: %v", f.MessageID, err)
			}
			return
		}
	}

	tgID, err := w.send.SendChannelLine(f.ChatID, ForwardLine(f))
	if err != nil {
		// Left unsettled on purpose: a Telegram outage should delay the line,
		// not swallow it.
		log.Printf("forward: send %s to chat %d: %v", f.MessageID, f.ChatID, err)
		return
	}
	if err := w.deps.Coord.MarkForwarded(w.deps.AgentID, f.MessageID, tgID); err != nil {
		log.Printf("forward: settle %s: %v", f.MessageID, err)
	}
}

// ForwardLine is what the owner reads on their phone.
//
// The room's name leads, because on Telegram every bound channel arrives in
// the same conversation: without it two projects read as one. The author is
// next — three agents write here, and which one answered is usually the point.
func ForwardLine(f coord.Forward) string {
	room := f.ChannelName
	if room == "" {
		room = f.ChannelID
	}
	return fmt.Sprintf("*#%s · %s*\n%s", room, f.AuthorID, cutRunes(f.Body, MaxForwardRunes))
}

func cutRunes(s string, max int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return strings.TrimSpace(string(r[:max])) + "\n\n… (còn nữa — xem trong dashboard)"
}
