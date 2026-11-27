package deploy

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ngocp/goterm-control/internal/connectors"
)

// Vercel is the Vercel provider, over its REST API alone — no CLI to install.
//
// Two shapes of project:
//   - "static": the folder built here is uploaded file by file and served as
//     is (a generated vercel.json rewrites unknown paths for an SPA).
//   - "framework": the project's source is uploaded and Vercel builds it
//     ("nextjs", "vite", "astro", …) — what a Next.js app with server
//     routes needs, since those only run on Vercel's own build.
//
// It answers on https://<name>.vercel.app, and on a custom domain once the
// domain points at Vercel (pointDNS writes that when the domain is on a
// Cloudflare zone). Connector values become the project's environment
// variables before the build, so server code and the build both see them.
// Vercel hosts no database here: a project with one stays on Cloudflare (D1).
type Vercel struct {
	Token string
	Team  string // VERCEL_TEAM_ID; empty = the token's personal account
	API   string // https://api.vercel.com; overridden in tests
	HTTP  *http.Client
	// Poll is how often a build is checked; short in tests.
	Poll time.Duration
}

func init() {
	Register("vercel", func() Provider { return NewVercel() })
}

// NewVercel reads VERCEL_TOKEN and VERCEL_TEAM_ID.
func NewVercel() *Vercel {
	return &Vercel{
		Token: Lookup("VERCEL_TOKEN"), Team: Lookup("VERCEL_TEAM_ID"),
		API: "https://api.vercel.com", HTTP: &http.Client{Timeout: 60 * time.Second}, Poll: 3 * time.Second,
	}
}

// ErrNoVercel says what to set.
var ErrNoVercel = errors.New("Vercel is not connected: add a token on the dashboard (Admin → Connections), " +
	"or `bomclaw secrets set vercel VERCEL_TOKEN=…` (vercel.com/account/tokens)")

func (v *Vercel) Name() string  { return "vercel" }
func (v *Vercel) Title() string { return "Vercel" }

func (v *Vercel) Validate(s *Spec) error {
	if s.Dockerfile != "" {
		return fmt.Errorf("deploy.dockerfile: Vercel does not run containers — use \"target\": \"fly\" or \"aws\" for an app with a Dockerfile")
	}
	if s.Main != "" {
		return fmt.Errorf("deploy.main is a Cloudflare Worker script; on Vercel, server code is part of the framework build (deploy.framework)")
	}
	if s.Database != nil {
		return fmt.Errorf("deploy.database: Vercel hosts no database in bomclaw — keep the data on Cloudflare D1 (\"target\": \"cloudflare\") or use a connector (Supabase)")
	}
	if s.Static == "" && s.Framework == "" {
		return fmt.Errorf("on Vercel, deploy needs \"static\" (a folder built here) or \"framework\" (source Vercel builds, e.g. \"nextjs\")")
	}
	if s.Static != "" && s.Framework != "" {
		return fmt.Errorf("on Vercel, deploy has both \"static\" and \"framework\"; keep one")
	}
	return nil
}

func (v *Vercel) Connected() (bool, string) {
	if v.Token == "" {
		return false, ErrNoVercel.Error()
	}
	return true, ""
}

// vercelErr is the API's error body.
type vercelErr struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// call sends one request. A 404 is (404, nil) so callers can say "not yet".
func (v *Vercel) call(ctx context.Context, method, path string, body io.Reader, hdr map[string]string, out any) (int, error) {
	u := v.API + path
	if v.Team != "" {
		sep := "?"
		if strings.Contains(path, "?") {
			sep = "&"
		}
		u += sep + "teamId=" + url.QueryEscape(v.Team)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+v.Token)
	for k, val := range hdr {
		req.Header.Set(k, val)
	}
	resp, err := v.HTTP.Do(req)
	if err != nil {
		return 0, fmt.Errorf("vercel: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return 404, nil
	}
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2000))
		var e vercelErr
		if json.Unmarshal(b, &e) == nil && e.Error.Message != "" {
			return resp.StatusCode, &VercelError{Status: resp.StatusCode, Code: e.Error.Code, Message: e.Error.Message}
		}
		return resp.StatusCode, fmt.Errorf("vercel %s %s: %s %s", method, path, resp.Status, strings.TrimSpace(string(b)))
	}
	if out != nil {
		return resp.StatusCode, json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode, nil
}

