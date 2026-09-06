package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPokeURLFromRegisteredAddress(t *testing.T) {
	cases := map[string]string{
		"ws://127.0.0.1:18789/ws":     "http://127.0.0.1:18789/api/tasks/poke",
		"ws://127.0.0.1:18790/ws?x=1": "http://127.0.0.1:18790/api/tasks/poke",
		"wss://bot.example.org/ws":    "https://bot.example.org/api/tasks/poke",
		"http://127.0.0.1:18789":      "http://127.0.0.1:18789/api/tasks/poke",
	}
	for in, want := range cases {
		got, err := PokeURL(in)
		if err != nil || got != want {
			t.Errorf("PokeURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "not a url", "ftp://x/ws"} {
		if _, err := PokeURL(bad); err == nil {
			t.Errorf("PokeURL(%q) should fail", bad)
		}
	}
}

// The doorbell must ring the local runner exactly once per POST, refuse other
// methods, and stay mountable when this agent does not claim tasks at all.
func TestPokeHandler(t *testing.T) {
	rang := 0
	h := PokeHandler(func() { rang++ })

	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodPost, "/api/tasks/poke", strings.NewReader("{}")))
	if rec.Code != http.StatusOK || rang != 1 || !strings.Contains(rec.Body.String(), `"poked":true`) {
		t.Fatalf("POST: code=%d rang=%d body=%s", rec.Code, rang, rec.Body)
	}

	rec = httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/api/tasks/poke", nil))
	if rec.Code != http.StatusMethodNotAllowed || rang != 1 {
		t.Fatalf("GET should be refused without ringing: code=%d rang=%d", rec.Code, rang)
	}

	// No runner (auto_claim off): still 200, so a peer never sees a 404 for ringing.
	rec = httptest.NewRecorder()
	PokeHandler(nil)(rec, httptest.NewRequest(http.MethodPost, "/api/tasks/poke", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("nil poke should still ack, got %d", rec.Code)
	}
}

// End to end over a real listener: PokeAgent given the ws address an agent
// registers must reach the HTTP handler.
func TestPokeAgentReachesHandlerOverHTTP(t *testing.T) {
	rang := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tasks/poke" {
			http.NotFound(w, r)
			return
		}
		PokeHandler(func() { rang <- struct{}{} })(w, r)
	}))
	defer srv.Close()

	wsAddr := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
	if err := PokeAgent(wsAddr); err != nil {
		t.Fatalf("PokeAgent(%s): %v", wsAddr, err)
	}
	select {
	case <-rang:
	default:
		t.Fatal("handler was not rung")
	}
}
