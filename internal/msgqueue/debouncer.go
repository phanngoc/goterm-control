package msgqueue

import (
	"sync"
	"time"
)

// Debouncer batches rapid messages per key (chatID).
// It waits for `delay` of silence before flushing accumulated texts.
type Debouncer struct {
	mu      sync.Mutex
	pending map[int64]*pendingBatch
	delay   time.Duration
	onFlush func(chatID int64, texts []string)
}

type pendingBatch struct {
	texts []string
	timer *time.Timer
}

// NewDebouncer creates a debouncer that calls onFlush after delay of silence.
func NewDebouncer(delay time.Duration, onFlush func(chatID int64, texts []string)) *Debouncer {
	return &Debouncer{
		pending: make(map[int64]*pendingBatch),
		delay:   delay,
		onFlush: onFlush,
	}
}

// Submit adds a message to the debounce buffer for chatID.
// Resets the timer — flush fires only after `delay` of silence.
func (d *Debouncer) Submit(chatID int64, text string) {
	d.mu.Lock()
	defer d.mu.Unlock()

	pb, ok := d.pending[chatID]
	if !ok {
		pb = &pendingBatch{}
		d.pending[chatID] = pb
	}

	// Simple dedup: skip if identical to last message in batch
	if len(pb.texts) > 0 && pb.texts[len(pb.texts)-1] == text {
		return
	}

	pb.texts = append(pb.texts, text)

	// Reset timer
	if pb.timer != nil {
		pb.timer.Stop()
	}
	pb.timer = time.AfterFunc(d.delay, func() {
		d.flush(chatID)
	})
}

// Flush immediately flushes pending messages for chatID (bypass debounce).
func (d *Debouncer) Flush(chatID int64) {
	d.flush(chatID)
}

func (d *Debouncer) flush(chatID int64) {
	d.mu.Lock()
	pb, ok := d.pending[chatID]
	if !ok || len(pb.texts) == 0 {
		d.mu.Unlock()
		return
	}
	texts := pb.texts
	if pb.timer != nil {
		pb.timer.Stop()
	}
	delete(d.pending, chatID)
	d.mu.Unlock()

	d.onFlush(chatID, texts)
}

// Close stops all pending timers.
func (d *Debouncer) Close() {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, pb := range d.pending {
		if pb.timer != nil {
			pb.timer.Stop()
		}
	}
	d.pending = make(map[int64]*pendingBatch)
}