// VercelError is an API refusal with its code.
type VercelError struct {
	Status        int
	Code, Message string
}

func (e *VercelError) Error() string { return fmt.Sprintf("vercel: %s (%s)", e.Message, e.Code) }

func (v *Vercel) json(ctx context.Context, method, path string, in, out any) (int, error) {
	var body io.Reader
	hdr := map[string]string{}
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
		hdr["Content-Type"] = "application/json"
	}
	return v.call(ctx, method, path, body, hdr, out)
}

type vercelDeployment struct {
	ID           string            `json:"id"`
	UID          string            `json:"uid"`
	URL          string            `json:"url"`
	ReadyState   string            `json:"readyState"`
	State        string            `json:"state"`
	CreatedAt    int64             `json:"createdAt"`
	Created      int64             `json:"created"`
	Meta         map[string]string `json:"meta"`
	Alias        []string          `json:"alias"`
	ErrorMessage string            `json:"errorMessage"`
	Name         string            `json:"name"`
	Target       string            `json:"target"`
}

func (d *vercelDeployment) id() string { return firstNonEmpty(d.ID, d.UID) }
func (d *vercelDeployment) state() string {
	return firstNonEmpty(d.ReadyState, d.State)
}
func (d *vercelDeployment) created() time.Time {
	ms := d.CreatedAt
	if ms == 0 {
		ms = d.Created
	}
	return time.UnixMilli(ms)
}

type vercelProject struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Framework string `json:"framework"`
	Targets   struct {
		Production *vercelDeployment `json:"production"`
	} `json:"targets"`
}

type vercelDomain struct {
	Name         string `json:"name"`
	Verified     bool   `json:"verified"`
	Verification []struct {
		Type   string `json:"type"`
		Domain string `json:"domain"`
		Value  string `json:"value"`
	} `json:"verification"`
}

func (v *Vercel) project(ctx context.Context, name string) (*vercelProject, error) {
	var p vercelProject
	code, err := v.json(ctx, http.MethodGet, "/v9/projects/"+url.PathEscape(name), nil, &p)
	if err != nil || code == 404 {
		return nil, err
	}
	return &p, nil
}

func (v *Vercel) domains(ctx context.Context, name string) ([]vercelDomain, error) {
	var out struct {
		Domains []vercelDomain `json:"domains"`
	}
	code, err := v.json(ctx, http.MethodGet, "/v9/projects/"+url.PathEscape(name)+"/domains", nil, &out)
	if err != nil || code == 404 {
		return nil, err
	}
	return out.Domains, nil
}

// Status asks Vercel what is live: the project, its production deployment,
// and which of its domains are verified.
func (v *Vercel) Status(ctx context.Context, r Resolved) (st ProdStatus) {
	st = ProdStatus{Provider: v.Name(), Title: v.Title(), Declared: true, Name: r.Name, WantDomain: r.Domain,
		Services: vercelServices(r)}
	if ok, hint := v.Connected(); !ok {
		st.Error = hint
		return st
	}
	st.Connected = true
	acc := make(chan *Account, 1)
	go func() { acc <- v.Account(ctx) }()
	defer func() { st.Account = <-acc }()

	p, err := v.project(ctx, r.Name)
	if err != nil {
		st.Error = err.Error()
		return st
	}
	if p == nil || p.Targets.Production == nil {
		return st // never deployed
	}
	fillVercel(&st, p, r.Name)
	if doms, err := v.domains(ctx, r.Name); err == nil {
		for _, d := range doms {
			if d.Verified && !strings.HasSuffix(d.Name, ".vercel.app") {
				st.Domains = append(st.Domains, d.Name)
			}
		}
		if len(st.Domains) > 0 {
			st.URL = "https://" + st.Domains[0]
		}
	}
	return st
}

