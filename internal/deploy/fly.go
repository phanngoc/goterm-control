package deploy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Fly is the Fly.io provider: an app on Fly Machines, built from a Docker
// image — the project's own (deploy.dockerfile, for an app that runs a server)
// or, for a static site, an nginx image generated around the built folder. It
// answers on https://<name>.fly.dev, and on a custom domain once its
// certificate is added. flyctl does the building and releasing; the Machines
// API answers what is live.
//
// Fly's MCP server (mcp.fly.io) is the other way an agent drives Fly — it
// carries Machines, volumes and Managed Postgres as tools. This provider is
// the part the dashboard and `bomclaw env` need: one declared app, deployed
// the same way every time.
type Fly struct {
	Token  string
	Org    string // FLY_ORG; default "personal"
	Region string // FLY_REGION; default "sin"
	Run    Runner
	// Machines is the Machines API base; GraphQL the GraphQL endpoint (for
	// certificates). Overridden in tests.
	Machines string
	GraphQL  string
	HTTP     *http.Client
	// Flyctl is the binary; found on PATH ("flyctl" or "fly") when empty.
	Flyctl string
}

func init() {
	Register("fly", func() Provider { return NewFly() })
}

// NewFly reads FLY_API_TOKEN (flyctl's own name for it), FLY_ORG and FLY_REGION.
func NewFly() *Fly {
	org := strings.TrimSpace(os.Getenv("FLY_ORG"))
	if org == "" {
		org = "personal"
	}
	region := strings.TrimSpace(os.Getenv("FLY_REGION"))
	if region == "" {
		region = "sin"
	}
	return &Fly{
		Token: strings.TrimSpace(os.Getenv("FLY_API_TOKEN")), Org: org, Region: region,
		Machines: "https://api.machines.dev/v1", GraphQL: "https://api.fly.io/graphql",
		HTTP: &http.Client{Timeout: 20 * time.Second},
	}
}

// ErrNoFly says what to set.
var ErrNoFly = errors.New("Fly.io is not connected: set FLY_API_TOKEN (flyctl tokens create org, or fly auth token) " +
	"and optionally FLY_ORG / FLY_REGION in the gateway's .env, install flyctl (brew install flyctl), and restart")

func (f *Fly) Name() string  { return "fly" }
func (f *Fly) Title() string { return "Fly.io" }

func (f *Fly) Validate(s *Spec) error {
	if s.Main != "" {
		return fmt.Errorf("deploy.main is a Cloudflare Worker script; on Fly.io an app with a server is deploy.dockerfile")
	}
	if s.Static == "" && s.Dockerfile == "" {
		return fmt.Errorf("on Fly.io, deploy needs \"static\" (served by a generated nginx image) or \"dockerfile\"")
	}
	if s.Static != "" && s.Dockerfile != "" {
		return fmt.Errorf("on Fly.io, deploy has both \"static\" and \"dockerfile\"; keep one (copy the static files in your Dockerfile)")
	}
	if s.Database != nil {
		return fmt.Errorf("deploy.database on Fly.io (Managed Postgres) is planned, not built yet — run the database inside the app, or use \"target\": \"cloudflare\" for D1")
	}
	return nil
}

func (f *Fly) Connected() (bool, string) {
	if f.Token == "" {
		return false, ErrNoFly.Error()
	}
	return true, ""
}

func (f *Fly) flyctl() (string, error) {
	if f.Flyctl != "" {
		return f.Flyctl, nil
	}
	for _, n := range []string{"flyctl", "fly"} {
		if p, err := exec.LookPath(n); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("flyctl is not installed (brew install flyctl, or curl -L https://fly.io/install.sh | sh)")
}

func (f *Fly) api(ctx context.Context, method, path string, body, out any) (int, error) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, f.Machines+path, rd)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+f.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := f.HTTP.Do(req)
	if err != nil {
		return 0, fmt.Errorf("fly: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return 404, nil
	}
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
		return resp.StatusCode, fmt.Errorf("fly %s %s: %s %s", method, path, resp.Status, strings.TrimSpace(string(msg)))
	}
	if out != nil {
		return resp.StatusCode, json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode, nil
}

