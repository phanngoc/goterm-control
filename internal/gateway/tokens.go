package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/ngocp/goterm-control/internal/credentials"
)

// The token store, from the dashboard: add, edit, rename and delete the
// tokens and API keys each provider's agents rotate through.
//
// Answered by the dashboard process, which writes the file every gateway
// re-reads before its next turn. A stored value never leaves the server:
// listings carry a mask, and an edit that sends no value keeps the old one.

// TokenView is one stored credential as the dashboard sees it.
type TokenView struct {
	Name    string                `json:"name"`
	Kind    credentials.TokenKind `json:"kind"`
	Masked  string                `json:"masked"`
	Rotate  bool                  `json:"rotate"`
	Note    string                `json:"note,omitempty"`
	Added   string                `json:"added"`
	Updated string                `json:"updated"`
	// Live is how each agent running this provider is using it right now.
	Live []TokenLive `json:"live,omitempty"`
}

// TokenLive is one agent's view of one account, from its /api/status.
type TokenLive struct {
	AgentID  string `json:"agent_id"`
	Current  bool   `json:"current,omitempty"`
	Uses     int    `json:"uses,omitempty"`
	Turns    int    `json:"turns"`
	Cooling  string `json:"cooling_until,omitempty"`
	LastErr  string `json:"last_error,omitempty"`
	Sessions int    `json:"sessions,omitempty"`
}

// ProviderView is one provider's section.
type ProviderView struct {
	Provider    string      `json:"provider"`
	RotateEvery int         `json:"rotate_every"`
	Tokens      []TokenView `json:"tokens"`
	// Agents lists the agents running this provider, so the page can say who
	// a token will serve. Other is every account those agents have from
	// config.yaml (logins, env keys), which the store does not manage.
	Agents []string    `json:"agents"`
	Other  []TokenLive `json:"other,omitempty"`
	// OtherNames parallels Other.
	OtherNames []string `json:"other_names,omitempty"`
}

func tokenStore(deps Deps) (*credentials.TokenStore, error) {
	if deps.Coord == nil {
		return nil, errNoCoord()
	}
	return credentials.NewTokenStore(credentials.TokenStorePath(deps.Coord.Path())), nil
}

func handleTokensList(deps Deps, _ json.RawMessage) (json.RawMessage, error) {
	store, err := tokenStore(deps)
	if err != nil {
		return nil, err
	}
	all, err := store.Load()
	if err != nil {
		return nil, err
	}
	agents, _ := deps.Coord.ListAgents()

	// Every online agent's accounts, fetched in parallel over loopback.
	type agentStatus struct {
		id, provider string
		accounts     []credentials.Status
	}
	var mu sync.Mutex
	var statuses []agentStatus
	var wg sync.WaitGroup
	for _, a := range agents {
		if !a.Online || a.WSAddr == "" {
			continue
		}
		wg.Add(1)
		go func(id, provider, addr string) {
			defer wg.Done()
			url, err := PeerURL(addr, "/api/status")
			if err != nil {
				return
			}
			resp, err := (&http.Client{Timeout: 2 * time.Second}).Get(url)
			if err != nil {
				return
			}
			defer resp.Body.Close()
			var st StatusResult
			if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&st) != nil {
				return
			}
			mu.Lock()
			statuses = append(statuses, agentStatus{id, provider, st.Accounts})
			mu.Unlock()
		}(a.ID, a.Provider, a.WSAddr)
	}
	wg.Wait()
	sort.Slice(statuses, func(i, j int) bool { return statuses[i].id < statuses[j].id })

	out := []ProviderView{}
	for _, provider := range []string{credentials.ProviderClaude, credentials.ProviderCodex} {
		pv := ProviderView{Provider: provider, Tokens: []TokenView{}, Agents: []string{}}
		for _, a := range agents {
			if a.Provider == provider {
				pv.Agents = append(pv.Agents, a.ID)
			}
		}
		managed := map[string]bool{}
		if pt := all[provider]; pt != nil {
			pv.RotateEvery = pt.RotateEvery
			for _, t := range pt.Tokens {
				managed[t.Name] = true
				tv := TokenView{Name: t.Name, Kind: t.Kind, Masked: credentials.Mask(t.Value), Rotate: t.Rotate,
					Note: t.Note, Added: t.Added.Format(time.RFC3339), Updated: t.Updated.Format(time.RFC3339)}
				for _, st := range statuses {
					for _, acc := range st.accounts {
						if acc.Provider == provider && acc.Name == t.Name {
							tv.Live = append(tv.Live, live(st.id, acc))
						}
					}
				}
				pv.Tokens = append(pv.Tokens, tv)
			}
		}
		for _, st := range statuses {
			for _, acc := range st.accounts {
				if acc.Provider == provider && !managed[acc.Name] {
					pv.Other = append(pv.Other, live(st.id, acc))
					pv.OtherNames = append(pv.OtherNames, acc.Name+" ("+acc.Kind+")")
				}
			}
		}
		out = append(out, pv)
	}
	return json.Marshal(out)
}

func live(agentID string, s credentials.Status) TokenLive {
	return TokenLive{AgentID: agentID, Current: s.Current, Uses: s.Uses, Turns: s.Turns,
		Cooling: s.CoolingHint, LastErr: s.LastError, Sessions: s.Sessions}
}

type tokenParams struct {
	credentials.TokenInput
	RotateEvery *int `json:"rotate_every,omitempty"`
}

// handleTokensMutate answers tokens.save, tokens.delete and tokens.settings,
// and returns the listing as it now stands.
func handleTokensMutate(deps Deps, method string, params json.RawMessage) (json.RawMessage, error) {
	store, err := tokenStore(deps)
	if err != nil {
		return nil, err
	}
	var p tokenParams
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}
	switch method {
	case "tokens.save":
		err = store.Save(p.TokenInput)
	case "tokens.delete":
		err = store.Delete(p.Provider, p.Name)
	case "tokens.settings":
		if p.RotateEvery == nil {
			return nil, fmt.Errorf("rotate_every is required")
		}
		err = store.SetRotateEvery(p.Provider, *p.RotateEvery)
	default:
		return nil, fmt.Errorf("unknown method %s", method)
	}
	if err != nil {
		return nil, err
	}
	return handleTokensList(deps, nil)
}