// fillVercel is a project's production deployment as ProdStatus.
func fillVercel(st *ProdStatus, p *vercelProject, name string) {
	d := p.Targets.Production
	st.Deployed = d.state() == "READY" || d.state() == ""
	if s := d.state(); s == "ERROR" || s == "CANCELED" {
		st.Error = "production deployment " + strings.ToLower(s)
	}
	st.DeployedAt = d.created().UTC().Format(time.RFC3339)
	st.Version = &ProdVersion{ID: d.id(), Message: d.Meta["bomclawMessage"],
		Commit: firstNonEmpty(d.Meta["bomclawCommit"], shortCommit(d.Meta["githubCommitSha"])), By: d.Meta["bomclawBy"]}
	// The project's own production alias, else the deployment's address.
	st.WorkersDev = "https://" + name + ".vercel.app"
	for _, a := range d.Alias {
		if strings.HasSuffix(a, ".vercel.app") {
			st.WorkersDev = "https://" + a
			break
		}
	}
	st.URL = st.WorkersDev
}

func shortCommit(c string) string {
	if len(c) > 7 {
		return c[:7]
	}
	return c
}

func vercelServices(r Resolved) []string {
	out := []string{"Deployments"}
	if r.Framework != "" {
		out = append(out, "Build ("+r.Framework+")")
	}
	if r.Domain != "" {
		out = append(out, "Domains")
	}
	return out
}

// Account is the Vercel account or team the token deploys into, with its plan.
func (v *Vercel) Account(ctx context.Context) *Account {
	if ok, _ := v.Connected(); !ok {
		return nil
	}
	if v.Team != "" {
		var t struct {
			Name    string `json:"name"`
			Slug    string `json:"slug"`
			Billing struct {
				Plan string `json:"plan"`
			} `json:"billing"`
		}
		if _, err := v.json(ctx, http.MethodGet, "/v2/teams/"+url.PathEscape(v.Team), nil, &t); err == nil {
			return &Account{Name: firstNonEmpty(t.Name, t.Slug), Plan: vercelPlan(t.Billing.Plan)}
		}
	}
	var u struct {
		User struct {
			Username string `json:"username"`
			Name     string `json:"name"`
			Email    string `json:"email"`
			Billing  *struct {
				Plan string `json:"plan"`
			} `json:"billing"`
		} `json:"user"`
	}
	if _, err := v.json(ctx, http.MethodGet, "/v2/user", nil, &u); err != nil {
		return &Account{PlanHint: err.Error()}
	}
	a := &Account{Name: firstNonEmpty(u.User.Name, u.User.Username, u.User.Email)}
	if u.User.Billing != nil {
		a.Plan = vercelPlan(u.User.Billing.Plan)
	}
	if a.Plan == "" {
		a.PlanHint = "Vercel không trả gói cho token này"
	}
	return a
}

func vercelPlan(p string) string {
	switch strings.ToLower(p) {
	case "":
		return ""
	case "hobby":
		return "Hobby"
	case "pro":
		return "Pro"
	case "enterprise":
		return "Enterprise"
	}
	return p
}

// Verify checks the token and names the account.
func (v *Vercel) Verify(ctx context.Context) (string, error) {
	if ok, hint := v.Connected(); !ok {
		return "", errors.New(hint)
	}
	a := v.Account(ctx)
	if a == nil || a.Name == "" {
		hint := ""
		if a != nil {
			hint = a.PlanHint
		}
		return "", fmt.Errorf("the token was refused: %s", hint)
	}
	msg := "token active · " + a.Name
	if a.Plan != "" {
		msg += " · plan " + a.Plan
	}
	return msg, nil
}

// Discover: a Vercel project named like the room is live PROD.
func (v *Vercel) Discover(ctx context.Context, cands []Candidate) (*ProdStatus, string) {
	if ok, _ := v.Connected(); !ok {
		return nil, ""
	}
	for _, c := range cands {
		p, err := v.project(ctx, c.Name)
		if err != nil || p == nil || p.Targets.Production == nil {
			continue
		}
		st := &ProdStatus{Provider: v.Name(), Title: v.Title(), Connected: true, Name: p.Name, Services: []string{"Deployments"}}
		fillVercel(st, p, p.Name)
		return st, c.Source
	}
	return nil, ""
}

// vercelFile is one file of a deployment.
type vercelFile struct {
	File string `json:"file"`
	SHA  string `json:"sha"`
	Size int64  `json:"size"`
	path string
	data []byte // generated files (vercel.json) have no path
}

