---
title: API Reference
layout: default
---

<section class="doc-page">
<div class="doc-content" markdown="1">

# API Reference

<p class="lead">WebSocket JSON-RPC API for programmatic control of the NanoClaw gateway.</p>

## Connection

Connect via WebSocket to the gateway:

```
ws://127.0.0.1:18789/ws
```

All messages use the [JSON-RPC 2.0](https://www.jsonrpc.org/specification) protocol.

## HTTP Endpoints

| Endpoint | Method | Description |
|---|---|---|
| `/health` | GET | Health check, returns `{"status": "ok"}` |
| `/ws` | WebSocket | JSON-RPC WebSocket endpoint |
| `/*` | GET | Serves dashboard static files |

## JSON-RPC Methods

### `status`

Get gateway health and status.

**Request:**
```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "status"
}
```

**Response:**
```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "result": {
    "status": "ok",
    "uptime": "2h15m30s",
    "sessions": 3
  }
}
```

### `models.list`

List available models with pricing and aliases.

**Request:**
```json
{
  "jsonrpc": "2.0",
  "id": 2,
  "method": "models.list"
}
```

**Response:**
```json
{
  "jsonrpc": "2.0",
  "id": 2,
  "result": {
    "models": [
      {
        "id": "claude-opus-4-6",
        "name": "Claude Opus 4.6",
        "aliases": ["opus", "o4"],
        "context_window": 200000,
        "input_price": 15.0,
        "output_price": 75.0
      }
    ],
    "default": "claude-sonnet-4-6"
  }
}
```

### `sessions.list`

List all sessions.

**Request:**
```json
{
  "jsonrpc": "2.0",
  "id": 3,
  "method": "sessions.list"
}
```

**Response:**
```json
{
  "jsonrpc": "2.0",
  "id": 3,
  "result": {
    "sessions": [
      {
        "id": "chat_abc123",
        "created_at": "2026-04-10T10:00:00Z",
        "updated_at": "2026-04-10T10:15:00Z",
        "message_count": 12,
        "model": "claude-sonnet-4-6"
      }
    ]
  }
}
```

### `sessions.get`

Get details for a specific session.

**Request:**
```json
{
  "jsonrpc": "2.0",
  "id": 4,
  "method": "sessions.get",
  "params": {
    "session_id": "chat_abc123"
  }
}
```

### `sessions.reset`

Clear a session's conversation history.

**Request:**
```json
{
  "jsonrpc": "2.0",
  "id": 5,
  "method": "sessions.reset",
  "params": {
    "session_id": "chat_abc123"
  }
}
```

### `transcript.get`

Load the full event transcript for a session.

**Request:**
```json
{
  "jsonrpc": "2.0",
  "id": 6,
  "method": "transcript.get",
  "params": {
    "session_id": "chat_abc123"
  }
}
```

**Response:**
```json
{
  "jsonrpc": "2.0",
  "id": 6,
  "result": {
    "events": [
      {
        "type": "user_message",
        "timestamp": "2026-04-10T10:00:00Z",
        "content": "Hello!"
      },
      {
        "type": "assistant_text",
        "timestamp": "2026-04-10T10:00:01Z",
        "content": "Hi! How can I help?"
      }
    ]
  }
}
```

### `send`

Send a message and receive a streaming response.

**Request:**
```json
{
  "jsonrpc": "2.0",
  "id": 7,
  "method": "send",
  "params": {
    "session_id": "chat_abc123",
    "message": "List all files in the current directory",
    "model": "opus"
  }
}
```

**Streaming events** (sent as separate messages with the same `id`):

```json
// Text chunk
{
  "jsonrpc": "2.0",
  "id": 7,
  "method": "stream.text",
  "params": { "text": "I'll list the files " }
}

// Tool use
{
  "jsonrpc": "2.0",
  "id": 7,
  "method": "stream.tool_use",
  "params": {
    "tool": "run_shell",
    "input": { "command": "ls -la" }
  }
}

// Tool result
{
  "jsonrpc": "2.0",
  "id": 7,
  "method": "stream.tool_result",
  "params": {
    "tool": "run_shell",
    "output": "total 48\ndrwxr-xr-x  12 user  staff  384 Apr 10 10:00 .\n..."
  }
}

// Final response
{
  "jsonrpc": "2.0",
  "id": 7,
  "result": {
    "status": "complete",
    "tokens": { "input": 1250, "output": 340 }
  }
}
```

### `cancel`

Cancel an in-flight request.

**Request:**
```json
{
  "jsonrpc": "2.0",
  "id": 8,
  "method": "cancel",
  "params": {
    "session_id": "chat_abc123"
  }
}
```

## Client Example

### JavaScript/TypeScript

```javascript
const ws = new WebSocket('ws://127.0.0.1:18789/ws');
let requestId = 0;

ws.onopen = () => {
  // Send a message
  ws.send(JSON.stringify({
    jsonrpc: '2.0',
    id: ++requestId,
    method: 'send',
    params: {
      session_id: 'my-session',
      message: 'What files are in ~/Documents?'
    }
  }));
};

ws.onmessage = (event) => {
  const msg = JSON.parse(event.data);
  
  if (msg.method === 'stream.text') {
    process.stdout.write(msg.params.text);
  } else if (msg.method === 'stream.tool_use') {
    console.log(`\n[Tool: ${msg.params.tool}]`);
  } else if (msg.result) {
    console.log('\n--- Complete ---');
  }
};
```

### Python

```python
import json
import websocket

ws = websocket.create_connection("ws://127.0.0.1:18789/ws")

ws.send(json.dumps({
    "jsonrpc": "2.0",
    "id": 1,
    "method": "send",
    "params": {
        "session_id": "my-session",
        "message": "Hello, Claude!"
    }
}))

while True:
    msg = json.loads(ws.recv())
    if "method" in msg and msg["method"] == "stream.text":
        print(msg["params"]["text"], end="", flush=True)
    elif "result" in msg:
        print("\n--- Complete ---")
        break

ws.close()
```

### Go

```go
import (
    "github.com/gorilla/websocket"
    "encoding/json"
)

conn, _, _ := websocket.DefaultDialer.Dial("ws://127.0.0.1:18789/ws", nil)

request := map[string]interface{}{
    "jsonrpc": "2.0",
    "id":      1,
    "method":  "send",
    "params": map[string]string{
        "session_id": "my-session",
        "message":    "Hello from Go!",
    },
}
conn.WriteJSON(request)

for {
    _, msg, _ := conn.ReadMessage()
    var resp map[string]interface{}
    json.Unmarshal(msg, &resp)
    
    if method, ok := resp["method"]; ok && method == "stream.text" {
        params := resp["params"].(map[string]interface{})
        fmt.Print(params["text"])
    }
    if _, ok := resp["result"]; ok {
        break
    }
}
```

<div class="doc-nav">
  <a href="{{ '/configuration' | relative_url }}">
    <div class="label">Previous</div>
    <div class="title">Configuration</div>
  </a>
  <a href="{{ '/' | relative_url }}" class="next">
    <div class="label">Back to</div>
    <div class="title">Home</div>
  </a>
</div>

</div>
</section>
