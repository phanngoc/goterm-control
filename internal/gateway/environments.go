package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
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
	done     chan struct{} // closed when the deploy ends
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
		// Not declared yet — but whether the infrastructure is connected is
		// a fact about the account, not the project, and it is the first
		// thing the room header shows.
		st.Prod = m.accountOnly(ctx, deploy.DefaultTarget, c)
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

// accountOnly is PROD before a project declares it: which provider it would
// go to, whether that provider is connected, and the account it reaches.
func (m *EnvManager) accountOnly(ctx context.Context, target string, c *coord.Channel) deploy.ProdStatus {
	prov, err := m.Provider(target)
	if err != nil {
		return deploy.ProdStatus{}
	}
	ok, hint := prov.Connected()
	st := deploy.ProdStatus{Provider: prov.Name(), Title: prov.Title(), Connected: ok}
	if !ok {
		st.Error = hint
		return st
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	// A project published by hand (wrangler, from its own folder) is live
	// without a word in bomclaw.json; say so rather than "no PROD".
	if dv, ok := prov.(deploy.Discoverer); ok && c != nil {
		if found, from := dv.Discover(ctx, m.candidates(c)); found != nil {
			st = *found
			st.DiscoveredFrom = from
		}
	}
	if ar, isReader := prov.(deploy.AccountReader); isReader {
		st.Account = ar.Account(ctx)
	}
	return st
}

// candidates are the Worker names a project may be live under, most specific
// first: what its own wrangler configs say, the name `bomclaw env` would give
// it, its folder, then the repos the room keeps.
func (m *EnvManager) candidates(c *coord.Channel) []deploy.Candidate {
	out := deploy.WranglerNames(c.Workspace)
	out = append(out, deploy.Candidate{Name: deploy.DefaultName(c.ID), Source: "tên phòng"})
	if c.Workspace != "" {
		out = append(out, deploy.Candidate{Name: filepath.Base(c.Workspace), Source: "thư mục dự án"})
	}
	if repos, err := m.coord.ListRepos(c.ID); err == nil {
		for _, r := range repos {
			if i := strings.LastIndex(r.Repo, "/"); i >= 0 {
				out = append(out, deploy.Candidate{Name: r.Repo[i+1:], Source: "repo " + r.Repo})
			}
		}
	}
	return out
}

// Deploy starts a deploy in the background; Status and Logs follow it.
func (m *EnvManager) Deploy(channelID string, opts deploy.Options) (*DeployJobView, error) {
	return m.DeployBy(channelID, opts, coord.Member{Kind: coord.MemberUser, ID: coord.OwnerUserID})
}

// DeployBy is Deploy on someone's behalf: the person at the Publish button,
// or the agent calling the publish tool. A real publish — not a dry run — is
// announced in the project's room when it ends, by whoever started it, so
// the team learns a new version is live (or that it failed) where they talk
// about the project rather than by opening a panel.
func (m *EnvManager) DeployBy(channelID string, opts deploy.Options, by coord.Member) (*DeployJobView, error) {
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
	job = &deployJob{running: true, started: time.Now(), opts: opts, log: ringLog{max: 256 << 10}, done: make(chan struct{})}
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
		if !opts.DryRun {
			m.announce(channelID, by, res, err)
		} else if res != nil && strings.HasSuffix(res.ConfigPath, "/wrangler.json") {
			// A rehearsal's generated config is for reading in the log, not
			// for keeping: one temp folder per dry run otherwise piles up.
			_ = os.RemoveAll(strings.TrimSuffix(res.ConfigPath, "/wrangler.json"))
		}
		close(job.done)
	}()
	return m.jobView(channelID), nil
}

// Wait blocks until the channel's current deploy ends, or ctx does, and
// returns how it ended. For the publish tool, which answers when it is over.
func (m *EnvManager) Wait(ctx context.Context, channelID string) (*DeployJobView, error) {
	m.mu.Lock()
	job := m.jobs[channelID]
	m.mu.Unlock()
	if job == nil {
		return nil, fmt.Errorf("no deploy of %s has started", channelID)
	}
	select {
	case <-job.done:
	case <-ctx.Done():
		return m.jobView(channelID), ctx.Err()
	}
	return m.jobView(channelID), nil
}

// announce posts the end of a publish into the project's room.
func (m *EnvManager) announce(channelID string, by coord.Member, res *deploy.Result, err error) {
	if m.coord == nil {
		return
	}
	body := ""
	switch {
	case err != nil:
		body = "✗ Publish lỗi: " + truncateLine(err.Error(), 300)
	case res != nil && res.URL != "":
		body = "🚀 Đã publish bản mới lên PROD: " + res.URL
		if res.DBUploaded {
			body += " (kèm database)"
		}
	default:
		body = "🚀 Đã publish bản mới lên PROD."
	}
	kind := by.Kind
	if kind == "" {
		kind = coord.MemberAgent
	}
	if _, _, perr := m.coord.PostMessage(coord.NewChannelMessage{
		ChannelID: channelID, AuthorKind: kind, AuthorID: by.ID, Body: body,
	}); perr != nil {
		log.Printf("env: announce publish of %s: %v", channelID, perr)
	}
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