// skipSource are folders a source upload leaves out: dependencies and build
// output Vercel makes again, version control, and local secrets.
var skipSource = map[string]bool{".git": true, "node_modules": true, ".vercel": true, ".next": true,
	".turbo": true, ".cache": true, ".svelte-kit": true, ".nuxt": true, ".output": true}

func collectFiles(root string, source bool) ([]vercelFile, error) {
	var out []vercelFile
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if p != root && source && skipSource[name] {
				return filepath.SkipDir
			}
			return nil
		}
		// A .env file holds secrets; they reach Vercel as environment
		// variables (the connectors), never as an uploaded file.
		if source && (name == ".env" || strings.HasPrefix(name, ".env.")) {
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		sum := sha1.Sum(b)
		out = append(out, vercelFile{File: filepath.ToSlash(rel), SHA: hex.EncodeToString(sum[:]), Size: int64(len(b)), path: p})
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].File < out[j].File })
	return out, err
}

func generated(name string, b []byte) vercelFile {
	sum := sha1.Sum(b)
	return vercelFile{File: name, SHA: hex.EncodeToString(sum[:]), Size: int64(len(b)), data: b}
}

// spaConfig rewrites unknown paths to index.html, for client-side routers.
const spaConfig = `{"rewrites":[{"source":"/(.*)","destination":"/index.html"}]}`

func (v *Vercel) upload(ctx context.Context, f vercelFile) error {
	b := f.data
	if b == nil {
		var err error
		if b, err = os.ReadFile(f.path); err != nil {
			return err
		}
	}
	_, err := v.call(ctx, http.MethodPost, "/v2/files", bytes.NewReader(b), map[string]string{
		"Content-Type": "application/octet-stream", "x-vercel-digest": f.SHA, "Content-Length": fmt.Sprint(len(b)),
	}, nil)
	return err
}

func (v *Vercel) uploadAll(ctx context.Context, files []vercelFile) error {
	sem := make(chan struct{}, 8)
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		first error
	)
	for _, f := range files {
		wg.Add(1)
		go func(f vercelFile) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if err := v.upload(ctx, f); err != nil {
				mu.Lock()
				if first == nil {
					first = fmt.Errorf("upload %s: %w", f.File, err)
				}
				mu.Unlock()
			}
		}(f)
	}
	wg.Wait()
	return first
}

// ensureProject creates the project the first time, with its framework.
func (v *Vercel) ensureProject(ctx context.Context, r Resolved, step func(string, ...any)) (*vercelProject, error) {
	p, err := v.project(ctx, r.Name)
	if err != nil || p != nil {
		return p, err
	}
	body := map[string]any{"name": r.Name}
	if r.Framework != "" {
		body["framework"] = r.Framework
	}
	var np vercelProject
	if _, err := v.json(ctx, http.MethodPost, "/v11/projects", body, &np); err != nil {
		return nil, fmt.Errorf("create project %s: %w", r.Name, err)
	}
	step("project: created %s", r.Name)
	return &np, nil
}

// setEnv makes the connectors' values the project's environment variables.
func (v *Vercel) setEnv(ctx context.Context, projectID string, vars []connectors.Var) error {
	for _, x := range vars {
		typ := "encrypted"
		if x.Public {
			typ = "plain"
		}
		body := map[string]any{"key": x.Name, "value": x.Value, "type": typ, "target": []string{"production", "preview"}}
		if _, err := v.json(ctx, http.MethodPost, "/v10/projects/"+url.PathEscape(projectID)+"/env?upsert=true", body, nil); err != nil {
			return fmt.Errorf("env %s: %w", x.Name, err)
		}
	}
	return nil
}

