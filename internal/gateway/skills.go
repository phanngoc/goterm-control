package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/ngocp/goterm-control/internal/coord"
	"github.com/ngocp/goterm-control/internal/skills"
)

// The skills hub: one screen where an agent's toolkit can be read, given,
// taken away, and carried to a peer.
//
// It reads and writes the workspaces directly rather than asking each agent's
// gateway over loopback, the way the settings screen does. Skills are plain
// files, on one machine, owned by one user — and doing it here means the hub
// still works on an agent whose gateway is down, which is exactly when someone
// is likely to be looking at what it was given.

// AgentSkills is one agent's toolkit as it stands.
type AgentSkills struct {
	AgentID   string         `json:"agent_id"`
	Workspace string         `json:"workspace"`
	Skills    []HubSkill     `json:"skills"`
	Missing   []string       `json:"missing,omitempty"` // bundled defaults this agent does not have
	Error     string         `json:"error,omitempty"`
	Online    bool           `json:"online"`
	Provider  string         `json:"provider,omitempty"`
	_         map[string]any `json:"-"`
}

// HubSkill is a skill plus what the hub needs to say about it.
type HubSkill struct {
	skills.Skill
	// Bundled says this name is one of the defaults every agent is seeded
	// with, so the hub can offer to put it back after a removal.
	Bundled bool `json:"bundled"`
	// Edited says the agent's copy no longer matches what it was seeded with.
	// It is the signal the whole per-agent design exists for: it marks where an
	// agent has learned something its peers have not.
	Edited bool `json:"edited"`
}

func handleSkillsList(deps Deps, params json.RawMessage) (json.RawMessage, error) {
	if deps.Coord == nil {
		return nil, errNoCoord()
	}
	agents, err := deps.Coord.ListAgents()
	if err != nil {
		return nil, err
	}
	bundled := map[string][]byte{}
	for _, n := range skills.DefaultNames() {
		if b, err := skills.Default(n); err == nil {
			bundled[n] = b
		}
	}

	out := make([]AgentSkills, 0, len(agents))
	for _, a := range agents {
		row := AgentSkills{
			AgentID: a.ID, Workspace: a.Workspace,
			Online: a.Online, Provider: a.Provider,
			Skills: []HubSkill{},
		}
		list, err := skills.Load(a.Workspace)
		if err != nil {
			// Reported per agent, not as a failed call: one unreadable
			// workspace must not blank the screen for the other two.
			row.Error = err.Error()
			out = append(out, row)
			continue
		}
		has := map[string]bool{}
		for _, s := range list {
			has[s.Name] = true
			h := HubSkill{Skill: s}
			if orig, ok := bundled[s.Name]; ok {
				h.Bundled = true
				if body, err := skills.Read(a.Workspace, s.Name); err == nil {
					h.Edited = !bytes.Equal(bytes.TrimSpace(body), bytes.TrimSpace(orig))
				}
			}
			row.Skills = append(row.Skills, h)
		}
		for n := range bundled {
			if !has[n] {
				row.Missing = append(row.Missing, n)
			}
		}
		out = append(out, row)
	}
	return json.Marshal(out)
}

type skillParams struct {
	AgentID string `json:"agent_id"`
	Name    string `json:"name"`
	Body    string `json:"body,omitempty"`
	// From is the agent whose version to copy, for skills.copy.
	From string `json:"from,omitempty"`
	// Bundled asks for the shipped version rather than an agent's, so the hub
	// can put back a default somebody removed, and show what the original said
	// beside an edited copy.
	Bundled bool `json:"bundled,omitempty"`
}

// workspaceOf resolves an agent id to the folder its skills live in.
func workspaceOf(deps Deps, agentID string) (string, error) {
	if agentID == "" {
		return "", fmt.Errorf("agent_id is required")
	}
	agents, err := deps.Coord.ListAgents()
	if err != nil {
		return "", err
	}
	for _, a := range agents {
		if a.ID == agentID {
			if a.Workspace == "" {
				return "", fmt.Errorf("%s has no workspace registered — it has not "+
					"started since workspaces were recorded", agentID)
			}
			return a.Workspace, nil
		}
	}
	return "", fmt.Errorf("no agent %q", agentID)
}

func handleSkillGet(deps Deps, params json.RawMessage) (json.RawMessage, error) {
	if deps.Coord == nil {
		return nil, errNoCoord()
	}
	var p skillParams
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}
	if p.Bundled {
		body, err := skills.Default(p.Name)
		if err != nil {
			return nil, err
		}
		return json.Marshal(map[string]any{"name": p.Name, "body": string(body), "bundled": true})
	}
	ws, err := workspaceOf(deps, p.AgentID)
	if err != nil {
		return nil, err
	}
	body, err := skills.Read(ws, p.Name)
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"agent_id": p.AgentID, "name": p.Name, "body": string(body)})
}

// handleSkillInstall gives an agent a skill, or replaces the one it has.
//
// Replacing rather than refusing: the two real uses are "give this agent the
// skill" and "give it the version its peer improved", and both are one write.
// What protects an agent's own revisions is that nothing does this on a timer —
// the startup seed never overwrites, and this only runs when somebody asked.
func handleSkillInstall(deps Deps, params json.RawMessage) (json.RawMessage, error) {
	if deps.Coord == nil {
		return nil, errNoCoord()
	}
	var p skillParams
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}
	ws, err := workspaceOf(deps, p.AgentID)
	if err != nil {
		return nil, err
	}
	body := []byte(p.Body)
	if len(body) == 0 {
		// No body means "the shipped one" — how a removed default is put back.
		if body, err = skills.Default(p.Name); err != nil {
			return nil, err
		}
	}
	if err := skills.Install(ws, p.Name, body); err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"ok": true, "agent_id": p.AgentID, "name": p.Name})
}

func handleSkillRemove(deps Deps, params json.RawMessage) (json.RawMessage, error) {
	if deps.Coord == nil {
		return nil, errNoCoord()
	}
	var p skillParams
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}
	ws, err := workspaceOf(deps, p.AgentID)
	if err != nil {
		return nil, err
	}
	if err := skills.Remove(ws, p.Name); err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"ok": true, "agent_id": p.AgentID, "name": p.Name})
}

// handleSkillCopy carries one agent's version of a skill to another.
//
// This is the operation that makes per-agent copies worth having. Without it
// each agent improves alone and whatever it learns dies with it — three loops
// running in parallel instead of one that accumulates.
func handleSkillCopy(deps Deps, params json.RawMessage) (json.RawMessage, error) {
	if deps.Coord == nil {
		return nil, errNoCoord()
	}
	var p skillParams
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}
	from, err := workspaceOf(deps, p.From)
	if err != nil {
		return nil, err
	}
	to, err := workspaceOf(deps, p.AgentID)
	if err != nil {
		return nil, err
	}
	if err := skills.Copy(from, to, p.Name); err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"ok": true, "from": p.From, "agent_id": p.AgentID, "name": p.Name})
}

// agentWorkspaces is a small helper for tests and for callers that want the
// map rather than one lookup at a time.
func agentWorkspaces(agents []coord.Agent) map[string]string {
	out := make(map[string]string, len(agents))
	for _, a := range agents {
		out[a.ID] = a.Workspace
	}
	return out
}
