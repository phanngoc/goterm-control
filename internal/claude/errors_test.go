package claude

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ngocp/goterm-control/internal/chat"
	"github.com/ngocp/goterm-control/internal/session"
)

func TestCLIErrorClassification(t *testing.T) {
	for _, tc := range []struct {
		name, id, result, stderr string
		details                  []string
		missing                  bool
	}{
		{name: "stderr", id: "gone", stderr: "No conversation found with session ID: gone\n", missing: true},
		{name: "errors array", id: "gone", details: []string{"No conversation found with session ID: gone"}, missing: true},
		{name: "wrong session", id: "kept", stderr: "No conversation found with session ID: gone"},
		{name: "new session", stderr: "No conversation found with session ID: gone"},
		{name: "rate limit", id: "kept", details: []string{"Rate limit exceeded"}},
		{name: "empty diagnostics"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := cliError(tc.id, tc.result, tc.details, tc.stderr)
			if errors.Is(err, chat.ErrSessionNotFound) != tc.missing {
				t.Fatalf("wrong classification: %v", err)
			}
			if err.Error() == "claude error: " {
				t.Fatal("empty diagnostic")
			}
		})
	}
}

func TestSendMessagePreservesTerminalErrorAndReapsCLI(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture requires POSIX")
	}
	for _, resultEvent := range []bool{true, false} {
		t.Run(map[bool]string{true: "result event", false: "exit only"}[resultEvent], func(t *testing.T) {
			dir := t.TempDir()
			script := "#!/bin/sh\necho 'No conversation found with session ID: gone' >&2\n"
			if resultEvent {
				script += "echo '{\"type\":\"result\",\"is_error\":true,\"errors\":[\"resume failed\"]}'\nexec sleep 60\n"
			} else {
				script += "exit 1\n"
			}
			if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			sess := session.New(1)
			sess.SetSessionID("gone")
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()

			err := New("", nil).SendMessage(ctx, sess, "test", "test", "", chat.StreamCallbacks{})
			if !errors.Is(err, chat.ErrSessionNotFound) {
				t.Fatalf("error = %v", err)
			}
			if resultEvent && !strings.Contains(err.Error(), "resume failed") {
				t.Fatalf("lost errors array: %v", err)
			}
			if ctx.Err() != nil {
				t.Fatal("terminal error did not stop child promptly")
			}
		})
	}
}
