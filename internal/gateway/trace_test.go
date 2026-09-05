package gateway

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ngocp/goterm-control/internal/agent"
	"github.com/ngocp/goterm-control/internal/coord"
	"github.com/ngocp/goterm-control/internal/models"
	"github.com/ngocp/goterm-control/internal/session"
	"github.com/ngocp/goterm-control/internal/storage"
	"github.com/ngocp/goterm-control/internal/trace"
)

// stubProvider answers with one tool call, then a final message — enough to
// exercise every span kind the handler is supposed to open.
type stubProvider struct{ calls int }

func (p *stubProvider) Stream(ctx context.Context, params agent.StreamParams) (<-chan agent.StreamEvent, error) {
	p.calls++
	ch := make(chan agent.StreamEvent, 4)
	if p.calls == 1 {
		ch <- agent.StreamEvent{
			Type: "tool_use", ToolID: "t1", ToolName: "run_command",
			ToolInput: json.RawMessage(`{"command":"echo hi"}`),
		}
		ch <- agent.StreamEvent{Type: "end", StopReason: "tool_use", Usage: &agent.Usage{InputTokens: 100, OutputTokens: 5}}
	} else {
		ch <- agent.StreamEvent{Type: "text", Text: "done"}
		ch <- agent.StreamEvent{Type: "end", StopReason: "end_turn", Usage: &agent.Usage{InputTokens: 120, OutputTokens: 3}}
	}
	close(ch)
	return ch, nil
}

type stubExecutor struct{}

func (stubExecutor) Execute(ctx context.Context, name string, input json.RawMessage) agent.ToolResult {
	return agent.ToolResult{Content: "hi", IsError: false}
}

// TestStreamingSendIsTraced covers a gap that shipped once: only the Telegram
// path was traced, while the dashboard chat box, `bomclaw send` and peer agents
// all go through the STREAMING send handler and left no trace at all.
func TestStreamingSendIsTraced(t *testing.T) {
	dir := t.TempDir()

	cdb, err := coord.Open(filepath.Join(dir, "coord.db"))
	if err != nil {
		t.Fatalf("coord: %v", err)
	}
	defer cdb.Close()

	sdb, err := storage.Open(filepath.Join(dir, "goterm.db"))
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	defer sdb.Close()

	rec := trace.New(cdb, "a1")
	deps := Deps{
		Sessions:     session.NewManager(storage.NewSessionStore(sdb)),
		Resolver:     models.NewResolver("claude-opus-4-8", nil),
		Provider:     &stubProvider{},
		ToolExecutor: stubExecutor{},
		DataDir:      dir,
		System:       "you are a test",
		Coord:        cdb,
		AgentID:      "a1",
		ProviderName: "claude",
		Trace:        rec,
	}

	params, _ := json.Marshal(SendParams{Message: "run echo", SessionID: "chat_1"})
	NewStreamSendHandler(deps)(context.Background(),
		Request{ID: "1", Method: "send", Params: params},
		func(StreamEvent) {})

	rec.Close() // drain the async writer before asserting

	traces, err := cdb.ListTraces(coord.TraceFilter{})
	if err != nil {
		t.Fatalf("list traces: %v", err)
	}
	if len(traces) != 1 {
		t.Fatalf("got %d traces, want exactly 1 for one command", len(traces))
	}

	root := traces[0]
	if root.Name != "gateway.send" {
		t.Errorf("root run = %q, want gateway.send", root.Name)
	}
	if root.Inputs != "run echo" {
		t.Errorf("root inputs = %q, want the user's message", root.Inputs)
	}
	if root.Status != coord.StatusSuccess {
		t.Errorf("root status = %q", root.Status)
	}

	runs, err := cdb.GetTrace(root.TraceID)
	if err != nil {
		t.Fatalf("get trace: %v", err)
	}

	var llm, tool int
	for _, r := range runs {
		switch r.RunType {
		case coord.RunTypeLLM:
			llm++
		case coord.RunTypeTool:
			tool++
			if r.Depth != 2 {
				t.Errorf("tool %q depth = %d, want 2 (root → llm → tool)", r.Name, r.Depth)
			}
		}
	}
	if llm != 2 {
		t.Errorf("got %d llm spans, want 2 — the loop makes one model call per tool round trip", llm)
	}
	if tool != 1 {
		t.Errorf("got %d tool spans, want 1", tool)
	}
	if root.TotalTokens != 228 {
		t.Errorf("rolled-up tokens = %d, want 228 (100+5+120+3)", root.TotalTokens)
	}
}

// TestStreamingSendRecordsTheSession covers a gap the dashboard made visible:
// the streaming handler wrote transcripts but never touched a session, so the
// dashboard's own conversation existed only as a file. sessions.list then
// synthesised a stub for it that always read "0 turns · 0 tokens".
func TestStreamingSendRecordsTheSession(t *testing.T) {
	dir := t.TempDir()

	sdb, err := storage.Open(filepath.Join(dir, "goterm.db"))
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	defer sdb.Close()

	sessions := session.NewManager(storage.NewSessionStore(sdb))
	deps := Deps{
		Sessions:     sessions,
		Resolver:     models.NewResolver("claude-opus-4-8", nil),
		Provider:     &stubProvider{},
		ToolExecutor: stubExecutor{},
		DataDir:      dir,
		System:       "you are a test",
		AgentID:      "a1",
		ProviderName: "claude",
	}

	params, _ := json.Marshal(SendParams{Message: "summarise yesterday's deploy"})
	NewStreamSendHandler(deps)(context.Background(),
		Request{ID: "1", Method: "send", Params: params},
		func(StreamEvent) {})

	sess := sessions.Get(dashboardChatID)
	if sess == nil {
		t.Fatal("the dashboard conversation did not become a real session")
	}
	if got := sess.GetMessageCount(); got != 1 {
		t.Errorf("message count = %d, want 1 — the sessions list shows this as turns", got)
	}
	in, out := sess.Tokens()
	if in != 220 || out != 8 {
		t.Errorf("tokens = %d/%d, want 220/8 summed across both model calls", in, out)
	}
	if label := sess.GetLabel(); label != "summarise yesterday's deploy" {
		t.Errorf("label = %q, want the opening message", label)
	}
}

// A session id that resolves to nothing must not be redirected into the default
// conversation: that would file the user's message under the wrong transcript.
func TestUnknownSessionIDIsNotRedirected(t *testing.T) {
	dir := t.TempDir()

	sdb, err := storage.Open(filepath.Join(dir, "goterm.db"))
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	defer sdb.Close()

	sessions := session.NewManager(storage.NewSessionStore(sdb))
	deps := Deps{
		Sessions:     sessions,
		Resolver:     models.NewResolver("claude-opus-4-8", nil),
		Provider:     &stubProvider{},
		ToolExecutor: stubExecutor{},
		DataDir:      dir,
		AgentID:      "a1",
	}

	params, _ := json.Marshal(SendParams{Message: "hello", SessionID: "chat_does_not_exist"})
	NewStreamSendHandler(deps)(context.Background(),
		Request{ID: "1", Method: "send", Params: params},
		func(StreamEvent) {})

	// The transcript must carry the id the caller asked for.
	if _, err := os.Stat(filepath.Join(dir, "transcripts", "chat_does_not_exist.jsonl")); err != nil {
		t.Errorf("message was not written to the requested transcript: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "transcripts", "chat_1.jsonl")); err == nil {
		t.Error("message leaked into the default dashboard conversation")
	}
}
