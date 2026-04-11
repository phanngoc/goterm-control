package msgqueue

import (
	"sync"
	"testing"
	"time"
)

func TestDebouncerBatchesRapidMessages(t *testing.T) {
	var mu sync.Mutex
	var flushed [][]string

	d := NewDebouncer(100*time.Millisecond, func(chatID int64, texts []string) {
		mu.Lock()
		flushed = append(flushed, texts)
		mu.Unlock()
	})
	defer d.Close()

	// Send 3 messages rapidly (within debounce window)
	d.Submit(1, "hello")
	d.Submit(1, "can you")
	d.Submit(1, "check my files")

	// Wait for debounce to fire
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(flushed) != 1 {
		t.Fatalf("expected 1 flush, got %d", len(flushed))
	}
	if len(flushed[0]) != 3 {
		t.Fatalf("expected 3 messages in batch, got %d", len(flushed[0]))
	}
}

func TestDebouncerSeparateBatches(t *testing.T) {
	var mu sync.Mutex
	var flushed [][]string

	d := NewDebouncer(50*time.Millisecond, func(chatID int64, texts []string) {
		mu.Lock()
		flushed = append(flushed, texts)
		mu.Unlock()
	})
	defer d.Close()

	d.Submit(1, "first")
	time.Sleep(100 * time.Millisecond) // wait for flush
	d.Submit(1, "second")
	time.Sleep(100 * time.Millisecond) // wait for flush

	mu.Lock()
	defer mu.Unlock()
	if len(flushed) != 2 {
		t.Fatalf("expected 2 separate flushes, got %d", len(flushed))
	}
}

func TestDebouncerDedup(t *testing.T) {
	var mu sync.Mutex
	var flushed [][]string

	d := NewDebouncer(50*time.Millisecond, func(chatID int64, texts []string) {
		mu.Lock()
		flushed = append(flushed, texts)
		mu.Unlock()
	})
	defer d.Close()

	d.Submit(1, "hello")
	d.Submit(1, "hello") // duplicate — should be skipped
	d.Submit(1, "world")

	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(flushed) != 1 {
		t.Fatalf("expected 1 flush, got %d", len(flushed))
	}
	if len(flushed[0]) != 2 {
		t.Fatalf("expected 2 messages (dedup), got %d: %v", len(flushed[0]), flushed[0])
	}
}

func TestDebouncerIsolatesChats(t *testing.T) {
	var mu sync.Mutex
	flushes := map[int64]int{}

	d := NewDebouncer(50*time.Millisecond, func(chatID int64, texts []string) {
		mu.Lock()
		flushes[chatID]++
		mu.Unlock()
	})
	defer d.Close()

	d.Submit(1, "msg for chat 1")
	d.Submit(2, "msg for chat 2")

	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if flushes[1] != 1 || flushes[2] != 1 {
		t.Fatalf("expected 1 flush per chat, got %v", flushes)
	}
}
