package browserbridge

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

const testToken = "secret-pairing-token"

func newTestHub(t *testing.T, mod func(*Options)) (*Hub, string) {
	t.Helper()
	opts := Options{
		Token:       testToken,
		AgentID:     "a1",
		AgentName:   "Agent One",
		CallTimeout: 2 * time.Second,
		AllowEval:   true,
		Logf:        func(string, ...any) {},
	}
	if mod != nil {
		mod(&opts)
	}
	hub := New(opts)
	srv := httptest.NewServer(hub)
	t.Cleanup(srv.Close)
	return hub, "ws" + strings.TrimPrefix(srv.URL, "http") + "/ext"
}

// fakeExtension dials the hub as the extension would and returns the socket
// once the welcome frame has arrived.
func fakeExtension(t *testing.T, wsURL, token string) *websocket.Conn {
	t.Helper()
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { ws.Close() })
	if err := ws.WriteJSON(frame{Type: "hello", Token: token, Client: "test-ext/1", Browser: "TestBrowser/1"}); err != nil {
		t.Fatalf("hello: %v", err)
	}
	return ws
}

func readFrame(t *testing.T, ws *websocket.Conn) (frame, error) {
	t.Helper()
	_ = ws.SetReadDeadline(time.Now().Add(3 * time.Second))
	var f frame
	err := ws.ReadJSON(&f)
	return f, err
}

func expectWelcome(t *testing.T, ws *websocket.Conn) {
	t.Helper()
	f, err := readFrame(t, ws)
	if err != nil || f.Type != "welcome" {
		t.Fatalf("expected welcome, got %+v (err %v)", f, err)
	}
	if f.Agent != "a1" || f.Name != "Agent One" {
		t.Fatalf("welcome should name the agent, got %+v", f)
	}
}

func closeCode(err error) int {
	var ce *websocket.CloseError
	if errors.As(err, &ce) {
		return ce.Code
	}
	return 0
}

// waitConnected gives the hub's handler a moment to install the connection
// after the welcome has been sent.
func waitConnected(t *testing.T, hub *Hub) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !hub.Connected() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !hub.Connected() {
		t.Fatal("hub never reported the browser as connected")
	}
}

func TestHubRejectsWrongToken(t *testing.T) {
	hub, wsURL := newTestHub(t, nil)
	ws := fakeExtension(t, wsURL, "not-the-token")

	_, err := readFrame(t, ws)
	if code := closeCode(err); code != closeUnauthorized {
		t.Fatalf("expected close %d for a wrong token, got err=%v", closeUnauthorized, err)
	}
	if hub.Connected() {
		t.Fatal("a rejected extension must not count as connected")
	}
}

func TestHubRejectsNonHelloFirstFrame(t *testing.T) {
	_, wsURL := newTestHub(t, nil)
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	_ = ws.WriteJSON(frame{Type: "result", ID: "1", OK: true})
	_, err = readFrame(t, ws)
	if code := closeCode(err); code != closeBadHandshake {
		t.Fatalf("expected close %d, got err=%v", closeBadHandshake, err)
	}
}

func TestHubCallRoundTrip(t *testing.T) {
	hub, wsURL := newTestHub(t, nil)
	ws := fakeExtension(t, wsURL, testToken)
	expectWelcome(t, ws)
	waitConnected(t, hub)

	// The extension answers the one call it receives.
	go func() {
		f, err := readFrame(t, ws)
		if err != nil || f.Type != "call" || f.Action != "navigate" {
			return
		}
		var p map[string]any
		_ = json.Unmarshal(f.Params, &p)
		res, _ := json.Marshal(map[string]any{"message": "Navigated to " + p["url"].(string)})
		_ = ws.WriteJSON(frame{Type: "result", ID: f.ID, OK: true, Result: res})
	}()

	out, err := hub.Call(context.Background(), "navigate", json.RawMessage(`{"url":"https://example.com"}`))
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !strings.Contains(string(out), "Navigated to https://example.com") {
		t.Fatalf("unexpected result %s", out)
	}

	st := hub.Status()
	if !st.Connected || st.Client != "test-ext/1" || st.Browser != "TestBrowser/1" {
		t.Fatalf("status should describe the extension, got %+v", st)
	}
}

func TestHubActionErrorIsReported(t *testing.T) {
	hub, wsURL := newTestHub(t, nil)
	ws := fakeExtension(t, wsURL, testToken)
	expectWelcome(t, ws)
	waitConnected(t, hub)

	go func() {
		f, err := readFrame(t, ws)
		if err != nil {
			return
		}
		_ = ws.WriteJSON(frame{Type: "result", ID: f.ID, OK: false, Error: "element n9 not found"})
	}()

	_, err := hub.Call(context.Background(), "click", json.RawMessage(`{"ref":"n9"}`))
	var ae *ActionError
	if !errors.As(err, &ae) {
		t.Fatalf("expected *ActionError, got %v", err)
	}
	if ae.Action != "click" || !strings.Contains(ae.Message, "n9") {
		t.Fatalf("action error lost detail: %+v", ae)
	}
}

