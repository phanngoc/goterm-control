package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// The handshake, the tool list and a call — the whole of what an MCP client
// does with a tools-only server — answered over newline-delimited JSON-RPC.
func TestAServerAnswersTheToolsHandshake(t *testing.T) {
	s := &Server{Name: "bomclaw", Version: "test", Tools: []Tool{
		{Name: "echo", Description: "says it back", Run: func(_ context.Context, a json.RawMessage) (string, error) {
			var p struct{ Text string }
			_ = json.Unmarshal(a, &p)
			return "you said " + p.Text, nil
		}},
		{Name: "fail", Run: func(context.Context, json.RawMessage) (string, error) { return "", errors.New("not today") }},
	}}
	in := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"echo","arguments":{"text":"hi"}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"fail"}}`,
		`{"jsonrpc":"2.0","id":5,"method":"nope"}`,
	}, "\n") + "\n"
	var out bytes.Buffer
	if err := s.Serve(context.Background(), strings.NewReader(in), &out); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 5 { // the notification gets no answer
		t.Fatalf("want 5 answers, got %d:\n%s", len(lines), out.String())
	}
	var init struct {
		Result struct {
			ProtocolVersion string `json:"protocolVersion"`
			Capabilities    map[string]any
		}
	}
	_ = json.Unmarshal([]byte(lines[0]), &init)
	if init.Result.ProtocolVersion != "2024-11-05" || init.Result.Capabilities["tools"] == nil {
		t.Errorf("initialize = %s", lines[0])
	}
	if !strings.Contains(lines[1], `"name":"echo"`) || !strings.Contains(lines[1], `"inputSchema"`) {
		t.Errorf("tools/list = %s", lines[1])
	}
	if !strings.Contains(lines[2], "you said hi") || !strings.Contains(lines[2], `"isError":false`) {
		t.Errorf("call = %s", lines[2])
	}
	if !strings.Contains(lines[3], "not today") || !strings.Contains(lines[3], `"isError":true`) {
		t.Errorf("failing call = %s", lines[3])
	}
	if !strings.Contains(lines[4], "-32601") {
		t.Errorf("unknown method = %s", lines[4])
	}
}
