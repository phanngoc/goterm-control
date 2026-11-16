// Package mcpserver is a minimal Model Context Protocol server over stdio:
// newline-delimited JSON-RPC 2.0, the tools capability and nothing else.
//
// bomclaw's own tools reach the agents this way — `bomclaw mcp` started by the
// CLI from a bundled plugin — so publishing a project is a tool call the model
// sees in its tool list, not a shell command it has to remember. The protocol
// surface is small enough that a dependency would be larger than the code.
package mcpserver

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
)

// Tool is one callable tool.
type Tool struct {
	Name        string
	Description string
	// Schema is the JSON Schema of the arguments object.
	Schema map[string]any
	// Run does the work. Its text is what the model reads; an error is
	// reported as a tool error (isError), not a protocol failure, so the
	// model can read why and act on it.
	Run func(ctx context.Context, args json.RawMessage) (string, error)
}

// Server answers MCP requests for a fixed set of tools.
type Server struct {
	Name    string
	Version string
	Tools   []Tool
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// latest is the protocol revision offered when the client names none we know.
const latest = "2025-06-18"

var known = map[string]bool{"2024-11-05": true, "2025-03-26": true, "2025-06-18": true}

// Serve reads requests from r and writes responses to w until r ends or ctx
// is canceled. Tool calls run one at a time, in order: a publish takes
// minutes, and two at once for one project would race on its build folder.
func (s *Server) Serve(ctx context.Context, r io.Reader, w io.Writer) error {
	in := bufio.NewScanner(r)
	in.Buffer(make([]byte, 0, 64<<10), 16<<20)
	var mu sync.Mutex
	write := func(v any) {
		b, _ := json.Marshal(v)
		mu.Lock()
		defer mu.Unlock()
		_, _ = w.Write(append(b, '\n'))
	}
	for in.Scan() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		line := in.Bytes()
		if len(line) == 0 {
			continue
		}
		var req request
		if err := json.Unmarshal(line, &req); err != nil {
			write(response{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{-32700, "parse error"}})
			continue
		}
		if len(req.ID) == 0 {
			continue // a notification: initialized, cancelled — nothing to answer
		}
		res, rerr := s.handle(ctx, req)
		write(response{JSONRPC: "2.0", ID: req.ID, Result: res, Error: rerr})
	}
	return in.Err()
}

func (s *Server) handle(ctx context.Context, req request) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		v := p.ProtocolVersion
		if !known[v] {
			v = latest
		}
		return map[string]any{
			"protocolVersion": v,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": s.Name, "version": s.Version},
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		list := make([]map[string]any, 0, len(s.Tools))
		for _, t := range s.Tools {
			schema := t.Schema
			if schema == nil {
				schema = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			list = append(list, map[string]any{"name": t.Name, "description": t.Description, "inputSchema": schema})
		}
		return map[string]any{"tools": list}, nil
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, &rpcError{-32602, "invalid params"}
		}
		for _, t := range s.Tools {
			if t.Name != p.Name {
				continue
			}
			args := p.Arguments
			if len(args) == 0 || string(args) == "null" {
				args = json.RawMessage("{}")
			}
			text, err := t.Run(ctx, args)
			if err != nil {
				if text != "" {
					text += "\n\n"
				}
				return toolResult(text+err.Error(), true), nil
			}
			return toolResult(text, false), nil
		}
		return nil, &rpcError{-32602, fmt.Sprintf("unknown tool %q", p.Name)}
	case "resources/list":
		return map[string]any{"resources": []any{}}, nil
	case "prompts/list":
		return map[string]any{"prompts": []any{}}, nil
	}
	return nil, &rpcError{-32601, "method not found: " + req.Method}
}

func toolResult(text string, isErr bool) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": isErr,
	}
}
