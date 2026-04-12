package memory

import (
	"context"
	"regexp"
	"strings"
	"time"
)

var (
	pathRe = regexp.MustCompile(`(?:^|[\s"'` + "`" + `])([/~][\w./-]+)`)
	urlRe  = regexp.MustCompile(`https?://\S+`)
)

// Extract dispatches to the appropriate extraction method based on mode.
// Modes: "llm" (LLM with rule-based fallback), "rule" (rule-based only), "hybrid" (merge both).
// If client is nil, falls back to rule-based regardless of mode.
func Extract(ctx context.Context, client Completer, mode string, sessionID string, chatID int64, userMsg, assistantResp string) Entry {
	switch mode {
	case "llm":
		if client == nil {
			return ExtractFacts(sessionID, chatID, userMsg, assistantResp)
		}
		entry, err := ExtractFactsLLM(ctx, client, sessionID, chatID, userMsg, assistantResp)
		if err != nil {
			logLLMFallback(err)
			return ExtractFacts(sessionID, chatID, userMsg, assistantResp)
		}
		return entry

	case "hybrid":
		ruleBased := ExtractFacts(sessionID, chatID, userMsg, assistantResp)
		if client == nil {
			return ruleBased
		}
		llmBased, err := ExtractFactsLLM(ctx, client, sessionID, chatID, userMsg, assistantResp)
		if err != nil {
			logLLMFallback(err)
			return ruleBased
		}
		return mergeEntries(llmBased, ruleBased)

	default: // "rule"
		return ExtractFacts(sessionID, chatID, userMsg, assistantResp)
	}
}

// mergeEntries combines LLM and rule-based entries, preferring LLM for keywords/summary/intent
// and merging facts from both sources.
func mergeEntries(llm, rule Entry) Entry {
	merged := llm // Start with LLM result (better keywords, summary, intent)

	// Merge facts: add rule-based facts not already captured by LLM
	seen := make(map[string]bool, len(llm.Facts))
	for _, f := range llm.Facts {
		seen[f] = true
	}
	for _, f := range rule.Facts {
		if !seen[f] {
			merged.Facts = append(merged.Facts, f)
		}
	}

	// Cap merged facts
	if len(merged.Facts) > 20 {
		merged.Facts = merged.Facts[:20]
	}

	return merged
}

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
