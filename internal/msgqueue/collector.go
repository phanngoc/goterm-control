package msgqueue

import (
	"fmt"
	"strings"
	"sync"
)

// Collector implements "collect" mode: when the agent is busy processing,
// incoming messages are accumulated and drained as a single batch when
// the agent finishes its current turn.
type Collector struct {
	mu    sync.Mutex
	state map[int64]*chatState
}

type chatState struct {
	busy      bool
	collected []string
}

// NewCollector creates a new collector.
func NewCollector() *Collector {
	return &Collector{
		state: make(map[int64]*chatState),
	}
}

// TryRun attempts to start execution for chatID.
// Returns true if the agent is free (caller should execute).
// Returns false if the agent is busy (message is collected for later).
func (c *Collector) TryRun(chatID int64, text string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	cs, ok := c.state[chatID]
	if !ok {
		cs = &chatState{}
		c.state[chatID] = cs
	}

	if cs.busy {
		cs.collected = append(cs.collected, text)
		return false
	}

	cs.busy = true
	return true
}

// Done marks the agent as no longer busy for chatID.
// If messages were collected while busy, returns them as a combined followup
// and marks busy again (caller should re-execute with the followup).
func (c *Collector) Done(chatID int64) (followup string, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	cs, exists := c.state[chatID]
	if !exists {
		return "", false
	}

	if len(cs.collected) == 0 {
		cs.busy = false
		return "", false
	}

	// Drain collected into a single followup prompt
	followup = formatFollowup(cs.collected)
	cs.collected = nil
	// Stay busy — caller will re-execute with followup
	return followup, true
}

// Cancel clears collected messages for chatID.
func (c *Collector) Cancel(chatID int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if cs, ok := c.state[chatID]; ok {
		cs.collected = nil
	}
}

// formatFollowup combines collected messages into a single prompt.
func formatFollowup(messages []string) string {
	if len(messages) == 1 {
		return messages[0]
	}

	var sb strings.Builder
	sb.WriteString("[Queued messages while agent was busy]\n")
	for i, msg := range messages {
		sb.WriteString(fmt.Sprintf("\nMessage %d:\n%s\n", i+1, msg))
	}
	return sb.String()
}
