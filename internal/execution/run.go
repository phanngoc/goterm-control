package execution

import (
	"context"
	"time"
)

// RunStatus represents the outcome of a Claude CLI invocation.
type RunStatus string

const (
	RunSuccess  RunStatus = "success"
	RunFailed   RunStatus = "failed"
	RunCanceled RunStatus = "canceled"
)

// RunResult captures the outcome of a single execution.
type RunResult struct {
	ID           string
	SessionID    string
	Status       RunStatus
	StartedAt    time.Time
	EndedAt      time.Time
	Error        error
	InputTokens  int
	OutputTokens int
}

// Hook is a callback invoked before or after a run.
type Hook func(ctx context.Context, result *RunResult)

// Hooks holds before/after run callbacks.
type Hooks struct {
	BeforeRun []Hook
	AfterRun  []Hook
}

func (h *Hooks) fireBeforeRun(ctx context.Context, r *RunResult) {
	for _, fn := range h.BeforeRun {
		fn(ctx, r)
	}
}

func (h *Hooks) fireAfterRun(ctx context.Context, r *RunResult) {
	for _, fn := range h.AfterRun {
		fn(ctx, r)
	}
}

// RunFunc is the function signature for work submitted to the queue.
type RunFunc func(ctx context.Context) (*RunResult, error)
