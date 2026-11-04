package gateway

import (
	"encoding/json"
	"fmt"
	"time"
)

// economics.get is the dashboard's Economics page: the same report as
// `bomclaw econ`, from the shared database, so the dashboard answers it itself.
func handleEconomics(deps Deps, params json.RawMessage) (json.RawMessage, error) {
	if deps.Coord == nil {
		return nil, errNoCoord()
	}
	var p struct {
		Days int `json:"days"`
	}
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
	}
	if p.Days <= 0 || p.Days > 365 {
		p.Days = 7
	}
	e, err := deps.Coord.EconomicsSince(time.Now().AddDate(0, 0, -p.Days))
	if err != nil {
		return nil, err
	}
	return json.Marshal(e)
}

// economics.judge sets (or, with "", clears) the agent that grades finished
// goals. Only a known agent can be named.
func handleEconomicsJudge(deps Deps, params json.RawMessage) (json.RawMessage, error) {
	if deps.Coord == nil {
		return nil, errNoCoord()
	}
	var p struct {
		AgentID string `json:"agent_id"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if p.AgentID != "" {
		agents, err := deps.Coord.ListAgents()
		if err != nil {
			return nil, err
		}
		known := false
		for _, a := range agents {
			known = known || a.ID == p.AgentID
		}
		if !known {
			return nil, fmt.Errorf("no agent %q", p.AgentID)
		}
	}
	if err := deps.Coord.SetJudge(p.AgentID); err != nil {
		return nil, err
	}
	return json.Marshal(map[string]string{"judge": p.AgentID})
}
