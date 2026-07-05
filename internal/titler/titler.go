// Package titler generates short human-readable session titles by
// summarizing recent conversation content with a cheap model call.
package titler

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/ngocp/goterm-control/internal/agent"
)

// Titler renames sessions asynchronously after each completed turn.
// Failures are logged and swallowed — the previous label is kept.
type Titler struct {
	provider agent.ModelProvider
	modelID  string

	mu       sync.Mutex
	inflight map[string]bool // session IDs with a refresh already running
}

// New creates a titler that summarizes with the given provider and model.
func New(provider agent.ModelProvider, modelID string) *Titler {
	return &Titler{
		provider: provider,
		modelID:  modelID,
		inflight: make(map[string]bool),
	}
}

const systemPrompt = "You generate short titles for chat sessions. " +
	"Reply with ONLY the title: at most 8 words, in the same language as the conversation, " +
	"no quotes, no trailing punctuation, no explanations."

// maxTitleRunes keeps titles short enough for Telegram inline buttons and
// the dashboard session list.
const maxTitleRunes = 48

// generateTimeout bounds one title call — the CLI provider spawns a
// subprocess, so allow for slow cold starts.
const generateTimeout = 60 * time.Second

// Refresh summarizes the conversation into a title on a background goroutine
// and hands it to apply. No-op when a refresh for the same session is already
// in flight, so back-to-back turns don't stack model calls.
func (t *Titler) Refresh(sessionID string, msgs []agent.Message, apply func(title string)) {
	if t == nil || t.provider == nil || len(msgs) == 0 {
		return
	}
	t.mu.Lock()
	if t.inflight[sessionID] {
		t.mu.Unlock()
		return
	}
	t.inflight[sessionID] = true
	t.mu.Unlock()

	go func() {
		defer func() {
			t.mu.Lock()
			delete(t.inflight, sessionID)
			t.mu.Unlock()
		}()

		ctx, cancel := context.WithTimeout(context.Background(), generateTimeout)
		defer cancel()

		title, err := t.generate(ctx, msgs)
		if err != nil {
			log.Printf("titler: %s: %v", sessionID, err)
			return
		}
		if title == "" {
			return
		}
		apply(title)
	}()
}

func (t *Titler) generate(ctx context.Context, msgs []agent.Message) (string, error) {
	ch, err := t.provider.Stream(ctx, agent.StreamParams{
		Model:        t.modelID,
		SystemPrompt: systemPrompt,
		Messages:     []agent.Message{{Role: "user", Content: buildPrompt(msgs)}},
		MaxTokens:    64,
	})
	if err != nil {
		return "", err
	}

	var sb strings.Builder
	for ev := range ch {
		switch ev.Type {
		case "text":
			sb.WriteString(ev.Text)
		case "error":
			return "", ev.Error
		}
	}
	return sanitize(sb.String()), nil
}

// buildPrompt renders the last few turns; that is enough signal for a title
// while keeping the request cheap.
func buildPrompt(msgs []agent.Message) string {
	if len(msgs) > 6 {
		msgs = msgs[len(msgs)-6:]
	}
	var sb strings.Builder
	sb.WriteString("Summarize this conversation as a session title:\n\n")
	for _, m := range msgs {
		if m.Content == "" {
			continue
		}
		role := "User"
		if m.Role == "assistant" {
			role = "Assistant"
		}
		content := m.Content
		if r := []rune(content); len(r) > 300 {
			content = string(r[:300]) + "…"
		}
		sb.WriteString(role + ": " + content + "\n")
	}
	return sb.String()
}

// sanitize reduces a model reply to a single clean title line.
func sanitize(s string) string {
	s = strings.TrimSpace(s)
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		s = strings.TrimSpace(s[:idx])
	}
	s = strings.Trim(s, `"'“”‘’`)
	s = strings.TrimRight(s, ".。!?")
	if r := []rune(s); len(r) > maxTitleRunes {
		s = strings.TrimSpace(string(r[:maxTitleRunes])) + "…"
	}
	return strings.TrimSpace(s)
}
