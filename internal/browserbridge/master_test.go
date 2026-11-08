package browserbridge

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

// The extension the user installs connects with no token at all. On loopback,
// straight from the browser, that is the user's own machine and is let in.
func TestALoopbackExtensionNeedsNoToken(t *testing.T) {
	hub, wsURL := newTestHub(t, func(o *Options) { o.Token = "" })
	ws := fakeExtension(t, wsURL, "")
	expectWelcome(t, ws)
	waitConnected(t, hub)
}

// The same connection arriving through a tunnel or proxy is not the user's
// machine, whatever its socket address says — cloudflared dials from
// loopback too. It has to present the token.
func TestATunnelledExtensionStillNeedsTheToken(t *testing.T) {
	hub, wsURL := newTestHub(t, nil)
	hdr := http.Header{"X-Forwarded-For": {"203.0.113.9"}}
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, hdr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer ws.Close()
	_ = ws.WriteJSON(frame{Type: "hello", Client: "x"})
	if _, err := readFrame(t, ws); closeCode(err) != closeUnauthorized {
		t.Fatalf("a forwarded hello with no token got %v, want close %d", err, closeUnauthorized)
	}
	if hub.Connected() {
		t.Fatal("a forwarded, tokenless browser was attached")
	}

	ok, _, err := websocket.DefaultDialer.Dial(wsURL, hdr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer ok.Close()
	_ = ok.WriteJSON(frame{Type: "hello", Token: testToken, Client: "x"})
	expectWelcome(t, ok)
}

// A gateway that borrows the browser takes no extension of its own: two
// sockets for two agents is the setup the master replaced.
func TestABorrowingAgentSendsTheExtensionToTheMaster(t *testing.T) {
	_, wsURL := newTestHub(t, func(o *Options) { o.Master = "http://127.0.0.1:18789" })
	ws := fakeExtension(t, wsURL, "")
	if _, err := readFrame(t, ws); closeCode(err) != closeNotMaster {
		t.Fatalf("got %v, want close %d", err, closeNotMaster)
	}
}

// Agent 2's call reaches the one browser through agent 1, and arrives at the
// extension under agent 2's name — that name is what gives it its own tab
// group. The answer comes back to agent 2 unchanged.
func TestAnotherAgentsCallRunsThroughTheMasterUnderItsOwnName(t *testing.T) {
	master, _ := newTestHub(t, func(o *Options) { o.Token = "" })
	mux := http.NewServeMux()
	mux.Handle("/ext", master)
	mux.HandleFunc("/api/browser/call", func(w http.ResponseWriter, r *http.Request) {
		var req remoteRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		res, err := master.CallAs(r.Context(), req.Agent, req.Action, req.Params)
		w.Header().Set("Content-Type", "application/json")
		if err != nil {
			_ = json.NewEncoder(w).Encode(remoteResponse{Error: err.Error()})
			return
		}
		_ = json.NewEncoder(w).Encode(remoteResponse{OK: true, Result: res})
	})
	mux.HandleFunc("/api/browser/status", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(master.Status())
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ws := fakeExtension(t, "ws"+strings.TrimPrefix(srv.URL, "http")+"/ext", "")
	expectWelcome(t, ws)
	waitConnected(t, master)

	got := make(chan frame, 1)
	go func() {
		for {
			f, err := readFrame(t, ws)
			if err != nil {
				return
			}
			if f.Type == "call" {
				got <- f
				_ = ws.WriteJSON(frame{Type: "result", ID: f.ID, OK: true, Result: json.RawMessage(`{"message":"done"}`)})
				return
			}
		}
	}()

	agent2 := New(Options{AgentID: "bomclaw2", Master: srv.URL, CallTimeout: 2 * time.Second, Logf: func(string, ...any) {}})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := agent2.Call(ctx, "snapshot", nil)
	if err != nil {
		t.Fatalf("call through the master: %v", err)
	}
	if !strings.Contains(string(res), "done") {
		t.Errorf("result = %s", res)
	}
	f := <-got
	if f.Agent != "bomclaw2" {
		t.Errorf("the extension saw the call as %q, want bomclaw2", f.Agent)
	}

	// Its status is the master's browser, under its own name.
	st := agent2.Status()
	if !st.Connected || st.AgentID != "bomclaw2" || st.Master != srv.URL {
		t.Errorf("status = %+v, want the master's browser under bomclaw2's name", st)
	}
}
