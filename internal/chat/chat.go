// Package chat holds the provider-agnostic contract between the bot layer and
// a CLI-backed chat client (claude, codex). It exists so internal/bot does not
// have to import a specific provider package just to name its callback type.
package chat

import (
	"context"
	"strings"

	"github.com/ngocp/goterm-control/internal/session"
	"github.com/ngocp/goterm-control/internal/tools"
)

// StreamCallbacks lets the bot layer react to provider events as they arrive.
type StreamCallbacks struct {
	OnText       func(chunk string)
	OnToolCall   func(name string, inputJSON string)
	OnToolResult func(name string, result tools.ToolResult)
}

// Client is one turn of conversation against a CLI-backed model.
// Implementations own their own session/thread resume semantics; they read the
// provider session id from sess and write it back on the first turn.
type Client interface {
	// SendMessage sends userText and streams the reply through cb.
	// modelID is the already-resolved model. memoryContext is cross-session
	// memory, injected on new sessions only (a resumed session already carries
	// the history, so re-injecting it pollutes the context).
	SendMessage(ctx context.Context, sess *session.Session, modelID string,
		userText string, memoryContext string, cb StreamCallbacks) error

	// Name returns the provider key stored on the session ("claude", "codex").
	Name() string
}

// TurnSink is where a turn's output goes as it happens — the one part of a turn
// that knows which channel it is talking to. Telegram edits a message in place;
// the dashboard pushes WebSocket events. Everything else about a turn (trace,
// transcript, message store, memory, auto-continue) is channel-agnostic and
// lives once, in the bot's turn engine.
//
// It is declared here, in the neutral package, rather than in bot or gateway:
// Go compares method parameter types by identity, so two structurally equal
// interfaces in two packages would make the engine satisfy neither caller.
type TurnSink interface {
	// Write appends a chunk of the assistant's reply.
	Write(chunk string)
	// NoteTool reports a tool the model just invoked, as a short label.
	NoteTool(label string)
	// Flush forces any buffered output out before something else is sent.
	Flush()
	// SendPhoto delivers an image the turn produced (e.g. a screenshot).
	SendPhoto(path, caption string)
	// Finalize marks the reply complete.
	Finalize()
}

// ExtractScreenshotPath finds the .png/.jpg path in a screencapture command so
// the bot can send the file as a photo instead of echoing the shell output.
// Handles quoted paths like screencapture -x "/tmp/foo.png".
func ExtractScreenshotPath(cmd string) string {
	for _, p := range strings.Fields(cmd) {
		if strings.HasPrefix(p, "-") {
			continue
		}
		if p == "screencapture" {
			continue
		}
		if strings.Contains(p, ".png") || strings.Contains(p, ".jpg") {
			// Strip surrounding quotes the model sometimes adds.
			p = strings.Trim(p, `"'`)
			return p
		}
	}
	return ""
}
