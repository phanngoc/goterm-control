package taskrunner

import (
	"log"
	"strings"
	"sync"
	"time"

	"github.com/ngocp/goterm-control/internal/coord"
)

// sessionLog writes a run's conversation into task_log as it streams, so the
// dashboard can show it while it happens and after, for every agent alike.
//
// Text arrives a few characters at a time. One row per chunk would be
// thousands of writes to a file three processes share, so text and thinking
// are buffered and written when the stream changes kind (a tool call), when a
// thinking block ends, every flushEvery while it keeps coming, and at the end.
type sessionLog struct {
	db      *coord.DB
	taskID  string
	runID   string
	agentID string

	mu       sync.Mutex
	text     strings.Builder
	thinking strings.Builder
	stop     chan struct{}
	stopOnce sync.Once
}

const flushEvery = 2 * time.Second

func newSessionLog(db *coord.DB, taskID, runID, agentID string) *sessionLog {
	l := &sessionLog{db: db, taskID: taskID, runID: runID, agentID: agentID, stop: make(chan struct{})}
	if db == nil {
		return l
	}
	go func() {
		t := time.NewTicker(flushEvery)
		defer t.Stop()
		for {
			select {
			case <-l.stop:
				return
			case <-t.C:
				l.flush()
			}
		}
	}()
	return l
}

func (l *sessionLog) write(kind, label, content string) {
	if l == nil || l.db == nil {
		return
	}
	if err := withRetry("task log "+l.taskID, func() error {
		return l.db.AppendTaskLog(coord.TaskLogEntry{
			TaskID: l.taskID, RunID: l.runID, AgentID: l.agentID,
			Kind: kind, Label: label, Content: content,
		})
	}); err != nil {
		// The log is for watching; losing a line of it must never stop work.
		log.Printf("taskrunner: %v", err)
	}
}

// flush writes whatever text and thinking are buffered, thinking first: it
// came before the words it led to.
func (l *sessionLog) flush() {
	if l == nil {
		return
	}
	l.mu.Lock()
	thinking, text := l.thinking.String(), l.text.String()
	l.thinking.Reset()
	l.text.Reset()
	l.mu.Unlock()
	if strings.TrimSpace(thinking) != "" {
		l.write(coord.LogThinking, "", thinking)
	}
	if strings.TrimSpace(text) != "" {
		l.write(coord.LogText, "", text)
	}
}

func (l *sessionLog) Status(label, content string) { l.write(coord.LogStatus, label, content) }
func (l *sessionLog) Prompt(p string)              { l.write(coord.LogPrompt, "", p) }

func (l *sessionLog) Text(chunk string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.text.WriteString(chunk)
	l.mu.Unlock()
}

// Thinking buffers reasoning; an empty chunk ends a block, which is a natural
// place to write it out.
func (l *sessionLog) Thinking(chunk string) {
	if l == nil {
		return
	}
	if chunk == "" {
		l.flush()
		return
	}
	l.mu.Lock()
	l.thinking.WriteString(chunk)
	l.mu.Unlock()
}

func (l *sessionLog) Tool(label, input string) {
	l.flush()
	l.write(coord.LogTool, label, input)
}

func (l *sessionLog) ToolResult(name, output string, isErr bool) {
	label := name
	if isErr {
		label = "error"
	}
	l.flush()
	l.write(coord.LogToolResult, label, clipResult(output))
}

// Close writes what is left and stops the ticker.
func (l *sessionLog) Close() {
	if l == nil {
		return
	}
	l.stopOnce.Do(func() { close(l.stop) })
	l.flush()
}

// clipResult keeps tool output readable on a board: the head is what says
// what happened.
func clipResult(s string) string {
	const max = 4000
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && (s[cut]&0xC0) == 0x80 {
		cut--
	}
	return s[:cut] + "\n…"
}
