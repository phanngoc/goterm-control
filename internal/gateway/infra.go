package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ngocp/goterm-control/internal/infra"
)

// The infra hub: what runs on this machine — pm2 apps by project, the
// LaunchAgents, every listening port and who answers for it — and the few
// actions an owner takes on them. Answered by the dashboard process itself
// (see DetachedMethods), so it works while the agents it can restart are down.

// newInfra is a seam for tests.
var newInfra = func(deps Deps) *infra.Infra {
	in := infra.New()
	in.Projects = map[string]string{}
	if deps.Coord != nil {
		if rooms, err := deps.Coord.ListChannels("", ""); err == nil {
			for _, c := range rooms {
				if c.Workspace != "" {
					in.Projects[c.Workspace] = c.Name
				}
			}
		}
	}
	return in
}

func handleInfraSnapshot(deps Deps, _ json.RawMessage) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return json.Marshal(newInfra(deps).Snapshot(ctx))
}

type infraParams struct {
	Kind   string `json:"kind"`
	Target string `json:"target"`
	Verb   string `json:"verb"`
	Lines  int    `json:"lines,omitempty"`
}

func handleInfraAction(deps Deps, params json.RawMessage) (json.RawMessage, error) {
	var p infraParams
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}
	if p.Kind == "" || p.Target == "" || p.Verb == "" {
		return nil, fmt.Errorf("kind, target and verb are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	r, err := newInfra(deps).Do(ctx, infra.Action{Kind: p.Kind, Target: p.Target, Verb: p.Verb})
	if err != nil {
		return nil, err
	}
	return json.Marshal(r)
}

func handleInfraLogs(deps Deps, params json.RawMessage) (json.RawMessage, error) {
	var p infraParams
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	logs, err := newInfra(deps).Logs(ctx, p.Kind, p.Target, p.Lines)
	if err != nil {
		return nil, err
	}
	return json.Marshal(logs)
}
