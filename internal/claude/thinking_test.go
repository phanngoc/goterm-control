package claude

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ngocp/goterm-control/internal/chat"
	"github.com/ngocp/goterm-control/internal/session"
)

// The thinking a person watches comes from stream_event thinking deltas,
// which only carry text when the CLI is asked for summaries. Both halves are
// checked: the flag is passed, and the deltas reach OnThinking with a block
// boundary after each block.
func TestThinkingDeltasReachTheCallback(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture requires POSIX")
	}
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	script := "#!/bin/sh\necho \"$@\" > " + argsFile + "\n" +
		`echo '{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"thinking"}}}'` + "\n" +
		`echo '{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Checking the "}}}'` + "\n" +
		`echo '{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"cache key."}}}'` + "\n" +
		`echo '{"type":"stream_event","event":{"type":"content_block_stop","index":0}}'` + "\n" +
		`echo '{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"done"}]}}'` + "\n" +
		`echo '{"type":"result","is_error":false,"result":"done"}'` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("BOMCLAW_THINKING_DISPLAY", "")

	var chunks []string
	var text strings.Builder
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	err := New("", nil).SendMessage(ctx, session.New(1), "test", "hi", "", chat.StreamCallbacks{
		OnText:     func(s string) { text.WriteString(s) },
		OnThinking: func(s string) { chunks = append(chunks, s) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"Checking the ", "cache key.", ""}; !slices.Equal(chunks, want) {
		t.Errorf("thinking chunks = %q, want %q", chunks, want)
	}
	if text.String() != "done" {
		t.Errorf("text = %q", text.String())
	}
	args, _ := os.ReadFile(argsFile)
	if !strings.Contains(string(args), "--thinking-display summarized") {
		t.Errorf("CLI not asked for thinking summaries: %s", args)
	}
}

func TestThinkingDisplayCanBeTurnedOff(t *testing.T) {
	t.Setenv("BOMCLAW_THINKING_DISPLAY", "off")
	if slices.Contains(buildArgs("m", "", true, ""), "--thinking-display") {
		t.Error("off still passes the flag")
	}
}
