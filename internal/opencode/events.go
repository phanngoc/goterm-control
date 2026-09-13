package opencode

import "encoding/json"

// The shape of `opencode run --format json`.
//
// Read off the CLI itself (packages/opencode/src/cli/cmd/run.ts), not guessed:
// every line is one JSON object of {type, timestamp, sessionID, ...data}, and
// the data for the interesting types is a single `part`.
//
// Two things about it decide how the client below is written. Every event
// carries sessionID, so resuming needs no separate lookup — the first line of
// the first turn is enough. And a text part is only emitted once it is
// finished (the CLI gates on part.time.end), so "streaming" here means whole
// paragraphs, not tokens: the sink sees fewer, larger writes than it does from
// claude, which is a difference in feel, not in correctness.
type event struct {
	Type      string          `json:"type"`
	SessionID string          `json:"sessionID"`
	Part      *part           `json:"part,omitempty"`
	Error     json.RawMessage `json:"error,omitempty"`
}

type part struct {
	Type  string     `json:"type"`  // text | tool | step-start | step-finish | reasoning
	Text  string     `json:"text,omitempty"`
	Tool  string     `json:"tool,omitempty"`
	State *toolState `json:"state,omitempty"`
}

type toolState struct {
	Status string          `json:"status"` // completed | error | running | pending
	Title  string          `json:"title,omitempty"`
	Input  json.RawMessage `json:"input,omitempty"`
	Output string          `json:"output,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// inputJSON renders a tool's arguments for the callback, which takes a string.
// An absent input is "{}" rather than "": the bot layer logs this verbatim and
// an empty string there reads like a parse failure rather than a call with no
// arguments.
func (s *toolState) inputJSON() string {
	if s == nil || len(s.Input) == 0 {
		return "{}"
	}
	return string(s.Input)
}
