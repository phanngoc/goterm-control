package gateway

import (
	"encoding/json"
	"fmt"
	"log"

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
