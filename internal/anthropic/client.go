package anthropic

import (
	"context"
	"encoding/json"
	"fmt"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/packages/param"
	"github.com/anthropics/anthropic-sdk-go/packages/ssestream"
	"github.com/ngocp/goterm-control/internal/agent"
)

// Client implements agent.ModelProvider using the Anthropic Messages API directly.
// It also provides Complete() for non-streaming single-turn calls (used by memory extraction).
type Client struct {
	sdk sdk.Client
}

// New creates an Anthropic API client.
func New(apiKey string) *Client {
	return &Client{
		sdk: sdk.NewClient(option.WithAPIKey(apiKey)),
	}
}

// Complete makes a non-streaming single-turn API call and returns the text response.
// Used for lightweight tasks like memory extraction where streaming is unnecessary.
func (c *Client) Complete(ctx context.Context, model, system, userMessage string, maxTokens int) (string, error) {
	if maxTokens <= 0 {
		maxTokens = 1024
	}

	var systemBlocks []sdk.TextBlockParam
	if system != "" {
		systemBlocks = []sdk.TextBlockParam{{Text: system}}
	}

	resp, err := c.sdk.Messages.New(ctx, sdk.MessageNewParams{
		Model:     sdk.Model(model),
		MaxTokens: int64(maxTokens),
		System:    systemBlocks,
		Messages: []sdk.MessageParam{
			sdk.NewUserMessage(sdk.NewTextBlock(userMessage)),
		},
	})
	if err != nil {
		return "", fmt.Errorf("anthropic complete: %w", err)
	}

	// Extract text from response content blocks
	var text string
	for _, block := range resp.Content {
		if tb, ok := block.AsAny().(sdk.TextBlock); ok {
			text += tb.Text
		}
	}
	return text, nil
}

// Stream implements agent.ModelProvider.
func (c *Client) Stream(ctx context.Context, params agent.StreamParams) (<-chan agent.StreamEvent, error) {
	messages := buildMessages(params.Messages)

	var system []sdk.TextBlockParam
	if params.SystemPrompt != "" {
		system = []sdk.TextBlockParam{{Text: params.SystemPrompt}}
	}

	tools := buildTools(params.Tools)

	maxTokens := int64(params.MaxTokens)
	if maxTokens <= 0 {
		maxTokens = 8192
	}

	apiParams := sdk.MessageNewParams{
		Model:     sdk.Model(params.Model),
		MaxTokens: maxTokens,
		Messages:  messages,
		System:    system,
		Tools:     tools,
	}

	stream := c.sdk.Messages.NewStreaming(ctx, apiParams)

	ch := make(chan agent.StreamEvent, 64)
	go func() {
		defer close(ch)
		processStream(stream, ch)
	}()

	return ch, nil
}

func processStream(stream *ssestream.Stream[sdk.MessageStreamEventUnion], ch chan<- agent.StreamEvent) {
	var currentToolID string
	var currentToolName string
	var toolInputBuf []byte

	for stream.Next() {
		ev := stream.Current()

		switch v := ev.AsAny().(type) {
		case sdk.MessageStartEvent:
			if v.Message.Usage.InputTokens > 0 {
				// Don't send as "end" — just note usage will come at the real end
			}

		case sdk.ContentBlockStartEvent:
			block := v.ContentBlock
			if tu, ok := block.AsAny().(sdk.ToolUseBlock); ok {
				currentToolID = tu.ID
				currentToolName = tu.Name
				toolInputBuf = nil
			}

		case sdk.ContentBlockDeltaEvent:
			switch d := v.Delta.AsAny().(type) {
			case sdk.TextDelta:
				ch <- agent.StreamEvent{Type: "text", Text: d.Text}
			case sdk.InputJSONDelta:
				toolInputBuf = append(toolInputBuf, []byte(d.PartialJSON)...)
			}

		case sdk.ContentBlockStopEvent:
			if currentToolID != "" {
				ch <- agent.StreamEvent{
					Type:      "tool_use",
					ToolID:    currentToolID,
					ToolName:  currentToolName,
					ToolInput: json.RawMessage(toolInputBuf),
				}
				currentToolID = ""
				currentToolName = ""
				toolInputBuf = nil
			}

		case sdk.MessageDeltaEvent:
			ch <- agent.StreamEvent{
				Type:       "end",
				StopReason: string(v.Delta.StopReason),
				Usage: &agent.Usage{
					OutputTokens: int(v.Usage.OutputTokens),
				},
			}
		}
	}

	if err := stream.Err(); err != nil {
		ch <- agent.StreamEvent{Type: "error", Error: fmt.Errorf("anthropic: %w", err)}
	}
}

func buildMessages(msgs []agent.Message) []sdk.MessageParam {
	var out []sdk.MessageParam
	for _, m := range msgs {
		switch {
		case len(m.ToolResults) > 0:
			var blocks []sdk.ContentBlockParamUnion
			for _, tr := range m.ToolResults {
				blocks = append(blocks, sdk.NewToolResultBlock(tr.ID, tr.Content, tr.IsError))
			}
			out = append(out, sdk.NewUserMessage(blocks...))

		case len(m.ToolCalls) > 0:
			var blocks []sdk.ContentBlockParamUnion
			if m.Content != "" {
				blocks = append(blocks, sdk.NewTextBlock(m.Content))
			}
			for _, tc := range m.ToolCalls {
				var input any
				_ = json.Unmarshal(tc.Input, &input)
				if input == nil {
					input = map[string]any{} // API requires input to be an object, not null
				}
				blocks = append(blocks, sdk.NewToolUseBlock(tc.ID, input, tc.Name))
			}
			out = append(out, sdk.NewAssistantMessage(blocks...))

		default:
			role := sdk.MessageParamRole(m.Role)
			out = append(out, sdk.MessageParam{
				Role:    role,
				Content: []sdk.ContentBlockParamUnion{sdk.NewTextBlock(m.Content)},
			})
		}
	}
	return out
}

func buildTools(defs []agent.ToolDef) []sdk.ToolUnionParam {
	out := make([]sdk.ToolUnionParam, len(defs))
	for i, d := range defs {
		props := map[string]any{}
		var required []string
		if schema, ok := d.InputSchema.(map[string]any); ok {
			if p, ok := schema["properties"]; ok {
				if pm, ok := p.(map[string]any); ok {
					props = pm
				}
			}
			if r, ok := schema["required"]; ok {
				if rs, ok := r.([]string); ok {
					required = rs
				} else if ra, ok := r.([]any); ok {
					for _, v := range ra {
						if s, ok := v.(string); ok {
							required = append(required, s)
						}
					}
				}
			}
		}
		out[i] = sdk.ToolUnionParam{
			OfTool: &sdk.ToolParam{
				Name:        d.Name,
				Description: param.Opt[string]{Value: d.Description},
				InputSchema: sdk.ToolInputSchemaParam{
					Properties: props,
					Required:   required,
				},
			},
		}
	}
	return out
}
