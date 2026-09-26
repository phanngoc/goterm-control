package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// fakeAgent is an agent's /ws: it answers "status", drops the connection on
// "die", and announces itself with a broadcast on connect.
func fakeAgent(t *testing.T, gotCookie chan<- string) *httptest.Server {
	t.Helper()
	up := websocket.Upgrader{}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if gotCookie != nil {
			select {
			case gotCookie <- r.Header.Get("Cookie"):
			default:
			}
		}
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		_ = c.WriteJSON(StreamEvent{Type: "event", Event: "session.turn", Data: "{}"})
		for {
			_, msg, err := c.ReadMessage()
			if err != nil {
				return
			}
			var req Request
			_ = json.Unmarshal(msg, &req)
			switch req.Method {
			case "die":
				return
			case "status":
				_ = c.WriteJSON(StreamEvent{ID: req.ID, Type: "stream", Event: "text", Data: "partial"})
				_ = c.WriteJSON(Response{ID: req.ID, Result: json.RawMessage(`"from agent"`)})
			}
		}
	}))
}

func dashboardFor(t *testing.T, upstream func() (string, error)) *websocket.Conn {
	t.Helper()
	local := func(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`"from dashboard"`), nil
	}
	s := NewServer("", local, nil, "", nil)
	s.SetRelay(&Relay{Local: local, Upstream: upstream, Retry: 20 * time.Millisecond})
	ts := httptest.NewServer(http.HandlerFunc(s.handleWS))
	t.Cleanup(ts.Close)
	hdr := http.Header{"Cookie": {"bomclaw_session=abc"}}
	c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http"), hdr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func wsURL(ts *httptest.Server) string { return "ws" + strings.TrimPrefix(ts.URL, "http") }

// readUntil reads frames until one matches, so broadcasts and stream events in
// between do not make the test order-sensitive.
func readUntil(t *testing.T, c *websocket.Conn, match func(map[string]any) bool) map[string]any {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		var m map[string]any
		if err := c.ReadJSON(&m); err != nil {
			t.Fatalf("read: %v", err)
		}
		if match(m) {
			return m
		}
	}
}

func finalFor(id string) func(map[string]any) bool {
	return func(m map[string]any) bool { return m["id"] == id && m["type"] == nil }
}

func TestTheDashboardAnswersFromTheSharedDatabaseWhileTheAgentIsDown(t *testing.T) {
	c := dashboardFor(t, func() (string, error) { return "ws://127.0.0.1:1/ws", nil })

	_ = c.WriteJSON(Request{ID: "1", Method: "tasks.list"})
	if m := readUntil(t, c, finalFor("1")); m["result"] != "from dashboard" {
		t.Fatalf("tasks.list = %v, want the dashboard's own answer", m)
	}

	_ = c.WriteJSON(Request{ID: "2", Method: "status"})
	m := readUntil(t, c, finalFor("2"))
	e, _ := m["error"].(map[string]any)
	if e == nil || !strings.Contains(e["message"].(string), "not reachable") {
		t.Fatalf("status with the agent down = %v, want an error saying so", m)
	}
}

func TestChatMethodsAndBroadcastsPassThroughToTheAgent(t *testing.T) {
	cookie := make(chan string, 1)
	agent := fakeAgent(t, cookie)
	defer agent.Close()
	c := dashboardFor(t, func() (string, error) { return wsURL(agent), nil })

	readUntil(t, c, func(m map[string]any) bool { return m["event"] == "session.turn" })
	if got := <-cookie; got != "bomclaw_session=abc" {
		t.Fatalf("agent saw cookie %q, want the browser's", got)
	}

	_ = c.WriteJSON(Request{ID: "7", Method: "status"})
	readUntil(t, c, func(m map[string]any) bool { return m["id"] == "7" && m["type"] == "stream" })
	if m := readUntil(t, c, finalFor("7")); m["result"] != "from agent" {
		t.Fatalf("status = %v, want the agent's answer", m)
	}
}

func TestARequestCaughtByTheAgentGoingAwayIsAnswered(t *testing.T) {
	agent := fakeAgent(t, nil)
	defer agent.Close()
	c := dashboardFor(t, func() (string, error) { return wsURL(agent), nil })
	readUntil(t, c, func(m map[string]any) bool { return m["event"] == "session.turn" })

	_ = c.WriteJSON(Request{ID: "9", Method: "die"})
	m := readUntil(t, c, finalFor("9"))
	if e, _ := m["error"].(map[string]any); e == nil || !strings.Contains(e["message"].(string), "dropped") {
		t.Fatalf("request lost with the agent = %v, want an error", m)
	}

	// And the link comes back on its own: the next connect's broadcast arrives.
	readUntil(t, c, func(m map[string]any) bool { return m["event"] == "session.turn" })
}

func TestDetachedSettingsTreatEveryAgentAsAPeer(t *testing.T) {
	deps := Deps{AgentID: "bomclaw", Detached: true}
	raw, err := handleAdminSettingsAll(deps)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "[]" {
		t.Fatalf("settings from the dashboard with no agents = %s, want no self row", raw)
	}
	if _, err := handleAdminSetModelOn(deps, json.RawMessage(`{"model":"claude-opus-5-5"}`)); err == nil {
		t.Fatal("a model change with no agent named was not refused")
	}
	if _, err := handleAdminRestartOn(deps, json.RawMessage(`{}`)); err == nil {
		t.Fatal("a restart with no agent named was not refused")
	}
}
