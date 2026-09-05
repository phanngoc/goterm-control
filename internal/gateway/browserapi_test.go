package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/ngocp/goterm-control/internal/browserbridge"
)

// dialExtension connects to a hub the way the Chrome extension does and
// returns the socket once the welcome frame has landed.
func dialExtension(t *testing.T, hub *browserbridge.Hub, token string) *websocket.Conn {
	t.Helper()
	srv := httptest.NewServer(hub)
	t.Cleanup(srv.Close)

	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { ws.Close() })

	if err := ws.WriteJSON(map[string]any{
		"type": "hello", "token": token, "client": "test-ext", "browser": "TestBrowser",
	}); err != nil {
		t.Fatalf("hello: %v", err)
	}
	var welcome map[string]any
	_ = ws.SetReadDeadline(time.Now().Add(3 * time.Second))
	if err := ws.ReadJSON(&welcome); err != nil || welcome["type"] != "welcome" {
		t.Fatalf("expected welcome, got %v (err %v)", welcome, err)
	}
	_ = ws.SetReadDeadline(time.Time{})

	deadline := time.Now().Add(2 * time.Second)
	for !hub.Connected() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	return ws
}

// answerOnce replies to the next call frame with a fixed successful result.
func answerOnce(t *testing.T, ws *websocket.Conn, result string) {
	t.Helper()
	go func() {
		var f struct {
			ID     string `json:"id"`
			Action string `json:"action"`
		}
		_ = ws.SetReadDeadline(time.Now().Add(3 * time.Second))
		if err := ws.ReadJSON(&f); err != nil {
			return
		}
		_ = ws.WriteJSON(map[string]any{
			"type": "result", "id": f.ID, "ok": true, "result": json.RawMessage(result),
		})
	}()
}

func newTestHub(t *testing.T) *browserbridge.Hub {
	t.Helper()
	return browserbridge.New(browserbridge.Options{
		Token: "tok", AgentID: "a1", AgentName: "Agent One",
		CallTimeout: 2 * time.Second, AllowEval: true,
		Logf: func(string, ...any) {},
	})
}

