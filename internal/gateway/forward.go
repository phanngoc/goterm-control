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

// Carrying a bound room out to the places it speaks into.
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
	// notifications the moment it comes back. It is counted per destination,
	// not per sweep: see coord.PendingForwards on why a shared budget lets one
	// dead endpoint starve every healthy one beside it.
	ForwardsPerSweep = 10

	// MaxForwardRunes is how much of one line travels. Telegram's own cap is
	// 4096 bytes, and an agent's answer can be far longer than that; past this
	// the dashboard is the place to read it.
	MaxForwardRunes = 2800

	// failureCooldown is how long a destination is left alone after it refuses
	// a line. Without it a webhook pointed at a dead host takes a POST every
	// sweep for every line it is behind on, forever.
	//
	// Deliberately in memory rather than in the database: this is a delay, not
	// a fact about the line. A restart trying again immediately is the right
	// behaviour, because a restart is usually what fixed it.
	failureCooldown = 60 * time.Second
)

// ForwardWatcher pushes bound rooms' lines out to their destinations.
type ForwardWatcher struct {
	deps       Deps
	transports map[string]ChannelTransport
	poke       chan struct{}

	mu     sync.Mutex
	failed map[string]time.Time // gateway id → when it last refused a line
	quiet  map[string]bool      // gateway id → already complained about
	// backoff is failureCooldown, as a field so a test can watch a retry
	// without waiting a minute for it.
	backoff time.Duration
}

// NewForwardWatcher returns nil unless this gateway can actually deliver
// something.
//
// Every gateway process builds one, and that is fine: a gateway row names the
// agent whose process carries it, so each watcher only ever sees its own
// destinations. There is no race to lose and no line that two processes could
// both send — which is what makes several destinations on one room safe.
func NewForwardWatcher(deps Deps, transports ...ChannelTransport) *ForwardWatcher {
	if deps.Coord == nil || deps.AgentID == "" {
		return nil
	}
	byKind := map[string]ChannelTransport{}
	for _, t := range transports {
		if t == nil {
			continue // a gateway that does not poll has no Telegram transport
		}
		byKind[t.Kind()] = t
	}
	if len(byKind) == 0 {
		return nil
	}
	return &ForwardWatcher{
		deps: deps, transports: byKind, poke: make(chan struct{}, 1),
		failed: map[string]time.Time{}, quiet: map[string]bool{},
		backoff: failureCooldown,
	}
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
	kinds := make([]string, 0, len(w.transports))
	for k := range w.transports {
		kinds = append(kinds, k)
	}
	log.Printf("forward: carrying bound rooms out over %s", strings.Join(kinds, ", "))
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
	// One room with three destinations asks the same "does the owner follow
	// this line" question three times. The answer cannot change inside a
	// sweep, so it is asked once.
	follows := map[string]bool{}
	for _, f := range pending {
		w.deliver(f, follows)
	}
}

// deliver decides about one line for one destination and settles it either way.
//
// "Settles" is the important half: a line the mode filters out is recorded as
// handled with no external id, because the mode will not have changed its mind
// by the next sweep and re-asking forever is how a poll loop becomes a
// treadmill.
func (w *ForwardWatcher) deliver(f coord.Forward, follows map[string]bool) {
	transport := w.transports[f.Kind]
	if transport == nil {
		// A row this process cannot serve is a misconfiguration to fix, not a
		// line to throw away — so it is left pending rather than settled. coord
		// refuses to create one, so this is close to unreachable; the log names
		// the row once so that "close to" is debuggable.
		if !w.quiet[f.GatewayID] {
			w.quiet[f.GatewayID] = true
			log.Printf("forward: %s carries %s gateway %s but registered no %s transport — its lines are waiting",
				w.deps.AgentID, f.Kind, f.GatewayID, f.Kind)
		}
		return
	}
	if last, ok := w.failed[f.GatewayID]; ok {
		if time.Since(last) < w.backoff {
			return
		}
		delete(w.failed, f.GatewayID)
	}

	if f.Mode == coord.ForwardMentions {
		ok, cached := follows[f.MessageID]
		if !cached {
			var err error
			ok, err = w.deps.Coord.OwnerFollows(f.MessageID, f.ThreadRoot)
			if err != nil {
				log.Printf("forward: %s: %v", f.MessageID, err)
				return // a read that failed is not a decision; try again next sweep
			}
			follows[f.MessageID] = ok
		}
		if !ok {
			if err := w.deps.Coord.RecordDelivery(f, coord.DeliverySkipped, ""); err != nil {
				log.Printf("forward: settle %s: %v", f.MessageID, err)
			}
			return
		}
	}

	ext, err := transport.Deliver(f, ForwardLine(f))
	if err != nil {
		// Left unsettled on purpose: an outage should delay the line, not
		// swallow it. The cooldown is what keeps that from becoming a retry
		// storm at a host that is not coming back this minute.
		log.Printf("forward: send %s to %s %s: %v", f.MessageID, f.Kind, f.Target, err)
		w.failed[f.GatewayID] = time.Now()
		return
	}
	if err := w.deps.Coord.RecordDelivery(f, coord.DeliverySent, ext); err != nil {
		log.Printf("forward: settle %s: %v", f.MessageID, err)
	}
}

// ForwardLine is what a person reads at the other end.
//
// The room's name leads, because every bound room arrives in the same
// conversation: without it two projects read as one. The author is next —
// three agents write here, and which one answered is usually the point.
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
