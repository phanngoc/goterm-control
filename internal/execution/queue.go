package execution

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"
)

// Engine manages per-session FIFO execution queues.
// Only one RunFunc executes at a time per chatID, preventing concurrent
// Claude CLI calls that would corrupt conversation state.
type Engine struct {
	mu    sync.Mutex
	lanes map[int64]*lane
	hooks Hooks
}

type lane struct {
	ch chan request
}

type request struct {
	fn     RunFunc
	ctx    context.Context
	result chan *RunResult
}

// NewEngine creates a new execution engine with the given hooks.
func NewEngine(hooks Hooks) *Engine {
	return &Engine{
		lanes: make(map[int64]*lane),
		hooks: hooks,
	}
}

// Enqueue submits work to the per-chatID queue and blocks until completion.
// Returns the RunResult or an error if ctx is canceled before execution.
func (e *Engine) Enqueue(ctx context.Context, chatID int64, fn RunFunc) (*RunResult, error) {
	l := e.getOrCreateLane(chatID)
	resCh := make(chan *RunResult, 1)

	req := request{fn: fn, ctx: ctx, result: resCh}

	select {
	case l.ch <- req:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	select {
	case r := <-resCh:
		if r.Error != nil && r.Status == RunFailed {
			return r, r.Error
		}
		return r, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// QueueDepth returns the number of pending requests for a chatID.
func (e *Engine) QueueDepth(chatID int64) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	l, ok := e.lanes[chatID]
	if !ok {
		return 0
	}
	return len(l.ch)
}

// Close shuts down all lanes gracefully.
func (e *Engine) Close() {
	e.mu.Lock()
	defer e.mu.Unlock()
	for id, l := range e.lanes {
		close(l.ch)
		delete(e.lanes, id)
	}
}

func (e *Engine) getOrCreateLane(chatID int64) *lane {
	e.mu.Lock()
	defer e.mu.Unlock()

	if l, ok := e.lanes[chatID]; ok {
		return l
	}

	l := &lane{
		ch: make(chan request, 32),
	}
	e.lanes[chatID] = l

	go l.run(e.hooks)
	return l
}

func (l *lane) run(hooks Hooks) {
	for req := range l.ch {
		result := l.executeOne(req, hooks)
		req.result <- result
	}
}

func (l *lane) executeOne(req request, hooks Hooks) (result *RunResult) {
	result = &RunResult{
		ID:        fmt.Sprintf("run_%d", time.Now().UnixNano()),
		StartedAt: time.Now(),
		Status:    RunSuccess,
	}

	// Recover from panics so a single bad request doesn't kill the lane.
	defer func() {
		if r := recover(); r != nil {
			log.Printf("execution: panic in lane: %v", r)
			result.Status = RunFailed
			result.Error = fmt.Errorf("panic: %v", r)
			result.EndedAt = time.Now()
			hooks.fireAfterRun(req.ctx, result)
		}
	}()

	hooks.fireBeforeRun(req.ctx, result)

	if req.ctx.Err() != nil {
		result.Status = RunCanceled
		result.Error = req.ctx.Err()
		result.EndedAt = time.Now()
		hooks.fireAfterRun(req.ctx, result)
		return result
	}

	r, err := req.fn(req.ctx)
	if r != nil {
		result = r
	}
	if err != nil {
		result.Status = RunFailed
		result.Error = err
	}
	if result.EndedAt.IsZero() {
		result.EndedAt = time.Now()
	}

	hooks.fireAfterRun(req.ctx, result)
	return result
}
