package codex

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ngocp/goterm-control/internal/chat"
	"github.com/ngocp/goterm-control/internal/session"
)

// TestLiveSendMessageAndResume drives the real codex CLI. It needs `codex login`
// and burns subscription quota, so it only runs with CODEX_LIVE=1.
//
//	CODEX_LIVE=1 go test ./internal/codex/ -run Live -v
func TestLiveSendMessageAndResume(t *testing.T) {
	if os.Getenv("CODEX_LIVE") != "1" {
		t.Skip("set CODEX_LIVE=1 to run against the real codex CLI")
	}

	c := New("You are a test fixture. Answer in as few words as possible.")
	c.SetWorkspace(t.TempDir())

	sess := session.New(999)
	model := os.Getenv("CODEX_LIVE_MODEL") // empty → CLI default

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// --- turn 1: new thread -------------------------------------------------
	var first strings.Builder
	err := c.SendMessage(ctx, sess, model,
		"Remember the number 4271. Reply with exactly: STORED", "", chat.StreamCallbacks{
			OnText: func(chunk string) { first.WriteString(chunk) },
		})
	if err != nil {
		t.Fatalf("turn 1: %v", err)
	}
	if !strings.Contains(first.String(), "STORED") {
		t.Errorf("turn 1 reply = %q, want it to contain STORED", first.String())
	}

	threadID := sess.GetSessionID()
	if threadID == "" {
		t.Fatal("turn 1 did not record a thread id — resume would start over every turn")
	}
	if got := sess.GetProvider(); got != ProviderName {
		t.Errorf("session provider = %q, want %q", got, ProviderName)
	}
	if sess.GetMessageCount() != 1 {
		t.Errorf("message count = %d, want 1", sess.GetMessageCount())
	}
	if sess.LastContextTokens() == 0 {
		t.Error("turn.completed usage was not recorded — the memory flush threshold would never fire")
	}

	// --- turn 2: resume the same thread -------------------------------------
	var second strings.Builder
	err = c.SendMessage(ctx, sess, model,
		"What number did I ask you to remember? Reply with just the number.", "", chat.StreamCallbacks{
			OnText: func(chunk string) { second.WriteString(chunk) },
		})
	if err != nil {
		t.Fatalf("turn 2 (resume): %v", err)
	}
	if !strings.Contains(second.String(), "4271") {
		t.Errorf("resume lost the thread history: reply = %q, want it to contain 4271", second.String())
	}
	if sess.GetSessionID() != threadID {
		t.Errorf("thread id changed on resume: %q → %q", threadID, sess.GetSessionID())
	}
	if sess.GetMessageCount() != 2 {
		t.Errorf("message count = %d, want 2", sess.GetMessageCount())
	}
}

// TestLiveProviderSwitchStartsFreshThread proves a session carrying another
// CLI's id is not handed to `codex exec resume`, which would fail every turn.
func TestLiveProviderSwitchStartsFreshThread(t *testing.T) {
	if os.Getenv("CODEX_LIVE") != "1" {
		t.Skip("set CODEX_LIVE=1 to run against the real codex CLI")
	}

	c := New("You are a test fixture.")
	c.SetWorkspace(t.TempDir())

	// A session left behind by the claude backend.
	sess := session.New(998)
	sess.SetSessionID("03a07538-d859-4daf-9f9a-f2f5121600d9")
	sess.SetProvider("claude")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	var out strings.Builder
	if err := c.SendMessage(ctx, sess, os.Getenv("CODEX_LIVE_MODEL"),
		"Reply with exactly: FRESH", "", chat.StreamCallbacks{
			OnText: func(chunk string) { out.WriteString(chunk) },
		}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if !strings.Contains(out.String(), "FRESH") {
		t.Errorf("reply = %q", out.String())
	}
	if sess.GetSessionID() == "03a07538-d859-4daf-9f9a-f2f5121600d9" {
		t.Error("the claude thread id survived — codex must have started its own thread")
	}
	if got := sess.GetProvider(); got != ProviderName {
		t.Errorf("provider = %q, want %q", got, ProviderName)
	}
}
