// Package reporter closes the return leg of a delegated task: it tells the
// person who asked what came of work their agent handed to a peer.
//
// The queue is a pull system — "poll, claim, run, report. The database is the
// source of truth" — and that reasoning was only ever applied to the executor.
// The requester had no loop at all, so a peer's result landed in tasks.result
// and stopped there: no query read it back, and Bot.Notify had exactly one
// producer in the tree (the scheduler). This is the missing half.
//
// It is deliberately the same shape as scheduler.settle, which already does
// this job for scheduled work: list what I delegated, ask whether it finished,
// win a compare-and-set so only one gateway delivers, then notify.
//
// # Why this is its own loop
//
// The two loops that already tick are both the wrong host. taskrunner starts
// only when tasks.auto_claim is true and scheduler only when schedules.enabled
// is true — and a gateway delegates precisely because it does not run the work
// itself, so it is exactly the gateway with both switched off. This one starts
// wherever the coordination database is open, the same condition that already
// mounts /api/tasks/poke "even when this agent does not claim tasks".
package reporter

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/ngocp/goterm-control/internal/coord"
)

// maxResultChars bounds one delivery. Telegram rejects a message over 4096
// characters outright, and a truncated result the user can follow up on beats a
// send that fails silently.
const maxResultChars = 3500

// Config tunes the loop. Zero values take the defaults noted.
type Config struct {
	AgentID string
	Tick    time.Duration // how often to look for finished work; default 30s
	Batch   int           // most reports to deliver per tick; default 20
}

func (c *Config) defaults() {
	if c.Tick <= 0 {
		c.Tick = 30 * time.Second
	}
	if c.Batch <= 0 {
		c.Batch = 20
	}
}

// Reporter is the loop. Create with New, wire SetNotify, then Start.
type Reporter struct {
	db  *coord.DB
	cfg Config

	notify func(text string) // deliver a line to the owner; nil = log only
	now    func() time.Time  // test seam
	poke   chan struct{}
	work   sync.WaitGroup
}

// New builds a reporter. It does nothing until Start.
func New(db *coord.DB, cfg Config) *Reporter {
	cfg.defaults()
	return &Reporter{
		db:   db,
		cfg:  cfg,
		now:  time.Now,
		poke: make(chan struct{}, 1),
	}
}

// SetNotify installs the delivery path — in the gateway, the owner's Telegram.
func (r *Reporter) SetNotify(fn func(text string)) {
	if r == nil {
		return
	}
	r.notify = fn
}

// Poke asks for a tick now, so a result does not wait out the interval. Safe to
// call on a nil Reporter, which is what a gateway without coord has.
func (r *Reporter) Poke() {
	if r == nil {
		return
	}
	select {
	case r.poke <- struct{}{}:
	default:
	}
}

// Start runs the loop until ctx ends. The first tick is immediate: results that
// arrived while this gateway was down are still owed a report.
func (r *Reporter) Start(ctx context.Context) {
	if r == nil {
		return
	}
	if r.notify == nil {
		log.Printf("reporter: no delivery path (no Telegram on this gateway) — " +
			"delegated results are left for a peer that has one")
		return
	}
	log.Printf("reporter: reporting delegated results as %s every %s", r.cfg.AgentID, r.cfg.Tick)
	r.work.Add(1)
	go func() {
		defer r.work.Done()
		t := time.NewTicker(r.cfg.Tick)
		defer t.Stop()
		r.Tick()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			case <-r.poke:
			}
			r.Tick()
		}
	}()
}

// Wait blocks until the loop has stopped. Used by tests and shutdown.
func (r *Reporter) Wait() {
	if r == nil {
		return
	}
	r.work.Wait()
}

// Tick is one pass: deliver the outcome of every finished task this agent
// delegated and nobody has reported yet. Exported so tests can drive it.
func (r *Reporter) Tick() {
	if r == nil || r.db == nil {
		return
	}

	// A gateway that cannot reach the owner must not touch the queue. Claiming
	// a report here would mark it delivered and then write it to a log nobody
	// reads, while the peer that does have Telegram never sees it again — the
	// marker makes silence permanent. Leaving it untouched costs nothing: any
	// gateway with a delivery path picks it up on its next tick.
	if r.notify == nil {
		return
	}

	pending, err := r.db.PendingReports(r.cfg.AgentID, r.cfg.Batch)
	if err != nil {
		log.Printf("reporter: %v", err)
		return
	}

	for i := range pending {
		task := pending[i]

		// Win the delivery before sending, never after. The other order would
		// send twice from two gateways and mark once.
		won, err := r.db.MarkReported(task.ID, r.now())
		if err != nil {
			log.Printf("reporter: %v", err)
			continue
		}
		if !won {
			continue // a peer got there first
		}

		r.notify(Format(&task))
		r.reportToThread(&task)
		log.Printf("reporter: delivered %s (%s) for task %s", task.State, task.ClaimedBy, task.ID)
	}
}

// reportToThread puts the outcome back where the work was asked for. A task
// that came out of a conversation has someone waiting in that conversation,
// and making them go and watch a board for the answer to a question they asked
// in a room is how a team stops using the room.
//
// Best effort, and deliberately after the owner's notification: the delivery
// is already claimed by then, so a failure here costs a thread post, never the
// report itself. It rides on that same claim, so two gateways cannot both post.
//
// Authored by the agent that ran the task rather than the one reporting — the
// reader wants to know who did the work. An agent's line in a thread wakes
// nobody, which is right: this is an answer, not a summons.
func (r *Reporter) reportToThread(t *coord.Task) {
	rootID, channelID, err := r.db.TaskThread(t.ID)
	if err != nil {
		log.Printf("reporter: thread of %s: %v", t.ID, err)
		return
	}
	if rootID == "" {
		return // not work that came out of a conversation
	}
	author := t.ClaimedBy
	if author == "" {
		author = r.cfg.AgentID
	}
	if _, _, err := r.db.PostMessage(coord.NewChannelMessage{
		ChannelID: channelID, ThreadRoot: rootID, AuthorID: author, Body: Format(t),
	}); err != nil {
		log.Printf("reporter: report %s into thread %s: %v", t.ID, rootID, err)
	}
}

// Format writes the line the owner sees.
//
// It leads with the outcome rather than the title because the outcome is what
// the reader is waiting for, and names the agent that ran it: with two agents
// on one machine, "done" without a name does not say whose work finished.
func Format(t *coord.Task) string {
	var b strings.Builder

	switch t.State {
	case coord.TaskCompleted:
		b.WriteString("✅ ")
	case coord.TaskFailed:
		b.WriteString("⛔ ")
	default: // canceled, rejected
		b.WriteString("⚠️ ")
	}

	b.WriteString(t.Title)
	if by := ranBy(t); by != "" {
		fmt.Fprintf(&b, "\n%s · %s", t.State, by)
	} else {
		fmt.Fprintf(&b, "\n%s", t.State)
	}
	if t.FailReason != "" {
		fmt.Fprintf(&b, " (%s)", t.FailReason)
	}

	if result := strings.TrimSpace(t.Result); result != "" {
		fmt.Fprintf(&b, "\n\n%s", truncate(result, maxResultChars))
	} else {
		b.WriteString("\n\n(finished without a result)")
	}

	return b.String()
}

// ranBy names the agent that executed the task, or nothing when it died before
// anyone claimed it — in which case saying "by ”" would be worse than silence.
func ranBy(t *coord.Task) string {
	if t.ClaimedBy != "" {
		return t.ClaimedBy
	}
	return ""
}

func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}
