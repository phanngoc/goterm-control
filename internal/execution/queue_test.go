package execution

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestQueueSequentialExecution(t *testing.T) {
	engine := NewEngine(Hooks{}, 0)
	defer engine.Close()

	var order []int
	var mu sync.Mutex
	var running int32

	var wg sync.WaitGroup
	for i := range 3 {
		wg.Add(1)
		i := i
		go func() {
			defer wg.Done()
			_, err := engine.Enqueue(context.Background(), 1, func(ctx context.Context) (*RunResult, error) {
				// Check no concurrent execution
				if n := atomic.AddInt32(&running, 1); n > 1 {
					t.Errorf("concurrent execution detected: %d running", n)
				}
				time.Sleep(10 * time.Millisecond)
				mu.Lock()
				order = append(order, i)
				mu.Unlock()
				atomic.AddInt32(&running, -1)
				return &RunResult{Status: RunSuccess}, nil
			})
			if err != nil {
				t.Errorf("enqueue %d: %v", i, err)
			}
		}()
	}
	wg.Wait()

	if len(order) != 3 {
		t.Fatalf("expected 3 executions, got %d", len(order))
	}
}

func TestQueueCancelation(t *testing.T) {
	engine := NewEngine(Hooks{}, 0)
	defer engine.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err := engine.Enqueue(ctx, 1, func(ctx context.Context) (*RunResult, error) {
		t.Fatal("should not execute")
		return nil, nil
	})

	if err == nil {
		t.Fatal("expected error from canceled context")
	}
}

func TestQueuePanicRecovery(t *testing.T) {
	engine := NewEngine(Hooks{}, 0)
	defer engine.Close()

	// First request panics — Enqueue returns the error but the lane survives
	r1, err := engine.Enqueue(context.Background(), 1, func(ctx context.Context) (*RunResult, error) {
		panic("test panic")
	})
	if r1 == nil || r1.Status != RunFailed {
		t.Errorf("expected RunFailed after panic, got r1=%v err=%v", r1, err)
	}

	// Second request on same lane should still work (lane not dead)
	r2, err := engine.Enqueue(context.Background(), 1, func(ctx context.Context) (*RunResult, error) {
		return &RunResult{Status: RunSuccess}, nil
	})
	if err != nil {
		t.Fatalf("second enqueue failed: %v", err)
	}
	if r2.Status != RunSuccess {
		t.Errorf("expected RunSuccess after recovery, got %s", r2.Status)
	}
}

func TestQueueHooks(t *testing.T) {
	var beforeCalled, afterCalled bool

	engine := NewEngine(Hooks{
		BeforeRun: []Hook{func(ctx context.Context, r *RunResult) { beforeCalled = true }},
		AfterRun:  []Hook{func(ctx context.Context, r *RunResult) { afterCalled = true }},
	}, 0)
	defer engine.Close()

	_, err := engine.Enqueue(context.Background(), 1, func(ctx context.Context) (*RunResult, error) {
		return &RunResult{Status: RunSuccess}, nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// Small delay for hook execution
	time.Sleep(50 * time.Millisecond)

	if !beforeCalled {
		t.Error("BeforeRun hook not called")
	}
	if !afterCalled {
		t.Error("AfterRun hook not called")
	}
}
