package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/ngocp/goterm-control/internal/deploy"
)

// The infrastructure behind every project, by provider, for Admin → Infra:
// is each provider connected, whose account and which plan, what the account
// is doing (requests, errors, databases), and which projects live on it.
//
// A room shows its own environments — what is deployed, which version, how
// it is doing. The account a project happens to run in, its plan and its
// traffic are facts about the provider, shared by every project on it, so
// they live here once instead of in every room.

// CloudProvider is one provider as the Infra page shows it.
type CloudProvider struct {
	Name  string `json:"name"`
	Title string `json:"title"`
	// Built is false for a provider that is known and not supported yet
	// (planned): it is listed so the page answers "what about AWS?".
	Built     bool            `json:"built"`
	Connected bool            `json:"connected"`
	Hint      string          `json:"hint,omitempty"`
	Account   *deploy.Account `json:"account,omitempty"`
	Metrics   *deploy.Metrics `json:"metrics,omitempty"`
	// Projects are the rooms whose PROD lives on this provider.
	Projects []CloudProject `json:"projects"`
}

// CloudProject is one project on a provider.
type CloudProject struct {
	ChannelID string             `json:"channel_id"`
	Name      string             `json:"name"`
	Declared  bool               `json:"declared"`
	DevAhead  int                `json:"dev_ahead"`
	Running   bool               `json:"running"`
	Prod      deploy.ProdStatus  `json:"prod"`
	Git       *deploy.GitVersion `json:"git,omitempty"`
	Error     string             `json:"error,omitempty"`
}

// CloudStatus is every provider.
type CloudStatus struct {
	At        string          `json:"at"`
	Window    string          `json:"window"`
	Providers []CloudProvider `json:"providers"`
}

const cloudWindow = 24 * time.Hour

// cloudTTL is how long a full answer is reused: it asks every project's
// provider, and the page polls.
const cloudTTL = 45 * time.Second

type cloudCache struct {
	mu  sync.Mutex
	at  time.Time
	val *CloudStatus
}

var cloudCached cloudCache

// Cloud reports every provider, the projects on each and the account's usage.
// It never changes anything. fresh skips the short cache.
func (m *EnvManager) Cloud(ctx context.Context, fresh bool) (*CloudStatus, error) {
	cloudCached.mu.Lock()
	if !fresh && cloudCached.val != nil && time.Since(cloudCached.at) < cloudTTL {
		v := cloudCached.val
		cloudCached.mu.Unlock()
		return v, nil
	}
	cloudCached.mu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	out := &CloudStatus{At: time.Now().Format(time.RFC3339), Window: cloudWindow.String()}
	byName := map[string]*CloudProvider{}
	var order []string
	for _, name := range deploy.Available() {
		prov, err := m.Provider(name)
		if err != nil {
			continue
		}
		ok, hint := prov.Connected()
		cp := &CloudProvider{Name: name, Title: prov.Title(), Built: true, Connected: ok, Projects: []CloudProject{}}
		if !ok {
			cp.Hint = hint
		}
		byName[name] = cp
		order = append(order, name)
	}
	for name, title := range deploy.Planned() {
		byName[name] = &CloudProvider{Name: name, Title: title, Projects: []CloudProject{},
			Hint: title + " chưa được hỗ trợ làm PROD"}
		order = append(order, name)
	}

	// Each connected provider's account and usage, beside the projects.
	var wg sync.WaitGroup
	for _, name := range order {
		cp := byName[name]
		if !cp.Built || !cp.Connected {
			continue
		}
		prov, err := m.Provider(name)
		if err != nil {
			continue
		}
		wg.Add(1)
		go func(cp *CloudProvider, prov deploy.Provider) {
			defer wg.Done()
			if ar, ok := prov.(deploy.AccountReader); ok {
				cp.Account = ar.Account(ctx)
			}
			if mr, ok := prov.(deploy.MetricsReader); ok {
				cp.Metrics = mr.Metrics(ctx, cloudWindow)
			}
		}(cp, prov)
	}

	projects, err := m.projects(ctx)
	wg.Wait()
	if err != nil {
		return nil, err
	}
	for _, p := range projects {
		name := p.Prod.Provider
		if name == "" {
			name = deploy.DefaultTarget
		}
		cp := byName[name]
		if cp == nil {
			continue
		}
		cp.Projects = append(cp.Projects, p)
	}

	// Built first, connected first, the busiest next, then by name; planned last.
	sort.SliceStable(order, func(i, j int) bool {
		a, b := byName[order[i]], byName[order[j]]
		if a.Built != b.Built {
			return a.Built
		}
		if a.Connected != b.Connected {
			return a.Connected
		}
		if len(a.Projects) != len(b.Projects) {
			return len(a.Projects) > len(b.Projects)
		}
		return a.Name < b.Name
	})
	for _, name := range order {
		cp := byName[name]
		sort.SliceStable(cp.Projects, func(i, j int) bool {
			a, b := cp.Projects[i], cp.Projects[j]
			if a.Prod.Deployed != b.Prod.Deployed {
				return a.Prod.Deployed
			}
			return a.Name < b.Name
		})
		out.Providers = append(out.Providers, *cp)
	}

	cloudCached.mu.Lock()
	cloudCached.at, cloudCached.val = time.Now(), out
	cloudCached.mu.Unlock()
	return out, nil
}

// projects is the environments of every project room, asked a few at a time.
func (m *EnvManager) projects(ctx context.Context) ([]CloudProject, error) {
	if m.coord == nil {
		return nil, nil
	}
	chans, err := m.coord.ListChannels("", "")
	if err != nil {
		return nil, err
	}
	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		out []CloudProject
		sem = make(chan struct{}, 4)
	)
	for _, c := range chans {
		if c.Workspace == "" {
			continue
		}
		wg.Add(1)
		go func(id, name string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			st := m.Status(ctx, id)
			// A room that neither declares PROD nor has anything live on a
			// provider is a project without infrastructure: not listed.
			if !st.Declared && !st.Prod.Deployed {
				return
			}
			p := CloudProject{ChannelID: id, Name: name, Declared: st.Declared, DevAhead: st.DevAhead,
				Prod: st.Prod, Git: st.Dev.Git, Error: st.Error}
			p.Prod.Account = nil // said once, on the provider
			if st.Deploy != nil {
				p.Running = st.Deploy.Running
			}
			mu.Lock()
			out = append(out, p)
			mu.Unlock()
		}(c.ID, c.Name)
	}
	wg.Wait()
	return out, nil
}

func handleCloud(ctx context.Context, deps Deps, params json.RawMessage) (json.RawMessage, error) {
	if deps.Envs == nil {
		return nil, fmt.Errorf("cloud status is answered by the dashboard process (bomclaw dashboard), not by an agent's gateway")
	}
	var p struct {
		Fresh bool `json:"fresh,omitempty"`
	}
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}
	st, err := deps.Envs.Cloud(ctx, p.Fresh)
	if err != nil {
		return nil, err
	}
	return json.Marshal(st)
}