type flyMachine struct {
	ID        string `json:"id"`
	State     string `json:"state"`
	Region    string `json:"region"`
	UpdatedAt string `json:"updated_at"`
}

// Status asks Fly what is live: the app, its machines, the domain's certificate.
func (f *Fly) Status(ctx context.Context, r Resolved) ProdStatus {
	st := ProdStatus{Provider: f.Name(), Title: f.Title(), Declared: true, Name: r.Name, WantDomain: r.Domain}
	if ok, hint := f.Connected(); !ok {
		st.Error = hint
		return st
	}
	st.Connected = true
	code, err := f.api(ctx, http.MethodGet, "/apps/"+url.PathEscape(r.Name), nil, nil)
	if err != nil {
		st.Error = err.Error()
		return st
	}
	if code == 404 {
		return st // the app has not been created: never deployed
	}
	var machines []flyMachine
	if _, err := f.api(ctx, http.MethodGet, "/apps/"+url.PathEscape(r.Name)+"/machines", nil, &machines); err != nil {
		st.Error = err.Error()
		return st
	}
	if len(machines) == 0 {
		return st // created, nothing released yet
	}
	st.Deployed = true
	var newest time.Time
	for _, m := range machines {
		if t, err := time.Parse(time.RFC3339, m.UpdatedAt); err == nil && t.After(newest) {
			newest = t
		}
	}
	if !newest.IsZero() {
		st.DeployedAt = newest.Format(time.RFC3339)
	}
	st.WorkersDev = "https://" + r.Name + ".fly.dev" // the provider's own address
	st.URL = st.WorkersDev
	if r.Domain != "" {
		if ok, err := f.certReady(ctx, r.Name, r.Domain); err == nil && ok {
			st.Domains = []string{r.Domain}
			st.URL = "https://" + r.Domain
		}
	}
	return st
}

