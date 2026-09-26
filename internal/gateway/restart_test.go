package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ngocp/goterm-control/internal/coord"
)

func restartDB(t *testing.T, peerAddr string) *coord.DB {
	t.Helper()
	db, err := coord.Open(filepath.Join(t.TempDir(), "coord.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.RegisterAgent(coord.Agent{ID: "bomclaw", DisplayName: "one", WSAddr: "ws://127.0.0.1:1/ws"}); err != nil {
		t.Fatal(err)
	}
	if err := db.RegisterAgent(coord.Agent{ID: "bomclaw2", DisplayName: "two", WSAddr: peerAddr}); err != nil {
		t.Fatal(err)
	}
	return db
}

// The button exists for the agent that is down, so the agent that is down has
// to be the case that works. Nothing answers on that port; the restart is
// carried out here, through the service manager.
func TestADeadPeerIsStartedFromHere(t *testing.T) {
	revived := ""
	deps := Deps{
		AgentID:     "bomclaw",
		Coord:       restartDB(t, "ws://127.0.0.1:1/ws"),
		ReviveAgent: func(id string) error { revived = id; return nil },
	}
	raw, err := handleAdminRestartOn(deps, json.RawMessage(`{"agent_id":"bomclaw2"}`))
	if err != nil {
		t.Fatalf("a down agent could not be restarted: %v", err)
	}
	if revived != "bomclaw2" {
		t.Fatalf("service manager was asked for %q, want bomclaw2", revived)
	}
	var got map[string]any
	json.Unmarshal(raw, &got)
	if got["how"] != "started" {
		t.Fatalf("a dead agent was reported as merely restarted: %v", got)
	}
}

// The rule that keeps the fallback honest. A peer that answers and says no is
// protecting a run in flight; reaching past that refusal into launchctl would
// kill the exact work it refused to lose. Only silence earns the fallback.
func TestAPeerThatRefusesIsNotKilledAnyway(t *testing.T) {
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "2 run(s) in flight — restarting now cancels them", http.StatusBadRequest)
	}))
	defer peer.Close()

	revived := ""
	deps := Deps{
		AgentID:     "bomclaw",
		Coord:       restartDB(t, "ws"+strings.TrimPrefix(peer.URL, "http")+"/ws"),
		ReviveAgent: func(id string) error { revived = id; return nil },
	}
	_, err := handleAdminRestartOn(deps, json.RawMessage(`{"agent_id":"bomclaw2"}`))
	if err == nil {
		t.Fatal("a refusal was reported as a successful restart")
	}
	if revived != "" {
		t.Fatalf("went around the refusal and restarted %q anyway", revived)
	}
	if !strings.Contains(err.Error(), "in flight") {
		t.Fatalf("the peer's own reason was lost: %v", err)
	}
}

// A live peer is asked rather than kicked: it is the only process that knows
// what it is doing right now.
func TestALivePeerIsAskedNotKicked(t *testing.T) {
	asked := false
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = r.Method == http.MethodPost && r.URL.Path == "/api/settings/restart"
		w.Write([]byte(`{"agent_id":"bomclaw2","restarted":true,"how":"asked"}`))
	}))
	defer peer.Close()

	revived := ""
	deps := Deps{
		AgentID:     "bomclaw",
		Coord:       restartDB(t, "ws"+strings.TrimPrefix(peer.URL, "http")+"/ws"),
		ReviveAgent: func(id string) error { revived = id; return nil },
	}
	if _, err := handleAdminRestartOn(deps, json.RawMessage(`{"agent_id":"bomclaw2"}`)); err != nil {
		t.Fatal(err)
	}
	if !asked {
		t.Fatal("the running peer was never asked")
	}
	if revived != "" {
		t.Fatalf("the service manager was used on a peer that was answering: %q", revived)
	}
}

// Restarting this gateway while it is mid-turn throws away live work, so it is
// a decision someone has to make twice, exactly as a model change is.
func TestRestartingYourselfMidTurnIsRefusedUntilForced(t *testing.T) {
	deps := Deps{
		AgentID: "bomclaw",
		Runs:    func() []RunInfo { return []RunInfo{{}} },
		Restart: func() error { return nil },
	}
	if _, err := handleAdminRestart(deps, json.RawMessage(`{}`)); err == nil {
		t.Fatal("a run in flight was cut without being mentioned")
	}
	if _, err := handleAdminRestart(deps, json.RawMessage(`{"force":true}`)); err != nil {
		t.Fatalf("force did not get through: %v", err)
	}
}

// A dead peer on a machine with no service manager must say so, not report a
// restart that never happened.
func TestNoServiceManagerIsSaidOutLoud(t *testing.T) {
	deps := Deps{AgentID: "bomclaw", Coord: restartDB(t, "ws://127.0.0.1:1/ws")}
	_, err := handleAdminRestartOn(deps, json.RawMessage(`{"agent_id":"bomclaw2"}`))
	if err == nil || !strings.Contains(err.Error(), "service manager") {
		t.Fatalf("expected an honest refusal, got %v", err)
	}
}
