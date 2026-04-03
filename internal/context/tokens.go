package context

import "encoding/json"

// EstimateTokens estimates the token count for a string.
// Uses a simple heuristic: ~4 characters per token for English text (cl100k-style).
// This avoids needing a tokenizer dependency while being accurate enough for budgeting.
func EstimateTokens(text string) int {
	if len(text) == 0 {
		return 0
	}
	// ~4 chars per token for Claude's tokenizer. Add 10% margin.
	return (len(text) * 11) / (4 * 10)
}

// EstimateJSONTokens estimates tokens for a JSON-serializable value.
func EstimateJSONTokens(v any) int {
	data, err := json.Marshal(v)
	if err != nil {
		return 0
	}
	return EstimateTokens(string(data))
}
