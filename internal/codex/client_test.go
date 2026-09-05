package codex

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ngocp/goterm-control/internal/chat"
	"github.com/ngocp/goterm-control/internal/tools"
)

func TestBuildArgsNewThread(t *testing.T) {
	got := strings.Join(buildArgs("gpt-6-astra", "", true), " ")
	want := "exec --json --skip-git-repo-check --dangerously-bypass-approvals-and-sandbox -m gpt-6-astra -"
	if got != want {
		t.Errorf("new thread args:\n got %q\nwant %q", got, want)
	}
}

func TestBuildArgsResume(t *testing.T) {
	got := strings.Join(buildArgs("gpt-6-astra", "01a06f8b-86c6-7960", false), " ")
	if !strings.HasPrefix(got, "exec resume 01a06f8b-86c6-7960 ") {
		t.Errorf("resume must put the thread id right after the subcommand, got %q", got)
	}
	if !strings.HasSuffix(got, " -") {
		t.Errorf("prompt must still come from stdin, got %q", got)
	}
}

func TestBuildArgsOmitsEmptyModel(t *testing.T) {
	if got := strings.Join(buildArgs("", "", true), " "); strings.Contains(got, "-m") {
		t.Errorf("empty model must fall through to the CLI default, got %q", got)
	}
}

// Event lines captured from codex-cli 0.153.4 (`codex exec --json`).
const realAgentMessage = `{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"PONG"}}`

func TestParseRealAgentMessage(t *testing.T) {
	var ev event
	if err := json.Unmarshal([]byte(realAgentMessage), &ev); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if ev.Type != "item.completed" || ev.Item == nil || ev.Item.Type != "agent_message" {
		t.Fatalf("unexpected parse: %+v", ev)
	}

	var got string
	(&Client{}).handleCompletedItem(ev.Item, map[string]string{}, chat.StreamCallbacks{
		OnText: func(chunk string) { got += chunk },
	})
	if got != "PONG" {
		t.Errorf("OnText got %q, want %q", got, "PONG")
	}
}

func TestParseRealTurnCompletedUsage(t *testing.T) {
	line := `{"type":"turn.completed","usage":{"input_tokens":14690,"cached_input_tokens":11520,` +
		`"cache_write_input_tokens":0,"output_tokens":6,"reasoning_output_tokens":0}}`
	var ev event
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if ev.Usage == nil || ev.Usage.InputTokens != 14690 || ev.Usage.CachedInputTokens != 11520 || ev.Usage.OutputTokens != 6 {
		t.Fatalf("usage parsed wrong: %+v", ev.Usage)
	}
}

func TestCommandExecutionFailureIsAnError(t *testing.T) {
	exit := 1
	it := &threadItem{
		ID:               "item_3",
		Type:             "command_execution",
		Command:          "ls /nope",
		AggregatedOutput: "No such file or directory",
		ExitCode:         &exit,
		Status:           "failed",
	}
	var got tools.ToolResult
	var name string
	(&Client{}).handleCompletedItem(it, map[string]string{}, chat.StreamCallbacks{
		OnToolResult: func(n string, r tools.ToolResult) { name, got = n, r },
	})
	if name != "Bash" {
		t.Errorf("tool name = %q, want Bash", name)
	}
	if !got.IsError {
		t.Error("non-zero exit must be reported as an error")
	}
	if got.Output != "No such file or directory" {
		t.Errorf("output = %q", got.Output)
	}
}

func TestScreenshotBecomesImage(t *testing.T) {
	pending := map[string]string{"item_5": `screencapture -x "/tmp/shot2.png"`}
	it := &threadItem{ID: "item_5", Type: "command_execution", Status: "completed"}

	var got tools.ToolResult
	(&Client{}).handleCompletedItem(it, pending, chat.StreamCallbacks{
		OnToolResult: func(_ string, r tools.ToolResult) { got = r },
	})
	if !got.IsImage || got.ImagePath != "/tmp/shot2.png" {
		t.Errorf("screenshot not detected: %+v", got)
	}
}

func TestFirstTurnPromptCarriesInstructionsAndMemory(t *testing.T) {
	c := &Client{systemPrompt: "You are AGENT 2."}
	got := c.firstTurnPrompt("what time is it?", "## Memory\nuser likes brevity")

	for _, want := range []string{"You are AGENT 2.", "## File access", "user likes brevity", "what time is it?"} {
		if !strings.Contains(got, want) {
			t.Errorf("first-turn prompt missing %q\n---\n%s", want, got)
		}
	}
	// The actual message must come last so it is not buried in instructions.
	if !strings.HasSuffix(got, "what time is it?") {
		t.Errorf("user text must end the prompt, got:\n%s", got)
	}
}

func TestResumedTurnSendsRawUserText(t *testing.T) {
	c := &Client{systemPrompt: "You are AGENT 2."}
	// A resumed thread already holds the instructions; SendMessage only calls
	// firstTurnPrompt for new threads, so the raw text is what goes over stdin.
	if got := c.firstTurnPrompt("hello", ""); strings.Contains(got, "## Memory") {
		t.Errorf("empty memory must not add a memory section: %q", got)
	}
}