// Deploy uploads, has Vercel deploy to production, waits for it to be ready,
// and attaches the domain.
func (v *Vercel) Deploy(ctx context.Context, r Resolved, opt Options, log io.Writer) (*Result, error) {
	if opt.WithDB {
		return nil, fmt.Errorf("--with-db: Vercel hosts no database in bomclaw")
	}
	if !opt.DryRun {
		if ok, hint := v.Connected(); !ok {
			return nil, errors.New(hint)
		}
	}
	step := func(format string, a ...any) { fmt.Fprintf(log, "▸ "+format+"\n", a...) }
	res := &Result{DryRun: opt.DryRun}
	vars := projectConnectors(r, step)

	var (
		files []vercelFile
		err   error
	)
	settings := map[string]any{"framework": nil}
	if r.Framework != "" {
		files, err = collectFiles(r.WorkDir, true)
		if err != nil {
			return nil, err
		}
		settings = map[string]any{"framework": r.Framework}
		if r.Build != "" {
			settings["buildCommand"] = r.Build
		}
		step("source: %d files from %s, built by Vercel (%s)", len(files), r.WorkDir, r.Framework)
	} else {
		if r.Build != "" {
			step("build: %s", r.Build)
			if err := ExecRunner(ctx, r.WorkDir, connectors.BuildEnv(vars), log, "sh", "-c", r.Build); err != nil {
				return nil, fmt.Errorf("build failed: %w", err)
			}
		}
		files, err = collectFiles(r.StaticDir, false)
		if err != nil || len(files) == 0 {
			return nil, fmt.Errorf("%s is empty or missing — nothing to publish (run the build, or fix deploy.static)", r.StaticDir)
		}
		has := false
		for _, f := range files {
			has = has || f.File == "vercel.json"
		}
		if r.SPA && !has {
			files = append(files, generated("vercel.json", []byte(spaConfig)))
		}
		step("static: %d files from %s", len(files), r.StaticDir)
	}
	var total int64
	for _, f := range files {
		total += f.Size
	}
	step("upload: %d files, %d KB", len(files), total/1024)
	if opt.DryRun {
		step("dry run: stopping before anything reaches Vercel")
		return res, nil
	}

	p, err := v.ensureProject(ctx, r, step)
	if err != nil {
		return nil, err
	}
	if len(vars) > 0 {
		if err := v.setEnv(ctx, p.ID, vars); err != nil {
			return nil, err
		}
		step("env: %d value(s) from the project's connectors → project %s", len(vars), r.Name)
	}
	if err := v.uploadAll(ctx, files); err != nil {
		return nil, err
	}

	g := Git(ctx, r.ProjectDir)
	meta := map[string]string{"bomclawBy": "bomclaw"}
	if g != nil {
		meta["bomclawCommit"] = g.Short
		meta["bomclawMessage"] = deployMessage(g)
	}
	body := map[string]any{"name": r.Name, "project": r.Name, "target": "production",
		"files": files, "projectSettings": settings, "meta": meta}
	var d vercelDeployment
	if _, err := v.json(ctx, http.MethodPost, "/v13/deployments?skipAutoDetectionConfirmation=1", body, &d); err != nil {
		return nil, fmt.Errorf("create deployment: %w", err)
	}
	step("deployment %s: %s", d.id(), "https://"+d.URL)
	if err := v.wait(ctx, &d, step); err != nil {
		return nil, err
	}
	res.URL = "https://" + r.Name + ".vercel.app"
	for _, a := range d.Alias {
		if strings.HasSuffix(a, ".vercel.app") {
			res.URL = "https://" + a
			break
		}
	}
	if r.Domain != "" {
		if v.attachDomain(ctx, r.Name, r.Domain, step) {
			res.URL = "https://" + r.Domain
		}
	}
	step("live: %s", res.URL)
	return res, nil
}

// wait follows a deployment until it is ready or failed.
func (v *Vercel) wait(ctx context.Context, d *vercelDeployment, step func(string, ...any)) error {
	last := ""
	for {
		s := d.state()
		if s != last {
			step("state: %s", strings.ToLower(s))
			last = s
		}
		switch s {
		case "READY":
			return nil
		case "ERROR", "CANCELED":
			return fmt.Errorf("deployment %s: %s%s", strings.ToLower(s), d.ErrorMessage, v.buildTail(ctx, d.id()))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(v.Poll):
		}
		if _, err := v.json(ctx, http.MethodGet, "/v13/deployments/"+url.PathEscape(d.id()), nil, d); err != nil {
			return err
		}
	}
}

