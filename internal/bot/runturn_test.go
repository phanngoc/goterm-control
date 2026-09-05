package bot

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ngocp/goterm-control/internal/agent"
	"github.com/ngocp/goterm-control/internal/chat"
	"github.com/ngocp/goterm-control/internal/config"
	"github.com/ngocp/goterm-control/internal/coord"
	"github.com/ngocp/goterm-control/internal/execution"
	"github.com/ngocp/goterm-control/internal/memory"
	"github.com/ngocp/goterm-control/internal/session"
	"github.com/ngocp/goterm-control/internal/storage"
	"github.com/ngocp/goterm-control/internal/tools"
	"github.com/ngocp/goterm-control/internal/trace"
	"github.com/ngocp/goterm-control/internal/transcript"
)

// scriptedClient plays one turn the way the real CLI clients do: text, a tool
// call with its result, more text, and — like claude/codex — it is the client
// that stamps the session id and bumps the session counters.
type scriptedClient struct {
	mu    sync.Mutex
	turns int
}

func (c *scriptedClient) Name() string { return "stub" }

func (c *scriptedClient) SendMessage(ctx context.Context, sess *session.Session, modelID,
	userText, memoryContext string, cb chat.StreamCallbacks) error {
	c.mu.Lock()
	c.turns++
	c.mu.Unlock()

	cb.OnText("Hello ")
	cb.OnToolCall("Bash", `{"command":"ls"}`)
	cb.OnToolResult("Bash", tools.ToolResult{Output: "a b"})
	cb.OnText("done")

	if sess.GetSessionID() == "" {
		sess.SetSessionID("stub-session-1")
		sess.SetProvider("stub")
	}
	sess.AddTokens(100, 7)
	sess.IncrementMessages()
	return nil
}

// recordingSink is a TurnSink that keeps what it was handed — the dashboard's
// stand-in, so the test can see the turn from the channel's side.
type recordingSink struct {
	mu     sync.Mutex
	text   strings.Builder
	tools  []string
	photos []string
	done   bool
}

func (s *recordingSink) Write(chunk string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.text.WriteString(chunk)
}
func (s *recordingSink) NoteTool(label string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tools = append(s.tools, label)
}
func (s *recordingSink) Flush() {}
func (s *recordingSink) SendPhoto(path, caption string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.photos = append(s.photos, path)
}
func (s *recordingSink) Finalize() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.done = true
}

