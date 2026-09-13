package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/ngocp/goterm-control/internal/config"
)

// The settings screen: which backend this agent runs on, and changing it.
//
// Two things make this narrower than it looks. What is chosen is a MODEL —
// the model's API selects the backend, so picking the model and deriving the
// provider means they cannot be set to disagree. And the change takes effect
// by restarting this gateway, because the chat client is built once at
// startup; the screen says that out loud rather than looking instant and
// leaving the old backend running.

// AgentSettings is what one agent reports about itself.
type AgentSettings struct {
	AgentID    string               `json:"agent_id"`
	AgentName  string               `json:"agent_name"`
	Provider   string               `json:"provider"`
	Model      string               `json:"model"`
	ConfigPath string               `json:"config_path"`
	Choices    []config.ModelChoice `json:"choices"`
	CanRestart bool                 `json:"can_restart"`
	Busy       bool                 `json:"busy"` // a turn or task is running right now

	// Reachable says whether this row is an answer or a guess. A peer that is
	// down appears with the reason rather than being dropped: an agent missing
	// from the list reads as an agent that does not exist.
	Reachable bool   `json:"reachable"`
	Error     string `json:"error,omitempty"`
}

func handleAdminSettings(deps Deps) (json.RawMessage, error) {
	if deps.ConfigPath == "" {
		return nil, fmt.Errorf("this gateway does not know its own config path")
	}
	cfg, err := config.Load(deps.ConfigPath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", deps.ConfigPath, err)
	}
	busy := false
	if deps.Runs != nil {
		busy = len(deps.Runs()) > 0
	}
	return json.Marshal(AgentSettings{
		AgentID:    deps.AgentID,
		AgentName:  deps.AgentName,
		Provider:   cfg.Provider,
		Model:      cfg.Models.Default,
		ConfigPath: deps.ConfigPath,
		Choices:    cfg.KnownModels(),
		CanRestart: deps.Restart != nil,
		Busy:       busy,
	})
}

type setModelParams struct {
	Model string `json:"model"`
	// Force skips the busy check. The screen sets it only after the person has
	// been told a turn is running and said to go ahead.
	Force bool `json:"force,omitempty"`
}

func handleAdminSetModel(deps Deps, params json.RawMessage) (json.RawMessage, error) {
	if deps.ConfigPath == "" {
		return nil, fmt.Errorf("this gateway does not know its own config path")
	}
	var p setModelParams
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}
	if p.Model == "" {
		return nil, fmt.Errorf("model is required")
	}
	// Restarting kills whatever is mid-turn. That is a decision, not a
	// side-effect, so it is refused until someone makes it.
	if !p.Force && deps.Runs != nil {
		if n := len(deps.Runs()); n > 0 {
			return nil, fmt.Errorf("%d run(s) in flight — restarting now cancels them; retry with force to go ahead", n)
		}
	}
	if err := config.SetModel(deps.ConfigPath, p.Model); err != nil {
		return nil, err
	}
	log.Printf("settings: %s switched to model %s; restarting", deps.AgentID, p.Model)

	if deps.Restart == nil {
		return json.Marshal(map[string]any{
			"written": true, "restarted": false,
			"note": "config written, but this gateway has no service manager — restart it yourself for the change to take",
		})
	}
	// Answer before the restart: the reply cannot survive the process it is
	// being sent from.
	go func() {
		if err := deps.Restart(); err != nil {
			log.Printf("settings: restart failed: %v", err)
		}
	}()
	return json.Marshal(map[string]any{"written": true, "restarted": true})
}

// --- the other agents ------------------------------------------------------
//
// A gateway can only read its own config file, so a settings screen that
// showed one agent was telling the truth and being useless: the machine runs
// three, and the question "which backend is each on" is asked about all of
// them at once.
//
// The answer is the peers, over the same loopback HTTP the doorbell uses.
// Two agents on one machine are local to each other, which is why those routes
// are exempt from the dashboard login — and why this one carries no
// credentials of its own.

// handleAdminSettingsAll gathers this agent's settings and every peer's.
//
// A peer that is down, or too old to answer, appears with Reachable false
// rather than being left out: an agent missing from the list reads as an agent
// that does not exist.
func handleAdminSettingsAll(deps Deps) (json.RawMessage, error) {
	mine, err := handleAdminSettings(deps)
	if err != nil {
		return nil, err
	}
	var self AgentSettings
	if err := json.Unmarshal(mine, &self); err != nil {
		return nil, err
	}
	self.Reachable = true
	out := []AgentSettings{self}

	if deps.Coord != nil {
		agents, err := deps.Coord.ListAgents()
		if err != nil {
			return nil, err
		}
		client := &http.Client{Timeout: 5 * time.Second}
		for _, a := range agents {
			if a.ID == deps.AgentID {
				continue
			}
			out = append(out, peerSettings(client, a.ID, a.DisplayName, a.WSAddr))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AgentID < out[j].AgentID })
	return json.Marshal(out)
}

func peerSettings(client *http.Client, id, name, wsAddr string) AgentSettings {
	fail := func(err error) AgentSettings {
		return AgentSettings{AgentID: id, AgentName: name, Reachable: false, Error: err.Error()}
	}
	if client == nil {
		// A timeout is the one thing this call must not go without: a peer
		// that accepts the connection and never answers would otherwise hold
		// the settings screen open forever.
		client = &http.Client{Timeout: 5 * time.Second}
	}
	url, err := PeerURL(wsAddr, "/api/settings")
	if err != nil {
		return fail(err)
	}
	resp, err := client.Get(url)
	if err != nil {
		return fail(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fail(fmt.Errorf("HTTP %d", resp.StatusCode))
	}
	var s AgentSettings
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		return fail(err)
	}
	s.Reachable = true
	return s
}

// handleAdminSetModelOn routes a change to whichever agent owns that config.
func handleAdminSetModelOn(deps Deps, params json.RawMessage) (json.RawMessage, error) {
	var p struct {
		AgentID string `json:"agent_id"`
		setModelParams
	}
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}
	if p.AgentID == "" || p.AgentID == deps.AgentID {
		return handleAdminSetModel(deps, params)
	}
	if deps.Coord == nil {
		return nil, errNoCoord()
	}
	agents, err := deps.Coord.ListAgents()
	if err != nil {
		return nil, err
	}
	for _, a := range agents {
		if a.ID != p.AgentID {
			continue
		}
		url, err := PeerURL(a.WSAddr, "/api/settings/model")
		if err != nil {
			return nil, err
		}
		body, _ := json.Marshal(p.setModelParams)
		resp, err := (&http.Client{Timeout: 15 * time.Second}).Post(url, "application/json", bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("reach %s: %w", p.AgentID, err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			// The peer's own words: it knows why better than this does.
			return nil, fmt.Errorf("%s: %s", p.AgentID, strings.TrimSpace(string(raw)))
		}
		return raw, nil
	}
	return nil, fmt.Errorf("no agent %q is registered", p.AgentID)
}

// SettingsHandler is the loopback HTTP face of the two settings methods, for
// peers. Mounted behind the same rule as /api/status: a login, or a local
// caller — and a peer on this machine is local.
func SettingsHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var raw json.RawMessage
		var err error
		switch r.Method {
		case http.MethodGet:
			raw, err = handleAdminSettings(deps)
		case http.MethodPost:
			body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
			raw, err = handleAdminSetModel(deps, body)
		default:
			http.Error(w, `{"error":"GET or POST"}`, http.StatusMethodNotAllowed)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(raw)
	}
}
