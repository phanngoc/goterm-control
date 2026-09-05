// Package trace records what an agent actually did, as a tree of runs in the
// shared coordination database.
//
// The model is LangSmith's: one turn is a root run, the model call is a child,
// and every tool the model invoked is a child of that. A trace is everything
// sharing a trace id; dotted_order carries the ancestry so the tree comes back
// nested from one ordered query.
//
// Recording is fire-and-forget on a background writer. Tracing is diagnostics:
// it must never slow a turn down and must never be the reason a turn fails, so
// a full queue drops spans and says so in the log rather than blocking.
package trace

import (
	"log"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/ngocp/goterm-control/internal/coord"
)

// queueSize is generous enough for a burst of tool calls inside one turn.
const queueSize = 512

// Recorder writes runs for one agent. A nil *Recorder is valid and does
// nothing, so callers never need to check before starting a span.
type Recorder struct {
	db      *coord.DB
	agentID string

	ch     chan func()
	closed chan struct{}
	once   sync.Once

	mu      sync.Mutex
	dropped int
}

// New starts a recorder backed by db. Passing a nil db returns nil, which
// disables tracing without any call site having to care.
func New(db *coord.DB, agentID string) *Recorder {
	if db == nil {
		return nil
	}
	r := &Recorder{
		db:      db,
		agentID: agentID,
		ch:      make(chan func(), queueSize),
		closed:  make(chan struct{}),
	}
	go r.loop()
	return r
}

func (r *Recorder) loop() {
	defer close(r.closed)
	for fn := range r.ch {
		fn()
	}
}

// Close drains outstanding writes. Safe to call more than once.
func (r *Recorder) Close() {
	if r == nil {
		return
	}
	r.once.Do(func() {
		close(r.ch)
		<-r.closed
		if n := r.droppedCount(); n > 0 {
			log.Printf("trace: dropped %d spans (writer could not keep up)", n)
		}
	})
}

// submit queues a write, dropping it if the writer is behind.
func (r *Recorder) submit(fn func()) {
	select {
	case r.ch <- fn:
	default:
		r.mu.Lock()
		r.dropped++
		n := r.dropped
		r.mu.Unlock()
		if n == 1 || n%100 == 0 {
			log.Printf("trace: span queue full, dropping (total dropped=%d)", n)
		}
	}
}

func (r *Recorder) droppedCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.dropped
}

// Meta is the context a trace is opened with.
type Meta struct {
	SessionID string
	ChatID    int64
	Model     string
	Provider  string
	Tags      string // JSON array
}

// Span is one open run. Every method tolerates a nil receiver.
type Span struct {
	rec         *Recorder
	id          string
	traceID     string
	dottedOrder string
	meta        Meta
	startedAt   time.Time

	mu     sync.Mutex
	ended  bool
	inputs string
}

// StartTrace opens a root run — one whole agent turn.
func (r *Recorder) StartTrace(name, runType string, meta Meta) *Span {
	if r == nil {
		return nil
	}
	now := time.Now()
	id := uuid.NewString()
	s := &Span{
		rec: r, id: id, traceID: id,
		dottedOrder: coord.DottedOrderSegment(now, id),
		meta:        meta, startedAt: now,
	}
	r.insert(s, "", name, runType)
	return s
}

// Child opens a nested run under s.
func (s *Span) Child(name, runType string) *Span {
	if s == nil || s.rec == nil {
		return nil
	}
	now := time.Now()
	id := uuid.NewString()
	c := &Span{
		rec: s.rec, id: id, traceID: s.traceID,
		dottedOrder: s.dottedOrder + "." + coord.DottedOrderSegment(now, id),
		meta:        s.meta, startedAt: now,
	}
	s.rec.insert(c, s.id, name, runType)
	return c
}

func (r *Recorder) insert(s *Span, parentID, name, runType string) {
	run := &coord.Run{
		ID: s.id, TraceID: s.traceID, ParentRunID: parentID,
		DottedOrder: s.dottedOrder, AgentID: r.agentID,
		SessionID: s.meta.SessionID, ChatID: s.meta.ChatID,
		Name: name, RunType: runType, StartedAt: s.startedAt,
		Model: s.meta.Model, Provider: s.meta.Provider, Tags: s.meta.Tags,
	}
	r.submit(func() {
		if err := r.db.InsertRun(run); err != nil {
			log.Printf("trace: insert run: %v", err)
		}
	})
}

// SetInputs attaches the run's input payload. Recorded on End so a single
// UPDATE carries both, and so a caller may refine inputs mid-run.
func (s *Span) SetInputs(inputs string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.inputs = inputs
	s.mu.Unlock()
	rec, id := s.rec, s.id
	rec.submit(func() {
		if _, err := rec.db.Conn().Exec(`UPDATE runs SET inputs = ? WHERE id = ?`, inputs, id); err != nil {
			log.Printf("trace: set inputs: %v", err)
		}
	})
}

// TraceID identifies the trace this span belongs to — the handle a task or a
// dashboard row uses to link back to the waterfall.
func (s *Span) TraceID() string {
	if s == nil {
		return ""
	}
	return s.traceID
}

// End closes the run. A non-nil err marks it failed.
func (s *Span) End(outputs string, err error) {
	s.EndWithTokens(outputs, err, 0, 0)
}

// EndWithTokens closes the run and records token usage.
func (s *Span) EndWithTokens(outputs string, err error, inTok, outTok int) {
	if s == nil || s.rec == nil {
		return
	}
	s.mu.Lock()
	if s.ended {
		s.mu.Unlock()
		return // double End is a caller bug, not a reason to write twice
	}
	s.ended = true
	s.mu.Unlock()

	msg := ""
	if err != nil {
		msg = err.Error()
	}
	rec, id, now := s.rec, s.id, time.Now()
	rec.submit(func() {
		if err := rec.db.EndRun(id, now, outputs, msg, inTok, outTok); err != nil {
			log.Printf("trace: end run: %v", err)
		}
	})
}
