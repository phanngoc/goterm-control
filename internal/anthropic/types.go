package anthropic

import "github.com/ngocp/goterm-control/internal/agent"

// ToolDefsToSDK converts agent.ToolDef slice to the format needed by the Anthropic SDK.
// This bridges our internal tool definitions with the SDK's ToolParam type.
func ToolDefsToSDK(defs []agent.ToolDef) []map[string]any {
	out := make([]map[string]any, len(defs))
	for i, d := range defs {
		out[i] = map[string]any{
			"name":         d.Name,
			"description":  d.Description,
			"input_schema": d.InputSchema,
		}
	}
	return out
}
