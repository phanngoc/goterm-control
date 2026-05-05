package bot

import (
	"encoding/json"
	"strings"
)

// Auto-continue lets the agent loop survive Claude stopping with `end_turn`
// while a TodoWrite list still has pending or in_progress items. Without it,
// the user has to manually nudge the bot ("sao rồi?") to resume work.
const (
	maxAutoContinue     = 3
	autoContinuePrompt  = "continue"
	autoContinueTailLen = 300 // chars of tail text inspected for the question guard
)

// todoItem mirrors the schema of TodoWrite tool input items.
type todoItem struct {
	Content    string `json:"content"`
	Status     string `json:"status"`
	ActiveForm string `json:"activeForm"`
}

// parseTodoWriteInput extracts todos from a TodoWrite tool call's input JSON.
// Returns nil when the JSON shape doesn't match (malformed, wrong type, etc.).
func parseTodoWriteInput(inputJSON string) []todoItem {
	var input struct {
		Todos []todoItem `json:"todos"`
	}
	if err := json.Unmarshal([]byte(inputJSON), &input); err != nil {
		return nil
	}
	return input.Todos
}

// pendingTodos returns the content strings of items still pending or in_progress.
func pendingTodos(todos []todoItem) []string {
	var out []string
	for _, t := range todos {
		if t.Status == "pending" || t.Status == "in_progress" {
			out = append(out, t.Content)
		}
	}
	return out
}

// askingMarkers are phrases that signal the model is asking the user something
// rather than reporting completion. We bail out of auto-continue when we see
// these so we don't stomp on a clarifying question with a "continue" prompt.
var askingMarkers = []string{
	// English
	"shall i ", "should i ", "do you want", "would you like",
	"let me know", "please confirm", "which would you",
	// Vietnamese
	"có muốn", "đúng không", "được không", "có nên",
	"bạn muốn", "bạn cần", "xác nhận", "chọn cái nào",
}

// shouldAutoContinue returns true when the loop should send another "continue"
// to the model. Two preconditions must hold:
//  1. There is at least one pending/in_progress todo (the work is unfinished).
//  2. The latest assistant turn does not look like a question to the user.
//
// The question guard inspects the tail of the text since clarifying questions
// almost always land at the end of an assistant turn.
func shouldAutoContinue(latestText string, pending []string) bool {
	if len(pending) == 0 {
		return false
	}
	trimmed := strings.TrimSpace(latestText)
	if trimmed == "" {
		// No text but pending todos — model probably stopped after tool calls
		// without summarizing. Continue.
		return true
	}

	tail := trimmed
	if runes := []rune(trimmed); len(runes) > autoContinueTailLen {
		tail = string(runes[len(runes)-autoContinueTailLen:])
	}

	// Trailing question mark (ASCII or fullwidth).
	if strings.HasSuffix(tail, "?") || strings.HasSuffix(tail, "？") {
		return false
	}

	tailLower := strings.ToLower(tail)
	for _, m := range askingMarkers {
		if strings.Contains(tailLower, m) {
			return false
		}
	}
	return true
}
