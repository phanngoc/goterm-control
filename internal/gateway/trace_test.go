package gateway

import (
	"context"
	"encoding/json"
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
