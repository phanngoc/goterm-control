package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ngocp/goterm-control/internal/coord"
	"github.com/ngocp/goterm-control/internal/deploy"
)

// A channel's two environments, side by side: DEV is the project on this
// machine (its Preview, its build, its local database), PROD is the same
// project published to Cloudflare (internal/deploy). Answered by the dashboard
// process, which also runs previews — it stays up while agents restart, and a
// deploy should not die with the agent that asked for it.

// EnvManager runs deploys, one at a time per channel.
type EnvManager struct {
	coord   *coord.DB
	preview *PreviewManager
	// Provider picks where PROD lives from bomclaw.json's deploy.target —
	// Cloudflare today, Fly.io next. Built per call so credentials added to
	// .env are seen after a restart; tests replace it.
	Provider func(target string) (deploy.Provider, error)

	mu   sync.Mutex
	jobs map[string]*deployJob
}

type deployJob struct {
	mu       sync.Mutex
	running  bool
	started  time.Time
	finished time.Time
	opts     deploy.Options
	log      ringLog
	result   *deploy.Result
	err      string
}

func NewEnvManager(cdb *coord.DB, previews *PreviewManager) *EnvManager {
	return &EnvManager{
		coord: cdb, preview: previews,
		Provider: deploy.For,
		jobs:     map[string]*deployJob{},
	}
}

// EnvStatus is both environments of one channel.
type EnvStatus struct {
	ChannelID  string            `json:"channel_id"`
	ProjectDir string            `json:"project_dir,omitempty"`
	Declared   bool              `json:"declared"` // bomclaw.json has "deploy"
	Error      string            `json:"error,omitempty"`
	Spec       *deploy.Spec      `json:"spec,omitempty"`
	Dev        EnvDev            `json:"dev"`
	Prod       deploy.ProdStatus `json:"prod"`
	Deploy     *DeployJobView    `json:"deploy,omitempty"`
}

// EnvDev is the local environment.
type EnvDev struct {
	Preview *PreviewStatus    `json:"preview,omitempty"`
	Build   *deploy.DevStatus `json:"build,omitempty"`
}

// DeployJobView is the last deploy of a channel, running or finished.
type DeployJobView struct {
	Running  bool           `json:"running"`
	Started  string         `json:"started"`
	Finished string         `json:"finished,omitempty"`
	WithDB   bool           `json:"with_db"`
	DryRun   bool           `json:"dry_run"`
	Result   *deploy.Result `json:"result,omitempty"`
	Error    string         `json:"error,omitempty"`
}

func (m *EnvManager) resolve(channelID string) (*coord.Channel, *deploy.Spec, error) {
	c, err := m.coord.GetChannel(channelID)
	if err != nil {
		return nil, nil, err
	}
	if c.Workspace == "" {
		return c, nil, fmt.Errorf("%s has no project folder — environments belong to project rooms", channelID)
	}
	s, err := deploy.Read(c.Workspace)
	return c, s, err
}

// Status reports both environments. It never changes anything.
func (m *EnvManager) Status(ctx context.Context, channelID string) EnvStatus {
	st := EnvStatus{ChannelID: channelID}
	c, spec, err := m.resolve(channelID)
	if c != nil {
		st.ProjectDir = c.Workspace
	}
	if m.preview != nil && c != nil && c.Workspace != "" {
		p := m.preview.Status(channelID)
		st.Dev.Preview = &p
	}
	if err != nil {
		st.Error = err.Error()
		return st
	}
	st.Deploy = m.jobView(channelID)
	if spec == nil {
		return st
	}
	st.Declared, st.Spec = true, spec
	r := spec.Resolve(c.Workspace, channelID)
	dev := deploy.Dev(r)
	st.Dev.Build = &dev
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	prov, err := m.Provider(spec.Target)
	if err != nil {
		st.Prod = deploy.ProdStatus{Declared: true, Provider: spec.Target, Error: err.Error()}
		return st
	}
	st.Prod = prov.Status(ctx, r)
	return st
}

// Deploy starts a deploy in the background; Status and Logs follow it.
func (m *EnvManager) Deploy(channelID string, opts deploy.Options) (*DeployJobView, error) {
	c, spec, err := m.resolve(channelID)
	if err != nil {
		return nil, err
	}
	if spec == nil {
		return nil, fmt.Errorf("%s declares no PROD environment — add \"deploy\" to %s (bomclaw env init)", channelID, deploy.Manifest)
	}
	prov, err := m.Provider(spec.Target)
	if err != nil {
		return nil, err
	}
	if ok, hint := prov.Connected(); !opts.DryRun && !ok {
		return nil, fmt.Errorf("%s", hint)
	}
	m.mu.Lock()
	job := m.jobs[channelID]
	if job != nil {
		job.mu.Lock()
		busy := job.running
		job.mu.Unlock()
		if busy {
			m.mu.Unlock()
			return nil, fmt.Errorf("a deploy of %s is already running", channelID)
		}
	}
	job = &deployJob{running: true, started: time.Now(), opts: opts, log: ringLog{max: 256 << 10}}
	m.jobs[channelID] = job
	m.mu.Unlock()

	r := spec.Resolve(c.Workspace, channelID)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
		defer cancel()
		res, err := prov.Deploy(ctx, r, opts, &job.log)
		job.mu.Lock()
		job.running, job.finished, job.result = false, time.Now(), res
		if err != nil {
			job.err = err.Error()
			fmt.Fprintf(&job.log, "✗ %v\n", err)
		} else {
			fmt.Fprintf(&job.log, "✓ done\n")
		}
		job.mu.Unlock()
	}()
	return m.jobView(channelID), nil
}

func (m *EnvManager) jobView(channelID string) *DeployJobView {
	m.mu.Lock()
	job := m.jobs[channelID]
	m.mu.Unlock()
	if job == nil {
		return nil
	}
	job.mu.Lock()
	defer job.mu.Unlock()
	v := &DeployJobView{
		Running: job.running, Started: job.started.Format(time.RFC3339),
		WithDB: job.opts.WithDB, DryRun: job.opts.DryRun, Result: job.result, Error: job.err,
	}
	if !job.finished.IsZero() {
		v.Finished = job.finished.Format(time.RFC3339)
	}
	return v
}

// Logs is the last deploy's output.
func (m *EnvManager) Logs(channelID string) string {
	m.mu.Lock()
	job := m.jobs[channelID]
	m.mu.Unlock()
	if job == nil {
		return ""
	}
	return job.log.String()
}

type envParams struct {
	ChannelID string `json:"channel_id"`
	WithDB    bool   `json:"with_db,omitempty"`
	DryRun    bool   `json:"dry_run,omitempty"`
}

func handleEnv(ctx context.Context, deps Deps, method string, params json.RawMessage) (json.RawMessage, error) {
	if deps.Envs == nil {
		return nil, fmt.Errorf("environments are run by the dashboard process (bomclaw dashboard), not by an agent's gateway")
	}
	var p envParams
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}
	if strings.TrimSpace(p.ChannelID) == "" {
		return nil, fmt.Errorf("channel_id is required")
	}
	switch method {
	case "env.status":
		return json.Marshal(deps.Envs.Status(ctx, p.ChannelID))
	case "env.deploy":
		v, err := deps.Envs.Deploy(p.ChannelID, deploy.Options{WithDB: p.WithDB, DryRun: p.DryRun})
		if err != nil {
			return nil, err
		}
		return json.Marshal(v)
	case "env.logs":
		return json.Marshal(map[string]string{"logs": deps.Envs.Logs(p.ChannelID)})
	}
	return nil, fmt.Errorf("unknown method: %s", method)
}
