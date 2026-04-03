package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ngocp/goterm-control/internal/agent"
	"github.com/ngocp/goterm-control/internal/models"
	"github.com/ngocp/goterm-control/internal/session"
)

// Deps holds the dependencies needed by RPC method handlers.
type Deps struct {
	Sessions *session.Manager
	Resolver *models.Resolver
	Provider agent.ModelProvider
	Tools    []agent.ToolDef
	System   string // system prompt
	Uptime   func() time.Duration
}

// NewMethodHandler creates a MethodHandler that routes to the appropriate handler.
func NewMethodHandler(deps Deps) MethodHandler {
	return func(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
		switch method {
		case "status":
			return handleStatus(deps)
		case "models.list":
			return handleModelsList(deps)
		case "send":
			return handleSend(ctx, deps, params)
		default:
			return nil, fmt.Errorf("unknown method: %s", method)
		}
	}
}

func handleStatus(deps Deps) (json.RawMessage, error) {
	result := StatusResult{
		Running:       true,
		Uptime:        deps.Uptime().Round(time.Second).String(),
		DefaultModel:  deps.Resolver.Default(),
		ActiveSessions: 0,
		Channels:       []string{"telegram", "gateway"},
	}
	return json.Marshal(result)
}

func handleModelsList(deps Deps) (json.RawMessage, error) {
	all := deps.Resolver.List()
	return json.Marshal(all)
}

func handleSend(ctx context.Context, deps Deps, params json.RawMessage) (json.RawMessage, error) {
	var p SendParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if p.Message == "" {
		return nil, fmt.Errorf("message is required")
	}

	modelID := deps.Resolver.Default()
	if p.ModelID != "" {
		m := deps.Resolver.Lookup(p.ModelID)
		if m != nil {
			modelID = m.ID
		}
	}

	m := deps.Resolver.Lookup(modelID)
	maxTokens := 8192
	if m != nil {
		maxTokens = m.MaxTokens
	}

	result, err := agent.RunAgent(ctx, agent.RunParams{
		Provider:     deps.Provider,
		ToolExecutor: nil, // gateway send doesn't execute tools for now
		ModelID:      modelID,
		SystemPrompt: deps.System,
		UserMessage:  p.Message,
		Tools:        deps.Tools,
		MaxTokens:    maxTokens,
	})
	if err != nil {
		return nil, err
	}

	return json.Marshal(map[string]any{
		"text":       result.Text,
		"iterations": result.Iterations,
		"usage":      result.Usage,
	})
}
