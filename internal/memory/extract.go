package memory

import (
	"regexp"
	"strings"
	"time"
)

var (
	pathRe = regexp.MustCompile(`(?:^|[\s"'` + "`" + `])([/~][\w./-]+)`)
	urlRe  = regexp.MustCompile(`https?://\S+`)
)

// ExtractFacts performs rule-based extraction of key facts from a conversation turn.
// No LLM call — fast and deterministic.
func ExtractFacts(sessionID string, chatID int64, userMessage, assistantResponse string) Entry {
	entry := Entry{
		CreatedAt: time.Now(),
		SessionID: sessionID,
		ChatID:    chatID,
	}

	combined := userMessage + " " + assistantResponse

	// Extract keywords from both messages
	entry.Keywords = extractKeywords(combined)

	// Extract facts: file paths, URLs, and sentences with technical content
	entry.Facts = extractFactItems(combined)

	// Summary: first meaningful sentence of assistant response
	entry.Summary = extractSummary(assistantResponse)

	return entry
}

// extractKeywords returns unique meaningful words from text.
func extractKeywords(text string) []string {
	tokens := tokenize(text)
	seen := make(map[string]bool, len(tokens))
	var unique []string
	for _, t := range tokens {
		if !seen[t] {
			seen[t] = true
			unique = append(unique, t)
		}
	}
	// Cap at 30 keywords to keep entries lean
	if len(unique) > 30 {
		unique = unique[:30]
	}
	return unique
}

// extractFactItems pulls file paths, URLs, and notable phrases.
func extractFactItems(text string) []string {
	var facts []string
	seen := make(map[string]bool)

	// File paths
	for _, match := range pathRe.FindAllStringSubmatch(text, 20) {
		p := strings.TrimSpace(match[1])
		if len(p) > 4 && !seen[p] {
			seen[p] = true
			facts = append(facts, "path: "+p)
		}
	}

	// URLs
	for _, u := range urlRe.FindAllString(text, 10) {
		u = strings.TrimRight(u, ".,;:!?)")
		if !seen[u] {
			seen[u] = true
			facts = append(facts, "url: "+u)
		}
	}

	// Cap at 15 facts
	if len(facts) > 15 {
		facts = facts[:15]
	}
	return facts
}

// extractSummary returns the first sentence of text, capped at 200 chars.
func extractSummary(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}

	// Find first sentence boundary
	for i, ch := range text {
		if (ch == '.' || ch == '!' || ch == '?') && i > 10 && i < 200 {
			return text[:i+1]
		}
	}

	// No sentence boundary found — truncate
	if len(text) > 200 {
		return text[:200] + "..."
	}
	return text
}