func TestBrowserAPICallRelaysResult(t *testing.T) {
	hub := newTestHub(t)
	ws := dialExtension(t, hub, "tok")
	api := &BrowserAPI{Hub: hub, Token: "tok", ExtURL: "ws://127.0.0.1:18789/ext"}

	answerOnce(t, ws, `{"message":"Clicked n7"}`)

	req := httptest.NewRequest(http.MethodPost, "/api/browser/call",
		strings.NewReader(`{"action":"click","params":{"ref":"n7"}}`))
	rec := httptest.NewRecorder()
	api.HandleCall(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	var out struct {
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.OK || !strings.Contains(string(out.Result), "Clicked n7") {
		t.Fatalf("unexpected body %s", rec.Body)
	}
}

// A missing browser must be distinguishable from a failed action: the CLI
// turns 503 into exit code 3, which is what tells the agent to ask the user
// to connect the extension rather than to retry the action.
func TestBrowserAPINoBrowserIs503(t *testing.T) {
	api := &BrowserAPI{Hub: newTestHub(t)}

	req := httptest.NewRequest(http.MethodPost, "/api/browser/call",
		strings.NewReader(`{"action":"snapshot"}`))
	rec := httptest.NewRecorder()
	api.HandleCall(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "no browser is connected") {
		t.Fatalf("body should say what to do: %s", rec.Body)
	}
}

// An action the browser itself refused is a 200 with ok:false — the request
// was fine, and the answer is the useful part.
func TestBrowserAPIActionFailureIs200(t *testing.T) {
	hub := newTestHub(t)
	ws := dialExtension(t, hub, "tok")
	api := &BrowserAPI{Hub: hub}

	go func() {
		var f struct {
			ID string `json:"id"`
		}
		_ = ws.SetReadDeadline(time.Now().Add(3 * time.Second))
		if err := ws.ReadJSON(&f); err != nil {
			return
		}
		_ = ws.WriteJSON(map[string]any{
			"type": "result", "id": f.ID, "ok": false, "error": "element n9 not found",
		})
	}()

	req := httptest.NewRequest(http.MethodPost, "/api/browser/call",
		strings.NewReader(`{"action":"click","params":{"ref":"n9"}}`))
	rec := httptest.NewRecorder()
	api.HandleCall(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "n9 not found") {
		t.Fatalf("body lost the reason: %s", rec.Body)
	}
}

func TestBrowserAPIEvalDisabledIs403(t *testing.T) {
	hub := browserbridge.New(browserbridge.Options{
		Token: "tok", AllowEval: false, CallTimeout: time.Second,
		Logf: func(string, ...any) {},
	})
	dialExtension(t, hub, "tok")
	api := &BrowserAPI{Hub: hub}

	req := httptest.NewRequest(http.MethodPost, "/api/browser/call",
		strings.NewReader(`{"action":"eval","params":{"expression":"1+1"}}`))
	rec := httptest.NewRecorder()
	api.HandleCall(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: %s", rec.Code, rec.Body)
	}
}

func TestBrowserAPIRejectsNonPost(t *testing.T) {
	api := &BrowserAPI{Hub: newTestHub(t)}
	rec := httptest.NewRecorder()
	api.HandleCall(rec, httptest.NewRequest(http.MethodGet, "/api/browser/call", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

func TestBrowserAPIStatusAndToken(t *testing.T) {
	hub := newTestHub(t)
	api := &BrowserAPI{Hub: hub, Token: "tok", ExtURL: "ws://127.0.0.1:18789/ext"}

	rec := httptest.NewRecorder()
	api.HandleStatus(rec, httptest.NewRequest(http.MethodGet, "/api/browser/status", nil))
	var st browserbridge.Status
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if st.Connected {
		t.Fatal("no extension is attached, status should say so")
	}

	dialExtension(t, hub, "tok")
	rec = httptest.NewRecorder()
	api.HandleStatus(rec, httptest.NewRequest(http.MethodGet, "/api/browser/status", nil))
	_ = json.Unmarshal(rec.Body.Bytes(), &st)
	if !st.Connected || st.Client != "test-ext" {
		t.Fatalf("status should describe the extension, got %+v", st)
	}

	rec = httptest.NewRecorder()
	api.HandleToken(rec, httptest.NewRequest(http.MethodGet, "/api/browser/token", nil))
	var tok struct{ Token, URL string }
	_ = json.Unmarshal(rec.Body.Bytes(), &tok)
	if tok.Token != "tok" || tok.URL != "ws://127.0.0.1:18789/ext" {
		t.Fatalf("token payload = %+v", tok)
	}
}

// browser.call is the same relay over the dashboard's WebSocket RPC, so the
// dashboard and the CLI cannot drift apart.
func TestBrowserCallRPC(t *testing.T) {
	hub := newTestHub(t)
	ws := dialExtension(t, hub, "tok")
	answerOnce(t, ws, `{"tabs":[]}`)

	handler := NewMethodHandler(Deps{Browser: hub})
	out, err := handler(context.Background(), "browser.call",
		json.RawMessage(`{"action":"tabs","params":{"action":"list"}}`))
	if err != nil {
		t.Fatalf("browser.call: %v", err)
	}
	if !strings.Contains(string(out), `"tabs"`) {
		t.Fatalf("unexpected result %s", out)
	}
}

func TestBrowserCallRPCDisabled(t *testing.T) {
	handler := NewMethodHandler(Deps{})
	_, err := handler(context.Background(), "browser.call", json.RawMessage(`{"action":"tabs"}`))
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("expected a 'disabled' error, got %v", err)
	}
}

// The extra-route hook is what mounts /ext and /api/browser/*. The dashboard
// is served from a "/" catch-all, so this proves the specific patterns win
// over it rather than being swallowed and served index.html.
func TestServerHandleMountsExtraRoutesOverDashboardCatchAll(t *testing.T) {
	dashboard := t.TempDir()
	if err := os.WriteFile(filepath.Join(dashboard, "index.html"), []byte("<html>dashboard</html>"), 0o644); err != nil {
		t.Fatal(err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	srv := NewServer(addr, nil, nil, dashboard, nil)
	srv.Handle("/api/browser/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"connected":false}`))
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Start(ctx) }()

	base := "http://" + addr
	waitForServer(t, base+"/health")

	resp, err := http.Get(base + "/api/browser/status")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"connected"`) {
		t.Fatalf("the dashboard catch-all swallowed the route: %s", body)
	}
}

func waitForServer(t *testing.T, url string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			resp.Body.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("server never came up at %s", url)
}