func TestHubNoBrowser(t *testing.T) {
	hub, _ := newTestHub(t, nil)
	_, err := hub.Call(context.Background(), "snapshot", nil)
	if !errors.Is(err, ErrNoBrowser) {
		t.Fatalf("expected ErrNoBrowser, got %v", err)
	}
}

func TestHubCallTimesOut(t *testing.T) {
	hub, wsURL := newTestHub(t, func(o *Options) { o.CallTimeout = 50 * time.Millisecond })
	ws := fakeExtension(t, wsURL, testToken)
	expectWelcome(t, ws)
	waitConnected(t, hub)
	// The extension never answers.

	_, err := hub.Call(context.Background(), "snapshot", nil)
	if err == nil || !strings.Contains(err.Error(), "did not answer") {
		t.Fatalf("expected a timeout error, got %v", err)
	}
}

func TestHubWaitGetsItsOwnDuration(t *testing.T) {
	hub, _ := newTestHub(t, func(o *Options) { o.CallTimeout = time.Second })
	if got := hub.callTimeout("wait", json.RawMessage(`{"ms":1500}`)); got != 2500*time.Millisecond {
		t.Fatalf("wait timeout = %s, want 2.5s", got)
	}
	if got := hub.callTimeout("wait", json.RawMessage(`{"ms":9999999}`)); got != maxCallTimeout {
		t.Fatalf("wait timeout should be capped at %s, got %s", maxCallTimeout, got)
	}
	if got := hub.callTimeout("click", nil); got != time.Second {
		t.Fatalf("other actions keep the default, got %s", got)
	}
}

func TestHubNewerConnectionReplacesOlder(t *testing.T) {
	hub, wsURL := newTestHub(t, nil)
	first := fakeExtension(t, wsURL, testToken)
	expectWelcome(t, first)
	waitConnected(t, hub)

	second := fakeExtension(t, wsURL, testToken)
	expectWelcome(t, second)

	_, err := readFrame(t, first)
	if code := closeCode(err); code != closeReplaced {
		t.Fatalf("first connection should be closed with %d, got err=%v", closeReplaced, err)
	}

	// Calls now go to the second connection.
	go func() {
		f, err := readFrame(t, second)
		if err != nil {
			return
		}
		_ = second.WriteJSON(frame{Type: "result", ID: f.ID, OK: true, Result: json.RawMessage(`{"tabs":[]}`)})
	}()
	if _, err := hub.Call(context.Background(), "tabs", json.RawMessage(`{"action":"list"}`)); err != nil {
		t.Fatalf("call via second connection: %v", err)
	}
}

func TestHubPolicyAppliesBeforeTheBrowserSeesAnything(t *testing.T) {
	hub, wsURL := newTestHub(t, func(o *Options) {
		o.AllowEval = false
		o.BlockedHosts = []string{"*.bank.example"}
	})
	ws := fakeExtension(t, wsURL, testToken)
	expectWelcome(t, ws)
	waitConnected(t, hub)

	cases := []struct {
		action, params, want string
	}{
		{"eval", `{"expression":"1+1"}`, "disabled"},
		{"navigate", `{"url":"file:///etc/passwd"}`, "only http(s)"},
		{"navigate", `{"url":"javascript:alert(1)"}`, "only http(s)"},
		{"navigate", `{"url":"https://online.bank.example/login"}`, "blocked by config"},
		{"tabs", `{"action":"open","url":"chrome://settings"}`, "only http(s)"},
	}
	for _, c := range cases {
		_, err := hub.Call(context.Background(), c.action, json.RawMessage(c.params))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s %s: got %v, want error containing %q", c.action, c.params, err, c.want)
		}
	}

	// Nothing above reached the extension: the next frame it reads is the
	// legitimate call, not a leaked one.
	go func() {
		f, err := readFrame(t, ws)
		if err != nil || f.Action != "snapshot" {
			return
		}
		_ = ws.WriteJSON(frame{Type: "result", ID: f.ID, OK: true, Result: json.RawMessage(`{"nodes":[]}`)})
	}()
	if _, err := hub.Call(context.Background(), "snapshot", nil); err != nil {
		t.Fatalf("legitimate call after blocked ones: %v", err)
	}
}

