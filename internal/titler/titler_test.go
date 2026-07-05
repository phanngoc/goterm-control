package titler

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ngocp/goterm-control/internal/agent"
)

// fakeProvider streams a fixed reply after an optional delay.
type fakeProvider struct {
	reply string
	delay time.Duration
	calls int
}

func (f *fakeProvider) Stream(_ context.Context, _ agent.StreamParams) (<-chan agent.StreamEvent, error) {
	f.calls++
	ch := make(chan agent.StreamEvent, 2)
	go func() {
		time.Sleep(f.delay)
		ch <- agent.StreamEvent{Type: "text", Text: f.reply}
		ch <- agent.StreamEvent{Type: "end", StopReason: "end_turn"}
		close(ch)
	}()
	return ch, nil
}

func waitFor(t *testing.T, ch <-chan string) string {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for title")
		return ""
	}
}

func TestRefreshAppliesSanitizedTitle(t *testing.T) {
	p := &fakeProvider{reply: "\"Fix telegram bot restart conflict.\"\nextra line"}
	tl := New(p, "test-model")

	got := make(chan string, 1)
	tl.Refresh("chat_1", []agent.Message{{Role: "user", Content: "hello"}}, func(title string) {
		got <- title
	})

	if title := waitFor(t, got); title != "Fix telegram bot restart conflict" {
		t.Errorf("title = %q", title)
	}
}

func TestRefreshDedupesInflight(t *testing.T) {
	p := &fakeProvider{reply: "Title", delay: 100 * time.Millisecond}
	tl := New(p, "test-model")

	got := make(chan string, 2)
	msgs := []agent.Message{{Role: "user", Content: "hello"}}
	apply := func(title string) { got <- title }

	tl.Refresh("chat_1", msgs, apply)
	tl.Refresh("chat_1", msgs, apply) // second call while first is in flight

	waitFor(t, got)
	if p.calls != 1 {
		t.Errorf("provider called %d times, want 1", p.calls)
	}

	// After the first refresh completes, a new one is allowed again.
	tl.Refresh("chat_1", msgs, apply)
	waitFor(t, got)
	if p.calls != 2 {
		t.Errorf("provider called %d times, want 2", p.calls)
	}
}

func TestSanitizeTruncatesLongTitles(t *testing.T) {
	long := strings.Repeat("từ ", 40)
	out := sanitize(long)
	if r := []rune(out); len(r) > maxTitleRunes+1 { // +1 for the ellipsis
		t.Errorf("sanitized title too long: %d runes (%q)", len(r), out)
	}
}

func TestNilTitlerIsSafe(t *testing.T) {
	var tl *Titler
	tl.Refresh("chat_1", []agent.Message{{Role: "user", Content: "x"}}, func(string) {
		t.Error("apply must not be called on nil titler")
	})
}
