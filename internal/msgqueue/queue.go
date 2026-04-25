package msgqueue

import (
	"strings"
	"time"
)

// Queue is the coordinator that wires Layer 1 (debouncer) and Layer 2 (collector).
// It provides a single Submit entry point for incoming user messages.
type Queue struct {
	debouncer *Debouncer
	collector *Collector
	execute   func(chatID int64, text string) // callback that runs the Claude pipeline
}

// New creates a message queue with the given debounce delay and execution callback.
// The execute callback is called with the combined message text when it's time
// to run the agent. It blocks until the agent finishes.
func New(debounceDelay time.Duration, execute func(chatID int64, text string)) *Queue {
	q := &Queue{
		collector: NewCollector(),
		execute:   execute,
	}

	q.debouncer = NewDebouncer(debounceDelay, func(chatID int64, texts []string) {
		combined := strings.Join(texts, "\n")
		q.tryExecute(chatID, combined)
	})

	return q
}

// Submit sends a message through the debouncer → collector → execute pipeline.
func (q *Queue) Submit(chatID int64, text string) {
	q.debouncer.Submit(chatID, text)
}

// SubmitImmediate bypasses the debouncer and sends directly to the collector.
// Use for commands or media that shouldn't wait for the debounce delay.
func (q *Queue) SubmitImmediate(chatID int64, text string) {
	q.debouncer.Flush(chatID) // flush any pending debounced messages first
	q.tryExecute(chatID, text)
}

// Cancel clears collected messages for chatID.
func (q *Queue) Cancel(chatID int64) {
	q.collector.Cancel(chatID)
}

// PendingCount returns how many messages are waiting in the collector for chatID.
func (q *Queue) PendingCount(chatID int64) int {
	return q.collector.PendingCount(chatID)
}

// Close stops the debouncer.
func (q *Queue) Close() {
	q.debouncer.Close()
}

// tryExecute attempts to run the agent. If busy, the collector holds the message.
// When the agent finishes, any collected followups are drained and re-executed.
func (q *Queue) tryExecute(chatID int64, text string) {
	if !q.collector.TryRun(chatID, text) {
		return // agent busy — message collected for later
	}

	go q.runAndDrain(chatID, text)
}

// runAndDrain executes the message and drains any followup collected during execution.
func (q *Queue) runAndDrain(chatID int64, text string) {
	q.execute(chatID, text)

	// Drain loop: if messages were collected while busy, execute them
	for {
		followup, ok := q.collector.Done(chatID)
		if !ok {
			return
		}
		q.execute(chatID, followup)
	}
}
