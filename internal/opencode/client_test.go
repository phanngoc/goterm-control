package opencode

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ngocp/goterm-control/internal/chat"
	"github.com/ngocp/goterm-control/internal/tools"
)

func TestBuildArgsNewSession(t *testing.T) {
	got := strings.Join(buildArgs("anthropic/claude-opus-5", "", true), " ")
	want := "run --format json --model anthropic/claude-opus-5"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestBuildArgsResume(t *testing.T) {
	got := strings.Join(buildArgs("anthropic/claude-opus-5", "ses_abc", false), " ")
	if !strings.Contains(got, "--session ses_abc") {
		t.Errorf("resume did not pass the session id: %q", got)
	}
}

// A session id must never be passed on a new session: --session with an id the
// CLI has not created is a failure, not a fresh start.
func TestBuildArgsIgnoresTheIDWhenStartingFresh(t *testing.T) {
	got := strings.Join(buildArgs("m", "ses_stale", true), " ")
	if strings.Contains(got, "--session") {
		t.Errorf("a new session carried an old id: %q", got)
	}
}

func TestBuildArgsOmitsEmptyModel(t *testing.T) {
	got := strings.Join(buildArgs("", "", true), " ")
	if strings.Contains(got, "--model") {
		t.Errorf("empty model reached the CLI: %q", got)
	}
}

// The shapes below are the CLI's own, from
// packages/opencode/src/cli/cmd/run.ts: one JSON object per line, always
// {type, timestamp, sessionID, ...}.
const realTextEvent = `{"type":"text","timestamp":1757000000000,"sessionID":"ses_01","part":` +
	`{"id":"prt_1","sessionID":"ses_01","type":"text","text":"Đã xong.","time":{"start":1,"end":2}}}`

const realToolEvent = `{"type":"tool_use","timestamp":1757000000001,"sessionID":"ses_01","part":` +
	`{"id":"prt_2","type":"tool","tool":"bash","state":{"status":"completed",` +
	`"title":"ls","input":{"command":"ls -la"},"output":"total 0"}}}`

const realToolError = `{"type":"tool_use","timestamp":1757000000002,"sessionID":"ses_01","part":` +
	`{"id":"prt_3","type":"tool","tool":"read","state":{"status":"error","error":"file not found"}}}`

func TestParseRealTextEvent(t *testing.T) {
	var ev event
	if err := json.Unmarshal([]byte(realTextEvent), &ev); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if ev.Type != "text" || ev.Part == nil || ev.Part.Text != "Đã xong." {
		t.Fatalf("unexpected parse: %+v", ev)
	}
	// Every event carries it, which is why resuming needs no separate lookup.
	if ev.SessionID != "ses_01" {
		t.Errorf("session id lost: %q", ev.SessionID)
	}
}

func TestParseRealToolEvent(t *testing.T) {
	var ev event
	if err := json.Unmarshal([]byte(realToolEvent), &ev); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if ev.Part.Tool != "bash" || ev.Part.State.Status != "completed" {
		t.Fatalf("unexpected parse: %+v", ev.Part)
	}
	if in := ev.Part.State.inputJSON(); !strings.Contains(in, "ls -la") {
		t.Errorf("tool input lost: %q", in)
	}
	if got := toolResult(ev.Part.State); got.IsError || got.Output != "total 0" {
		t.Errorf("tool result: %+v", got)
	}
}

func TestToolErrorIsAnError(t *testing.T) {
	var ev event
	if err := json.Unmarshal([]byte(realToolError), &ev); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got := toolResult(ev.Part.State)
	if !got.IsError || !strings.Contains(got.Output, "file not found") {
		t.Errorf("a failed tool came back as success: %+v", got)
	}
}

// A call with no arguments must not look like a parse failure downstream.
func TestEmptyToolInputIsAnEmptyObject(t *testing.T) {
	if got := (&toolState{}).inputJSON(); got != "{}" {
		t.Errorf("got %q, want %q", got, "{}")
	}
}

func TestFirstTurnPromptCarriesInstructionsAndMemory(t *testing.T) {
	c := New("You are terse.")
	got := c.firstTurnPrompt("what time is it", "The user is in Hanoi.")
	for _, want := range []string{"You are terse.", "The user is in Hanoi.", "what time is it"} {
		if !strings.Contains(got, want) {
			t.Errorf("first-turn prompt is missing %q:\n%s", want, got)
		}
	}
	// And a resumed session must not get them again.
	if again := c.firstTurnPrompt("next", ""); strings.Contains(again, "Hanoi") {
		t.Error("memory leaked into a prompt that was not given any")
	}
}

func TestErrorTextPrefersTheMessage(t *testing.T) {
	if got := errorText(json.RawMessage(`{"name":"ProviderError","message":"rate limited"}`), ""); got != "rate limited" {
		t.Errorf("got %q", got)
	}
	if got := errorText(nil, "auth failed"); got != "auth failed" {
		t.Errorf("fallback to stderr: got %q", got)
	}
	if got := errorText(nil, ""); got == "" {
		t.Error("an error with nothing in it still has to say something")
	}
}

func TestSessionNotFoundIsRecognised(t *testing.T) {
	for _, line := range []string{"Error: session not found", "no such session: ses_9"} {
		if !sessionNotFound(line) {
			t.Errorf("not recognised: %q", line)
		}
	}
	if sessionNotFound("model provider returned 500") {
		t.Error("an unrelated failure was read as a missing session")
	}
}

// The client has to keep satisfying the interface the whole gateway is built
// on; a signature drift would otherwise only show up at the registry.
func TestClientSatisfiesChatClient(t *testing.T) {
	var _ chat.Client = New("")
	var _ tools.ToolResult = toolResult(&toolState{})
}