// TestRunTurnIsTheTelegramPath is the S0 acceptance test: a message handed to
// RunTurn by a non-Telegram caller must leave exactly the footprint a Telegram
// message leaves — rows in the messages table, events in the transcript, the
// session counted, and a trace with tool spans. Every one of these was missing
// on the dashboard before it shared the engine.
func TestRunTurnIsTheTelegramPath(t *testing.T) {
	dir := t.TempDir()

	sdb, err := storage.Open(filepath.Join(dir, "goterm.db"))
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	defer sdb.Close()

	cdb, err := coord.Open(filepath.Join(dir, "coord.db"))
	if err != nil {
		t.Fatalf("coord: %v", err)
	}
	defer cdb.Close()

	rec := trace.New(cdb, "a1")
	sessions := session.NewManager(storage.NewSessionStore(sdb))

	h := &Handler{
		sessions:   sessions,
		llm:        &scriptedClient{},
		cfg:        &config.Config{},
		engine:     execution.NewEngine(execution.Hooks{}, 3),
		transcript: transcript.NewWriter(filepath.Join(dir, "transcripts")),
		messages:   storage.NewMessageStore(sdb),
		memory:     memory.NewManager(memory.Config{}), // disabled: no files to read
		trace:      rec,
		agentID:    "a1",
	}

	sess := sessions.Get(1) // the dashboard's chat
	sink := &recordingSink{}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, err := h.RunTurn(ctx, sess, 1, "test-model", "list the files", sink)
	if err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	if result == nil || result.Status != execution.RunSuccess {
		t.Fatalf("result = %+v, want success", result)
	}
	rec.Close() // drain the async trace writer before asserting on it

	// --- the channel saw the turn -------------------------------------------
	if got := sink.text.String(); got != "Hello done" {
		t.Errorf("sink text = %q, want %q", got, "Hello done")
	}
	if len(sink.tools) != 1 || !strings.HasPrefix(sink.tools[0], "Bash") {
		t.Errorf("sink tools = %v, want one Bash label", sink.tools)
	}

	// --- the messages table has both sides (the dashboard never wrote it) ---
	history, err := h.messages.LoadHistory(sess.ID, 10)
	if err != nil {
		t.Fatalf("load history: %v", err)
	}
	if len(history) != 2 || history[0].Role != "user" || history[1].Role != "assistant" {
		t.Fatalf("messages table = %+v, want user then assistant", history)
	}
	if history[1].Content != "Hello done" {
		t.Errorf("assistant row = %q", history[1].Content)
	}

	// --- the transcript carries the tool call, not just the text -------------
	raw, err := os.ReadFile(filepath.Join(dir, "transcripts", sess.ID+".jsonl"))
	if err != nil {
		t.Fatalf("transcript: %v", err)
	}
	for _, want := range []string{`"user_message"`, `"assistant_text"`, `"tool_call"`, `"tool_result"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("transcript is missing a %s event", want)
		}
	}

	// --- the session was counted and labelled --------------------------------
	if sess.GetMessageCount() != 1 {
		t.Errorf("message count = %d, want 1", sess.GetMessageCount())
	}
	if in, out := sess.Tokens(); in != 100 || out != 7 {
		t.Errorf("tokens = %d/%d, want 100/7", in, out)
	}
	if sess.GetLabel() != "list the files" {
		t.Errorf("label = %q, want the opening message", sess.GetLabel())
	}
	if sess.GetSessionID() != "stub-session-1" {
		t.Errorf("provider session id = %q — the CLI's id must be kept for resume", sess.GetSessionID())
	}

	// --- the trace has the Telegram shape: turn → llm → tool -----------------
	traces, err := cdb.ListTraces(coord.TraceFilter{})
	if err != nil {
		t.Fatalf("list traces: %v", err)
	}
	if len(traces) != 1 || traces[0].Name != "turn" {
		t.Fatalf("traces = %+v, want exactly one root named turn", traces)
	}
	runs, _ := cdb.GetTrace(traces[0].TraceID)
	var llm, tool int
	for _, r := range runs {
		switch r.RunType {
		case coord.RunTypeLLM:
			llm++
		case coord.RunTypeTool:
			tool++
			if r.Depth != 2 {
				t.Errorf("tool span depth = %d, want 2 (turn → llm → tool)", r.Depth)
			}
			if r.Name != "Bash" {
				t.Errorf("tool span name = %q, want Bash", r.Name)
			}
		}
	}
	if llm != 1 || tool != 1 {
		t.Errorf("spans: llm=%d tool=%d, want 1 and 1", llm, tool)
	}
	if traces[0].TotalTokens != 107 {
		t.Errorf("rolled-up tokens = %d, want 107 (counted once, on the llm span)", traces[0].TotalTokens)
	}
}

// TestNewSessionContextOnlyForFreshSessions pins the rule both channels now
// share: memory and history are injected into a session's first turn only. A
// resumed session already carries its history in the CLI; injecting again
// pollutes the context (an old topic overriding the user's current intent).
func TestNewSessionContextOnlyForFreshSessions(t *testing.T) {
	dir := t.TempDir()
	sdb, err := storage.Open(filepath.Join(dir, "goterm.db"))
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	defer sdb.Close()

	store := storage.NewMessageStore(sdb)
	sessions := session.NewManager(storage.NewSessionStore(sdb))
	h := &Handler{
		sessions: sessions,
		messages: store,
		memory:   memory.NewManager(memory.Config{}),
	}

	// Through the manager, not session.New: messages reference sessions by
	// foreign key, so a session must exist in the store before it can have any.
	sess := sessions.Get(7)
	if err := store.Append(sess.ID, agent.Message{Role: "user", Content: "earlier question"}); err != nil {
		t.Fatalf("seed history: %v", err)
	}

	if got := h.NewSessionContext(sess); !strings.Contains(got, "earlier question") {
		t.Errorf("fresh session must receive recent history, got %q", got)
	}

	sess.SetSessionID("already-known-to-the-cli")
	if got := h.NewSessionContext(sess); got != "" {
		t.Errorf("resumed session must receive nothing, got %q", got)
	}
}
