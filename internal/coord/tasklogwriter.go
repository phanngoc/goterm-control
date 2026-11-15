package coord

import (
	"log"
	"strings"
	"sync"
	"time"
)

// TaskLogWriter streams one turn's conversation into task_log as it happens:
// what the agent says, thinks and calls. Every place an agent works for
// someone writes through it — a task run, a room mention, a direct message,
// a Telegram turn — so the board reads one log the same way for all of them.
//
// Text arrives a few characters at a time. One row per chunk would be
// thousands of writes to a file three processes share, so text and thinking
// are buffered and written when the stream changes kind (a tool call), when a
// thinking block ends, every flushEvery while it keeps coming, and at Close.
//
// A room turn does not know yet whether it is work worth a task — it becomes
// one on its first tool call. Until Bind names the task, entries are kept in
// memory and written the moment it does, so the log starts at the beginning.
type TaskLogWriter struct {
	db      *DB
	runID   string
	agentID string

	mu       sync.Mutex
	taskID   string
	pending  []TaskLogEntry // before Bind
	text     strings.Builder
	thinking strings.Builder
	stop     chan struct{}
	stopOnce sync.Once
}

const taskLogFlushEvery = 2 * time.Second

// maxPendingLog bounds what an unbound writer keeps: a turn that never
// becomes a task should not hold its whole transcript in memory.
const maxPendingLog = 400

// NewTaskLogWriter starts a writer. taskID may be empty; see Bind.
func NewTaskLogWriter(db *DB, taskID, runID, agentID string) *TaskLogWriter {
	l := &TaskLogWriter{db: db, taskID: taskID, runID: runID, agentID: agentID, stop: make(chan struct{})}
	if db == nil {
		return l
	}
	go func() {
		t := time.NewTicker(taskLogFlushEvery)
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

// Bind names the task the log belongs to and writes what was kept so far.
// The first call wins.
func (l *TaskLogWriter) Bind(taskID, runID string) {
	if l == nil || taskID == "" {
		return
	}
	l.mu.Lock()
	if l.taskID != "" {
		l.mu.Unlock()
		return
	}
	l.taskID = taskID
	if runID != "" {
		l.runID = runID
	}
	pending := l.pending
	l.pending = nil
	l.mu.Unlock()
	for _, e := range pending {
		e.TaskID = taskID
		if e.RunID == "" {
			e.RunID = l.runID
		}
		l.put(e)
	}
}

func (l *TaskLogWriter) put(e TaskLogEntry) {
	if err := l.db.AppendTaskLog(e); err != nil {
		// The log is for watching; losing a line of it must never stop work.
		log.Printf("task log: %v", err)
	}
}

func (l *TaskLogWriter) write(kind, label, content string) {
	if l == nil || l.db == nil {
		return
	}
	l.mu.Lock()
	e := TaskLogEntry{TaskID: l.taskID, RunID: l.runID, AgentID: l.agentID,
		Kind: kind, Label: label, Content: content, CreatedAt: time.Now()}
	if l.taskID == "" {
		if len(l.pending) < maxPendingLog {
			l.pending = append(l.pending, e)
		}
		l.mu.Unlock()
		return
	}
	l.mu.Unlock()
	l.put(e)
}

// flush writes whatever text and thinking are buffered, thinking first: it
// came before the words it led to.
func (l *TaskLogWriter) flush() {
	if l == nil {
		return
	}
	l.mu.Lock()
	thinking, text := l.thinking.String(), l.text.String()
	l.thinking.Reset()
	l.text.Reset()
	l.mu.Unlock()
	if strings.TrimSpace(thinking) != "" {
		l.write(LogThinking, "", thinking)
	}
	if strings.TrimSpace(text) != "" {
		l.write(LogText, "", text)
	}
}

func (l *TaskLogWriter) Status(label, content string) { l.flush(); l.write(LogStatus, label, content) }
func (l *TaskLogWriter) Prompt(p string)              { l.write(LogPrompt, "", p) }
func (l *TaskLogWriter) User(by, text string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	agent := l.agentID
	l.agentID = by
	l.mu.Unlock()
	l.write(LogUser, "", text)
	l.mu.Lock()
	l.agentID = agent
	l.mu.Unlock()
}

func (l *TaskLogWriter) Text(chunk string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.text.WriteString(chunk)
	l.mu.Unlock()
}

// Thinking buffers reasoning; an empty chunk ends a block, which is a natural
// place to write it out.
func (l *TaskLogWriter) Thinking(chunk string) {
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

func (l *TaskLogWriter) Tool(label, input string) {
	l.flush()
	l.write(LogTool, label, input)
}

func (l *TaskLogWriter) ToolResult(name, output string, isErr bool) {
	label := name
	if isErr {
		label = "error"
	}
	l.flush()
	l.write(LogToolResult, label, clipResult(output))
}

// Close writes what is left and stops the ticker.
func (l *TaskLogWriter) Close() {
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
