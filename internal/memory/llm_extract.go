package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"
)

// Completer makes a non-streaming LLM call. Implemented by anthropic.Client.
type Completer interface {
	Complete(ctx context.Context, model, system, userMessage string, maxTokens int) (string, error)
}

// extractionModel is the model used for memory extraction (fast + cheap).
const extractionModel = "claude-haiku-4-5-20251001"

const extractionPrompt = `You are a memory extraction system. Given a user message and assistant response from a conversation, extract structured memory data.

Return ONLY valid JSON with this exact schema:
{
  "keywords": ["semantic", "keywords", "capturing", "intent"],
  "facts": ["path: /file/paths", "url: https://urls", "error: error messages", "decision: technical decisions"],
  "summary": "One sentence capturing the main point of this exchange",
  "intent": "What the user is trying to accomplish (one short sentence)"
}

Rules:
- keywords: 5-15 semantic keywords that capture the MEANING, not just raw words. Include technologies, concepts, and actions.
- facts: Extract file paths (prefix "path:"), URLs (prefix "url:"), error messages (prefix "error:"), code references (prefix "ref:"), and technical decisions (prefix "decision:"). Max 15 items.
- summary: Capture the main point of the conversation turn, not just the first sentence. Max 200 chars.
- intent: What the user is trying to do in one short sentence. Max 100 chars.`

// extractionResult is the JSON structure returned by the LLM.
type extractionResult struct {
	Keywords []string `json:"keywords"`
	Facts    []string `json:"facts"`
	Summary  string   `json:"summary"`
	Intent   string   `json:"intent"`
}

// ExtractFactsLLM uses an LLM to extract structured memory from a conversation turn.
// Falls back to rule-based extraction on any error.
func ExtractFactsLLM(ctx context.Context, client Completer, sessionID string, chatID int64, userMessage, assistantResponse string) (Entry, error) {
	combined := fmt.Sprintf("USER MESSAGE:\n%s\n\nASSISTANT RESPONSE:\n%s", userMessage, truncate(assistantResponse, 2000))

	// Check cache first
	cacheKey := contentHash(combined)
	if cached, ok := extractionCache.get(cacheKey); ok {
		cached.SessionID = sessionID
		cached.ChatID = chatID
		cached.CreatedAt = time.Now()
		return cached, nil
	}

	resp, err := client.Complete(ctx, extractionModel, extractionPrompt, combined, 500)
	if err != nil {
		return Entry{}, fmt.Errorf("llm extraction: %w", err)
	}

	var result extractionResult
	if err := json.Unmarshal([]byte(resp), &result); err != nil {
		// Try to extract JSON from response (LLM might add markdown fences)
		if cleaned := extractJSON(resp); cleaned != "" {
			if err2 := json.Unmarshal([]byte(cleaned), &result); err2 != nil {
				return Entry{}, fmt.Errorf("parse extraction response: %w", err)
			}
		} else {
			return Entry{}, fmt.Errorf("parse extraction response: %w", err)
		}
	}

	entry := Entry{
		CreatedAt: time.Now(),
		SessionID: sessionID,
		ChatID:    chatID,
		Keywords:  result.Keywords,
		Facts:     result.Facts,
		Summary:   result.Summary,
		Intent:    result.Intent,
	}

	// Cache the result
	extractionCache.set(cacheKey, entry)

	return entry, nil
}

// truncate limits text to maxLen characters.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// extractJSON tries to find a JSON object in text (handles markdown code fences).
func extractJSON(text string) string {
	// Find first { and last }
	start := -1
	end := -1
	for i, ch := range text {
		if ch == '{' && start == -1 {
			start = i
		}
		if ch == '}' {
			end = i
		}
	}
	if start >= 0 && end > start {
		return text[start : end+1]
	}
	return ""
}

func contentHash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:16])
}

// --- In-memory cache with TTL ---

var extractionCache = newExtrCache()

type extrCache struct {
	mu      sync.RWMutex
	entries map[string]extrCacheEntry
}

type extrCacheEntry struct {
	entry   Entry
	expires time.Time
}

const cacheTTL = 1 * time.Hour
const cacheMaxSize = 100

func newExtrCache() *extrCache {
	return &extrCache{entries: make(map[string]extrCacheEntry)}
}

func (c *extrCache) get(key string) (Entry, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.entries[key]
	if !ok || time.Now().After(e.expires) {
		return Entry{}, false
	}
	return e.entry, true
}

func (c *extrCache) set(key string, entry Entry) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Evict expired entries if cache is full
	if len(c.entries) >= cacheMaxSize {
		now := time.Now()
		for k, v := range c.entries {
			if now.After(v.expires) {
				delete(c.entries, k)
			}
		}
		// If still full, clear oldest half
		if len(c.entries) >= cacheMaxSize {
			count := 0
			for k := range c.entries {
				delete(c.entries, k)
				count++
				if count >= cacheMaxSize/2 {
					break
				}
			}
		}
	}

	c.entries[key] = extrCacheEntry{
		entry:   entry,
		expires: time.Now().Add(cacheTTL),
	}
}

// logLLMFallback logs when LLM extraction fails and falls back to rule-based.
func logLLMFallback(err error) {
	log.Printf("memory: LLM extraction failed, falling back to rule-based: %v", err)
}
