package bot

import (
	"sync"
	"testing"
	"time"

	"github.com/ngocp/goterm-control/internal/config"
)

func TestTypingIndicator_NilSafe(t *testing.T) {
	// All methods should be safe to call on nil (disabled indicator).
	var ti *TypingIndicator
	ti.Start(123)
	ti.Stop(123)
	ti.Close()
}

func TestTypingIndicator_StartStop(t *testing.T) {
	ti := &TypingIndicator{
		// bot is nil — sendTyping will fail, but the loop logic still works
		interval: 50 * time.Millisecond,
		ttl:      2 * time.Second,
		active:   make(map[int64]chan struct{}),
	}

	ti.Start(100)

	ti.mu.Lock()
	_, active := ti.active[100]
	ti.mu.Unlock()
	if !active {
		t.Fatal("expected chat 100 to be active after Start")
	}

	ti.Stop(100)

	ti.mu.Lock()
	_, active = ti.active[100]
	ti.mu.Unlock()
	if active {
		t.Fatal("expected chat 100 to be inactive after Stop")
	}
}

func TestTypingIndicator_DuplicateStart(t *testing.T) {
	ti := &TypingIndicator{
		interval: 50 * time.Millisecond,
		ttl:      2 * time.Second,
		active:   make(map[int64]chan struct{}),
	}

	ti.Start(200)
	ti.Start(200) // should stop old loop, start new one

	ti.mu.Lock()
	count := len(ti.active)
	ti.mu.Unlock()
	if count != 1 {
		t.Fatalf("expected 1 active entry, got %d", count)
	}

	ti.Close()
}

func TestTypingIndicator_MultipleChats(t *testing.T) {
	ti := &TypingIndicator{
		interval: 50 * time.Millisecond,
		ttl:      2 * time.Second,
		active:   make(map[int64]chan struct{}),
	}

	ti.Start(301)
	ti.Start(302)
	ti.Start(303)

	ti.mu.Lock()
	count := len(ti.active)
	ti.mu.Unlock()
	if count != 3 {
		t.Fatalf("expected 3 active chats, got %d", count)
	}

	ti.Close()

	ti.mu.Lock()
	count = len(ti.active)
	ti.mu.Unlock()
	if count != 0 {
		t.Fatalf("expected 0 active chats after Close, got %d", count)
	}
}

func TestTypingIndicator_TTLExpiry(t *testing.T) {
	ti := &TypingIndicator{
		interval: 10 * time.Millisecond,
		ttl:      80 * time.Millisecond,
		active:   make(map[int64]chan struct{}),
	}

	ti.Start(400)
	time.Sleep(200 * time.Millisecond)

	ti.mu.Lock()
	_, active := ti.active[400]
	ti.mu.Unlock()
	if active {
		t.Fatal("expected chat 400 to expire after TTL")
	}
}

func TestTypingIndicator_ConcurrentStartStop(t *testing.T) {
	ti := &TypingIndicator{
		interval: 10 * time.Millisecond,
		ttl:      2 * time.Second,
		active:   make(map[int64]chan struct{}),
	}

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(chatID int64) {
			defer wg.Done()
			ti.Start(chatID)
			time.Sleep(20 * time.Millisecond)
			ti.Stop(chatID)
		}(int64(500 + i))
	}
	wg.Wait()

	ti.mu.Lock()
	count := len(ti.active)
	ti.mu.Unlock()
	if count != 0 {
		t.Fatalf("expected 0 active after concurrent ops, got %d", count)
	}
}

func TestNewTypingIndicator_Disabled(t *testing.T) {
	cfg := config.IndicatorConfig{UseChatAction: false}
	ti := NewTypingIndicator(nil, cfg, 20*time.Minute)
	if ti != nil {
		t.Fatal("expected nil when UseChatAction is false")
	}
}

func TestNewTypingIndicator_TTLMatchesExecTimeout(t *testing.T) {
	cfg := config.IndicatorConfig{
		UseChatAction:      true,
		ChatActionInterval: 4,
		ChatActionTTL:      120, // 2 minutes — shorter than exec timeout
	}
	execTTL := 20 * time.Minute
	ti := NewTypingIndicator(nil, cfg, execTTL)
	if ti == nil {
		t.Fatal("expected non-nil")
	}
	// TTL should be at least execution timeout, not the shorter config value
	if ti.ttl < execTTL {
		t.Errorf("ttl=%s, want >= %s", ti.ttl, execTTL)
	}
}

func TestNewTypingIndicator_ConfigTTLOverridesWhenLarger(t *testing.T) {
	cfg := config.IndicatorConfig{
		UseChatAction:      true,
		ChatActionInterval: 4,
		ChatActionTTL:      3600, // 1 hour — longer than exec timeout
	}
	execTTL := 20 * time.Minute
	ti := NewTypingIndicator(nil, cfg, execTTL)
	if ti == nil {
		t.Fatal("expected non-nil")
	}
	// Config TTL (1h) is larger than exec timeout (20m), so it wins
	if ti.ttl != time.Hour {
		t.Errorf("ttl=%s, want %s", ti.ttl, time.Hour)
	}
}

func TestNewTypingIndicator_ZeroConfigTTL(t *testing.T) {
	cfg := config.IndicatorConfig{
		UseChatAction:      true,
		ChatActionInterval: 4,
		ChatActionTTL:      0, // not set — should use exec timeout
	}
	execTTL := 20 * time.Minute
	ti := NewTypingIndicator(nil, cfg, execTTL)
	if ti == nil {
		t.Fatal("expected non-nil")
	}
	if ti.ttl != execTTL {
		t.Errorf("ttl=%s, want %s", ti.ttl, execTTL)
	}
}
