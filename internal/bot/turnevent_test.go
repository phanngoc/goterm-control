package bot

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ngocp/goterm-control/internal/config"
	"github.com/ngocp/goterm-control/internal/execution"
	"github.com/ngocp/goterm-control/internal/memory"
	"github.com/ngocp/goterm-control/internal/session"
	"github.com/ngocp/goterm-control/internal/storage"
	"github.com/ngocp/goterm-control/internal/transcript"
)

// TestRunTurnAnnouncesStartAndFinish covers the dashboard staleness bug: with
// Telegram and the web on one session, a Telegram turn wrote the session and
// nothing told an open browser. The turn engine now announces every turn's
// start (user message already persisted) and finish (reply written), in that
// order, carrying the session it touched.
func TestRunTurnAnnouncesStartAndFinish(t *testing.T) {
	dir := t.TempDir()
	sdb, err := storage.Open(filepath.Join(dir, "goterm.db"))
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	defer sdb.Close()

	sessions := session.NewManager(storage.NewSessionStore(sdb))
	h := &Handler{
		sessions:   sessions,
		llm:        &scriptedClient{},
		cfg:        &config.Config{},
		engine:     execution.NewEngine(execution.Hooks{}, 3),
		transcript: transcript.NewWriter(filepath.Join(dir, "transcripts")),
		messages:   storage.NewMessageStore(sdb),
		memory:     memory.NewManager(memory.Config{}),
	}

	var mu sync.Mutex
	var got []TurnEvent
	done := make(chan struct{}, 4)
	h.SetTurnListener(func(ev TurnEvent) {
		mu.Lock()
		got = append(got, ev)
		mu.Unlock()
		done <- struct{}{}
	})

	sess := sessions.Get(42)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := h.RunTurn(ctx, sess, 42, "m", "hello", &recordingSink{}); err != nil {
		t.Fatalf("RunTurn: %v", err)
	}

	// Delivery is asynchronous by design; wait for both events.
	for i := 0; i < 2; i++ {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("only %d turn event(s) arrived", len(got))
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("got %d events, want started + finished: %+v", len(got), got)
	}
	// Both are dispatched in goroutines, so their arrival order is not
	// guaranteed; the contract is one of each, for this session and chat.
	phases := map[string]bool{}
	for _, ev := range got {
		phases[ev.Phase] = true
		if ev.SessionID != sess.ID || ev.ChatID != 42 {
			t.Errorf("event %+v is not for session %s / chat 42", ev, sess.ID)
		}
	}
	if !phases["started"] || !phases["finished"] {
		t.Errorf("phases = %v, want started and finished", phases)
	}
}

// A handler with no listener must run turns exactly as before.
func TestRunTurnWithoutListener(t *testing.T) {
	dir := t.TempDir()
	sdb, err := storage.Open(filepath.Join(dir, "goterm.db"))
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	defer sdb.Close()

	sessions := session.NewManager(storage.NewSessionStore(sdb))
	h := &Handler{
		sessions: sessions,
		llm:      &scriptedClient{},
		cfg:      &config.Config{},
		engine:   execution.NewEngine(execution.Hooks{}, 3),
		memory:   memory.NewManager(memory.Config{}),
	}
	if _, err := h.RunTurn(context.Background(), sessions.Get(7), 7, "m", "hi", &recordingSink{}); err != nil {
		t.Fatalf("RunTurn without listener: %v", err)
	}
}
