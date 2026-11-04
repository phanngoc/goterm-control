package gateway

import (
	"encoding/json"
	"fmt"

	"github.com/ngocp/goterm-control/internal/coord"
)

// The dashboard's GitHub panel for a room: the repos its code lives in, one
// primary. Answered from the shared database by the dashboard itself, like
// the rest of a channel's settings. Linking is a record; cloning stays with
// the agents and `bomclaw ch repo add --clone`, which run where the code goes.

type repoParams struct {
	ChannelID string `json:"channel_id"`
	Repo      string `json:"repo"` // owner/name, a URL, or a cr_ id
	Path      string `json:"path"`
	Branch    string `json:"branch"`
	Label     string `json:"label"`
	Primary   bool   `json:"primary"`
}

func handleChannelRepos(deps Deps, method string, params json.RawMessage) (json.RawMessage, error) {
	if deps.Coord == nil {
		return nil, errNoCoord()
	}
	var p repoParams
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}
	if p.ChannelID == "" {
		return nil, fmt.Errorf("channel_id is required")
	}
	switch method {
	case "channels.repos":
	case "channels.repo_add":
		// The dashboard is a person at a browser.
		if _, err := deps.Coord.AddRepo(p.ChannelID, p.Repo, p.Path, p.Branch, p.Label, coord.OwnerUserID, p.Primary); err != nil {
			return nil, err
		}
	case "channels.repo_primary":
		if _, err := deps.Coord.SetPrimaryRepo(p.ChannelID, p.Repo); err != nil {
			return nil, err
		}
	case "channels.repo_remove":
		if err := deps.Coord.RemoveRepo(p.ChannelID, p.Repo); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unknown method: %s", method)
	}
	repos, err := deps.Coord.ListRepos(p.ChannelID)
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"repos": repos})
}