// buildTail is the end of a failed build's log.
func (v *Vercel) buildTail(ctx context.Context, id string) string {
	var events []struct {
		Text    string `json:"text"`
		Payload struct {
			Text string `json:"text"`
		} `json:"payload"`
	}
	if _, err := v.json(ctx, http.MethodGet, "/v3/deployments/"+url.PathEscape(id)+"/events?limit=40&direction=backward", nil, &events); err != nil || len(events) == 0 {
		return ""
	}
	var lines []string
	for _, e := range events {
		if t := strings.TrimSpace(firstNonEmpty(e.Text, e.Payload.Text)); t != "" {
			lines = append(lines, t)
		}
	}
	if len(lines) > 15 {
		lines = lines[len(lines)-15:]
	}
	return "\n" + strings.Join(lines, "\n")
}

// attachDomain adds the domain to the project and points DNS at Vercel. It
// reports whether the domain is serving already.
func (v *Vercel) attachDomain(ctx context.Context, project, domain string, step func(string, ...any)) bool {
	_, err := v.json(ctx, http.MethodPost, "/v10/projects/"+url.PathEscape(project)+"/domains", map[string]string{"name": domain}, nil)
	var ve *VercelError
	if err != nil && !(errors.As(err, &ve) && (ve.Status == http.StatusConflict || strings.Contains(ve.Code, "exist"))) {
		step("domain %s: %v", domain, err)
		return false
	}
	recs := []DNSRecord{{Type: "CNAME", Name: domain, Content: "cname.vercel-dns.com"}}
	if strings.Count(domain, ".") == 1 { // an apex cannot be a CNAME
		recs = []DNSRecord{{Type: "A", Name: domain, Content: "76.76.21.21"}}
	}
	if doms, err := v.domains(ctx, project); err == nil {
		for _, d := range doms {
			if d.Name != domain {
				continue
			}
			for _, x := range d.Verification {
				recs = append(recs, DNSRecord{Type: x.Type, Name: x.Domain, Content: x.Value})
			}
		}
	}
	pointDNS(ctx, recs, step)
	var cfg struct {
		Misconfigured bool `json:"misconfigured"`
	}
	if _, err := v.json(ctx, http.MethodGet, "/v6/domains/"+url.PathEscape(domain)+"/config", nil, &cfg); err == nil && !cfg.Misconfigured {
		step("domain %s: serving", domain)
		return true
	}
	step("domain %s: added — Vercel serves it once DNS has propagated (minutes)", domain)
	return false
}

// Metrics is the account's deployments over the window. Vercel's request
// counts are not in its public API, and the page says so.
func (v *Vercel) Metrics(ctx context.Context, window time.Duration) *Metrics {
	if ok, _ := v.Connected(); !ok {
		return nil
	}
	until := time.Now().UTC().Truncate(time.Minute)
	since := until.Add(-window)
	m := &Metrics{Since: since.Format(time.RFC3339), Until: until.Format(time.RFC3339), Unit: "Project",
		Note: "Vercel không mở số request qua API — xem Observability trên vercel.com"}
	var out struct {
		Deployments []vercelDeployment `json:"deployments"`
	}
	if _, err := v.json(ctx, http.MethodGet, fmt.Sprintf("/v6/deployments?since=%d&limit=100", since.UnixMilli()), nil, &out); err != nil {
		m.Error = err.Error()
		return m
	}
	per := map[string]*WorkerMetric{}
	for _, d := range out.Deployments {
		m.Deploys++
		w := per[d.Name]
		if w == nil {
			w = &WorkerMetric{Name: d.Name, Kind: "deployments"}
			per[d.Name] = w
		}
		w.Requests++ // deployments, for this provider
		if s := d.state(); s == "ERROR" || s == "CANCELED" {
			m.DeployErrors++
			w.Errors++
		}
	}
	var projects struct {
		Projects []struct{} `json:"projects"`
	}
	if _, err := v.json(ctx, http.MethodGet, "/v9/projects?limit=100", nil, &projects); err == nil {
		m.Scripts = len(projects.Projects)
	}
	for _, w := range per {
		m.Workers = append(m.Workers, *w)
	}
	sort.Slice(m.Workers, func(i, j int) bool { return m.Workers[i].Requests > m.Workers[j].Requests })
	return m
}