// certReady asks whether a custom domain's certificate is issued. Only the
// GraphQL API carries certificates.
func (f *Fly) certReady(ctx context.Context, app, host string) (bool, error) {
	q := map[string]any{
		"query":     `query($app:String!,$host:String!){app(name:$app){certificate(hostname:$host){clientStatus}}}`,
		"variables": map[string]string{"app": app, "host": host},
	}
	b, _ := json.Marshal(q)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.GraphQL, bytes.NewReader(b))
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+f.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := f.HTTP.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	var out struct {
		Data struct {
			App struct {
				Certificate *struct {
					ClientStatus string `json:"clientStatus"`
				} `json:"certificate"`
			} `json:"app"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return false, err
	}
	c := out.Data.App.Certificate
	return c != nil && strings.EqualFold(c.ClientStatus, "Ready"), nil
}

// Deploy creates the app the first time, builds the image on Fly's remote
// builder, releases it, and adds the domain's certificate.
func (f *Fly) Deploy(ctx context.Context, r Resolved, opt Options, log io.Writer) (*Result, error) {
	run := f.Run
	if run == nil {
		run = ExecRunner
	}
	if !opt.DryRun {
		if ok, hint := f.Connected(); !ok {
			return nil, errors.New(hint)
		}
	}
	if opt.WithDB {
		return nil, fmt.Errorf("--with-db: Fly.io has no database in this provider yet (Managed Postgres is planned)")
	}
	step := func(format string, a ...any) { fmt.Fprintf(log, "▸ "+format+"\n", a...) }
	res := &Result{DryRun: opt.DryRun}

	if r.Build != "" {
		step("build: %s", r.Build)
		if err := run(ctx, r.WorkDir, nil, log, "sh", "-c", r.Build); err != nil {
			return nil, fmt.Errorf("build failed: %w", err)
		}
	}

	tmp, err := os.MkdirTemp("", "bomclaw-fly-")
	if err != nil {
		return nil, err
	}
	if !opt.DryRun {
		defer os.RemoveAll(tmp)
	}
	dockerfile, port := r.DockerPath, r.Port
	if r.StaticDir != "" {
		n := 0
		_ = walkFiles(r.StaticDir, func(os.FileInfo) { n++ })
		if n == 0 {
			return nil, fmt.Errorf("%s is empty or missing — nothing to publish (run the build, or fix deploy.static)", r.StaticDir)
		}
		rel, err := filepath.Rel(r.WorkDir, r.StaticDir)
		if err != nil || strings.HasPrefix(rel, "..") {
			return nil, fmt.Errorf("deploy.static must be inside the project (%s)", r.StaticDir)
		}
		dockerfile = filepath.Join(tmp, "Dockerfile")
		if err := os.WriteFile(dockerfile, []byte(StaticDockerfile(rel, r.SPA)), 0o644); err != nil {
			return nil, err
		}
		port = 8080
		step("static: %d files from %s, served by nginx", n, r.StaticDir)
	} else if _, err := os.Stat(dockerfile); err != nil {
		return nil, fmt.Errorf("deploy.dockerfile: %w", err)
	}
	if port == 0 {
		port = 8080
	}
	region := r.Region
	if region == "" {
		region = f.Region
	}
	cfgPath := filepath.Join(tmp, "fly.toml")
	cfg := FlyConfig(r.Name, region, port)
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		return nil, err
	}
	res.ConfigPath = cfgPath
	step("config: %s", cfgPath)
	if opt.DryRun {
		fmt.Fprintf(log, "%s\n", cfg)
		if r.StaticDir != "" {
			b, _ := os.ReadFile(dockerfile)
			fmt.Fprintf(log, "--- Dockerfile ---\n%s\n", b)
		}
		step("dry run: stopping before flyctl deploy")
		return res, nil
	}

	bin, err := f.flyctl()
	if err != nil {
		return nil, err
	}
	env := []string{"FLY_API_TOKEN=" + f.Token}
	code, err := f.api(ctx, http.MethodGet, "/apps/"+url.PathEscape(r.Name), nil, nil)
	if err != nil {
		return nil, err
	}
	if code == 404 {
		step("create app %s in %s", r.Name, f.Org)
		if _, err := f.api(ctx, http.MethodPost, "/apps", map[string]string{"app_name": r.Name, "org_slug": f.Org}, nil); err != nil {
			return nil, fmt.Errorf("create app %s: %w", r.Name, err)
		}
	}
	step("flyctl deploy %s (remote builder)", r.Name)
	if err := run(ctx, r.WorkDir, env, log, bin, "deploy", r.WorkDir,
		"--config", cfgPath, "--dockerfile", dockerfile, "--remote-only", "--yes", "--ha=false"); err != nil {
		return nil, fmt.Errorf("flyctl deploy failed: %w", err)
	}
	res.URL = "https://" + r.Name + ".fly.dev"
	if r.Domain != "" {
		step("certificate for %s", r.Domain)
		var out bytes.Buffer
		if err := run(ctx, r.WorkDir, env, io.MultiWriter(log, &out), bin, "certs", "add", r.Domain, "-a", r.Name); err != nil &&
			!strings.Contains(strings.ToLower(out.String()), "already") {
			fmt.Fprintf(log, "  (the domain was not attached: %v — point a CNAME %s → %s.fly.dev and retry)\n", err, r.Domain, r.Name)
		} else {
			res.URL = "https://" + r.Domain
		}
	}
	step("live: %s", res.URL)
	return res, nil
}

// StaticDockerfile serves a built folder with nginx on 8080. SPA routes
// unknown paths to index.html.
func StaticDockerfile(staticRel string, spa bool) string {
	fallback := "=404"
	if spa {
		fallback = "/index.html"
	}
	return fmt.Sprintf(`FROM nginx:1.27-alpine
RUN printf 'server {\n  listen 8080;\n  root /usr/share/nginx/html;\n  location / { try_files $uri $uri/ %s; }\n}\n' > /etc/nginx/conf.d/default.conf
COPY %s /usr/share/nginx/html
`, fallback, filepath.ToSlash(staticRel))
}

// FlyConfig is the fly.toml: one HTTP service, HTTPS forced, machines that
// stop when idle and start on a request — a project site should not bill
// around the clock for nobody.
func FlyConfig(app, region string, port int) string {
	return fmt.Sprintf(`app = %q
primary_region = %q

[http_service]
  internal_port = %d
  force_https = true
  auto_stop_machines = "stop"
  auto_start_machines = true
  min_machines_running = 0
`, app, region, port)
}
