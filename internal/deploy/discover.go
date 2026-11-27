package deploy

import (
	"context"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Finding a project that is live without having said so.
//
// PROD is declared in bomclaw.json, and most projects never declared it: an
// agent published them with wrangler from a folder of their own, so
// shopee-deals answered at deal.bomclaw.org while the room said "no PROD".
// A header that says a live site is not live is worse than one that says
// nothing. So before calling a project undeployed, the account is asked
// whether a Worker it can be matched to is running.

// Candidate is a Worker name a project may be published under, and why.
type Candidate struct {
	Name   string
	Source string // "wrangler.jsonc", "tên phòng", "repo phanngoc/x"
}

// Discoverer is a Provider that can find a project's live deployment by name.
type Discoverer interface {
	Discover(ctx context.Context, cands []Candidate) (*ProdStatus, string)
}

var (
	wranglerJSONName = regexp.MustCompile(`"name"\s*:\s*"([^"]+)"`)
	wranglerTOMLName = regexp.MustCompile(`(?m)^\s*name\s*=\s*"([^"]+)"`)
)

// WranglerNames finds the Worker names wrangler configs in a project folder
// declare — the name a hand-run `wrangler deploy` published under. Only the
// top-level name of each file is taken (the first one in it), and
// node_modules and dot-folders are skipped; three levels deep is where a
// project keeps them (cloud/wrangler.jsonc, apps/web/wrangler.toml).
func WranglerNames(dir string) []Candidate {
	if dir == "" {
		return nil
	}
	var out []Candidate
	root := filepath.Clean(dir)
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		if d.IsDir() {
			if p != root && (strings.HasPrefix(d.Name(), ".") || d.Name() == "node_modules" ||
				strings.Count(rel, string(filepath.Separator)) >= 3) {
				return filepath.SkipDir
			}
			return nil
		}
		var re *regexp.Regexp
		switch d.Name() {
		case "wrangler.json", "wrangler.jsonc":
			re = wranglerJSONName
		case "wrangler.toml":
			re = wranglerTOMLName
		default:
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil || len(b) > 256<<10 {
			return nil
		}
		if m := re.FindSubmatch(b); m != nil {
			out = append(out, Candidate{Name: string(m[1]), Source: rel})
		}
		return nil
	})
	return out
}

type workerScript struct {
	ID         string    `json:"id"`
	ModifiedOn time.Time `json:"modified_on"`
}

// Discover finds the first candidate that is a Worker on the account and
// reports it as live PROD. It returns nil when none is; the string is the
// candidate's source, for the screen to say how it was matched.
func (d *Cloudflare) Discover(ctx context.Context, cands []Candidate) (*ProdStatus, string) {
	if ok, _ := d.Connected(); !ok || len(cands) == 0 {
		return nil, ""
	}
	c := d.Client
	var scripts []workerScript
	if err := c.do(ctx, http.MethodGet, c.acct()+"/workers/scripts", nil, &scripts); err != nil {
		return nil, ""
	}
	live := map[string]workerScript{}
	for _, s := range scripts {
		live[s.ID] = s
	}
	for _, cand := range cands {
		s, ok := live[cand.Name]
		if !ok {
			continue
		}
		st := &ProdStatus{Provider: d.Name(), Title: d.Title(), Connected: true, Name: s.ID, Deployed: true}
		if !s.ModifiedOn.IsZero() {
			st.DeployedAt = s.ModifiedOn.Format(time.RFC3339)
		}
		if sub, err := c.Subdomain(ctx); err == nil && sub != "" {
			st.WorkersDev = "https://" + s.ID + "." + sub + ".workers.dev"
			st.URL = st.WorkersDev
		}
		if hosts, err := c.Domains(ctx, s.ID); err == nil && len(hosts) > 0 {
			st.Domains = hosts
			st.URL = "https://" + hosts[0]
		}
		// Which version is live, even when it went out by hand.
		if dep, err := c.LatestDeployment(ctx, s.ID); err == nil && dep != nil {
			st.Version = dep.Version()
			st.DeployedAt = dep.CreatedOn.Format(time.RFC3339)
		}
		st.Services = []string{"Workers"}
		if len(st.Domains) > 0 {
			st.Services = append(st.Services, "Custom Domain")
		}
		return st, cand.Source
	}
	return nil, ""
}
