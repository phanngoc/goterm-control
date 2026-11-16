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
	Name   string                `json:"name"`
	Kind   credentials.TokenKind `json:"kind"`
	Masked string                `json:"masked"`
	Rotate bool                  `json:"rotate"`
	Note   string                `json:"note,omitempty"`
	// ConfigDir is a login's directory; Agents the agents it is limited to
	// (empty: all). LastUsed is its newest turn on any agent.
	ConfigDir string   `json:"config_dir,omitempty"`
	Agents    []string `json:"agents,omitempty"`
	LastUsed  string   `json:"last_used,omitempty"`
	Added     string   `json:"added"`
	Updated   string   `json:"updated"`
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

	var lastUsed map[string]time.Time
	if sh, err := credentials.OpenShared(credentials.SharedStatePath(deps.Coord.Path()), ""); err == nil {
		lastUsed = sh.LastUsed()
		_ = sh.Close()
	}

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
					Note: t.Note, Added: t.Added.Format(time.RFC3339), Updated: t.Updated.Format(time.RFC3339),
					ConfigDir: t.ConfigDir, Agents: t.Agents}
				if t.Kind == credentials.KindLogin {
					tv.Masked = t.ConfigDir
				}
				if at, ok := lastUsed[t.Account(provider).Key()]; ok {
					tv.LastUsed = at.Format(time.RFC3339)
				}
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

// TokenUsage is the turns each stored credential carried over time, per agent
// — kept for 30 days in the shared state, so a token's history survives the
// restarts that reset the live counters.
type TokenUsage struct {
	Provider string      `json:"provider"`
	Bucket   int64       `json:"bucket_seconds"`
	Buckets  []int64     `json:"buckets"` // unix seconds, oldest first
	Agents   []string    `json:"agents"`
	Tokens   []TokenLine `json:"tokens"`
}

// TokenLine is one credential's series.
type TokenLine struct {
	Name    string           `json:"name"`
	Kind    string           `json:"kind"`
	Total   int              `json:"total"`
	ByAgent map[string][]int `json:"by_agent"` // parallel to Buckets
}

func handleTokensUsage(deps Deps, params json.RawMessage) (json.RawMessage, error) {
	var p struct {
		Provider string `json:"provider"`
		Days     int    `json:"days"`
	}
	_ = decodeParams(params, &p)
	if p.Provider == "" {
		p.Provider = credentials.ProviderClaude
	}
	if p.Days <= 0 || p.Days > 30 {
		p.Days = 7
	}
	store, err := tokenStore(deps)
	if err != nil {
		return nil, err
	}
	all, err := store.Load()
	if err != nil {
		return nil, err
	}
	sh, err := credentials.OpenShared(credentials.SharedStatePath(deps.Coord.Path()), "")
	if err != nil {
		return nil, err
	}
	defer sh.Close()

	// Hours for a day or two, days beyond — aligned to local midnight.
	bucket := time.Hour
	if p.Days > 2 {
		bucket = 24 * time.Hour
	}
	now := time.Now()
	_, off := now.Zone()
	b := int64(bucket / time.Second)
	end := ((now.Unix()+int64(off))/b)*b - int64(off)
	n := int64(p.Days) * 24 * 3600 / b
	start := end - (n-1)*b
	pts, err := sh.History(time.Unix(start, 0), bucket, int64(off))
	if err != nil {
		return nil, err
	}
	u := TokenUsage{Provider: p.Provider, Bucket: b, Tokens: []TokenLine{}}
	for t := start; t <= end; t += b {
		u.Buckets = append(u.Buckets, t)
	}
	agentSet := map[string]bool{}
	byKey := map[string]*TokenLine{}
	var lines []*TokenLine
	if pt := all[p.Provider]; pt != nil {
		for _, t := range pt.Tokens {
			line := &TokenLine{Name: t.Name, Kind: string(t.Kind), ByAgent: map[string][]int{}}
			lines = append(lines, line)
			byKey[t.Account(p.Provider).Key()] = line
		}
	}
	for _, pt := range pts {
		line := byKey[pt.Key]
		if line == nil {
			continue
		}
		i := (pt.Bucket - start) / b
		if i < 0 || i >= int64(len(u.Buckets)) {
			continue
		}
		agent := pt.Agent
		if agent == "" {
			agent = "?"
		}
		if line.ByAgent[agent] == nil {
			line.ByAgent[agent] = make([]int, len(u.Buckets))
		}
		line.ByAgent[agent][i] += pt.Turns
		line.Total += pt.Turns
		agentSet[agent] = true
	}
	for _, l := range lines {
		u.Tokens = append(u.Tokens, *l)
	}
	for a := range agentSet {
		u.Agents = append(u.Agents, a)
	}
	sort.Strings(u.Agents)
	return json.Marshal(u)
}