func TestHubDisconnectFailsInFlightCall(t *testing.T) {
	hub, wsURL := newTestHub(t, func(o *Options) { o.CallTimeout = 5 * time.Second })
	ws := fakeExtension(t, wsURL, testToken)
	expectWelcome(t, ws)
	waitConnected(t, hub)

	go func() {
		if _, err := readFrame(t, ws); err == nil {
			ws.Close() // drop mid-call
		}
	}()

	_, err := hub.Call(context.Background(), "snapshot", nil)
	if !errors.Is(err, ErrDisconnected) {
		t.Fatalf("expected ErrDisconnected, got %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for hub.Connected() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if hub.Connected() {
		t.Fatal("hub still reports a browser after it dropped")
	}
}

func TestHubAnswersExtensionPings(t *testing.T) {
	hub, wsURL := newTestHub(t, nil)
	ws := fakeExtension(t, wsURL, testToken)
	expectWelcome(t, ws)
	waitConnected(t, hub)

	_ = ws.WriteJSON(frame{Type: "ping"})
	f, err := readFrame(t, ws)
	if err != nil || f.Type != "pong" {
		t.Fatalf("expected pong, got %+v (err %v)", f, err)
	}
}

// Status has to answer "whose browser is this, and has it done anything?".
// With two agents on one machine a bare "connected" is ambiguous, and it looks
// identical whether the agent is working or has never touched the browser.
func TestStatusNamesTheAgentAndItsActivity(t *testing.T) {
	hub, wsURL := newTestHub(t, nil)

	// Disconnected: the agent is still named, because "which agent am I
	// pairing with" is exactly the question at that moment.
	if st := hub.Status(); st.Connected || st.AgentID != "a1" || st.AgentName != "Agent One" {
		t.Fatalf("disconnected status should still name the agent, got %+v", st)
	}

	ws := fakeExtension(t, wsURL, testToken)
	expectWelcome(t, ws)
	waitConnected(t, hub)

	if st := hub.Status(); st.Actions != 0 || st.LastAction != "" {
		t.Fatalf("a fresh connection has run nothing, got %+v", st)
	}

	go func() {
		f, err := readFrame(t, ws)
		if err != nil {
			return
		}
		_ = ws.WriteJSON(frame{Type: "result", ID: f.ID, OK: true, Result: json.RawMessage(`{"nodes":[]}`)})
	}()
	if _, err := hub.Call(context.Background(), "snapshot", nil); err != nil {
		t.Fatal(err)
	}

	st := hub.Status()
	if st.Actions != 1 || st.LastAction != "snapshot" || st.LastActionAt == "" {
		t.Errorf("a successful action should be recorded, got %+v", st)
	}
	if st.LastError != "" {
		t.Errorf("a successful action must not leave an error, got %q", st.LastError)
	}
	if st.AgentID != "a1" || st.BrowserName != "TestBrowser/1" {
		t.Errorf("status should carry agent + browser identity, got %+v", st)
	}
}

// A failed action is still activity, and the reason is what the operator needs.
func TestStatusRecordsTheLastFailure(t *testing.T) {
	hub, wsURL := newTestHub(t, nil)
	ws := fakeExtension(t, wsURL, testToken)
	expectWelcome(t, ws)
	waitConnected(t, hub)

	go func() {
		f, err := readFrame(t, ws)
		if err != nil {
			return
		}
		_ = ws.WriteJSON(frame{Type: "result", ID: f.ID, OK: false, Error: "element n9 not found"})
	}()
	_, _ = hub.Call(context.Background(), "click", json.RawMessage(`{"ref":"n9"}`))

	st := hub.Status()
	if st.Actions != 1 || st.LastAction != "click" {
		t.Errorf("a failed action still counts, got %+v", st)
	}
	if !strings.Contains(st.LastError, "n9 not found") {
		t.Errorf("status should carry the failure reason, got %q", st.LastError)
	}
}

func TestDescribeUA(t *testing.T) {
	cases := []struct{ ua, want string }{
		{"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/149.0.0.0 Safari/537.36", "Chrome 149 on macOS"},
		{"Mozilla/5.0 (Windows NT 10.0; Win64; x64) Chrome/141.0.0.0 Safari/537.36 Edg/141.0.0.0", "Edge 141 on Windows"},
		{"Mozilla/5.0 (X11; Linux x86_64) Gecko/20100101 Firefox/133.0", "Firefox 133 on Linux"},
		{"", ""},
	}
	for _, c := range cases {
		if got := describeUA(c.ua); got != c.want {
			t.Errorf("describeUA(%.40q) = %q, want %q", c.ua, got, c.want)
		}
	}
	// An agent string nothing matches keeps its own words rather than being
	// guessed at.
	if got := describeUA("SomeBot/2.0"); got != "SomeBot/2.0" {
		t.Errorf("unknown UA should pass through, got %q", got)
	}
}
