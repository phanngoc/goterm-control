package gateway

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ngocp/goterm-control/internal/coord"
	"github.com/ngocp/goterm-control/internal/deploy"
)

func envProject(t *testing.T) (*EnvManager, *coord.Channel) {
	t.Helper()
	cdb, err := coord.Open(filepath.Join(t.TempDir(), "coord.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cdb.Close() })
	c, err := cdb.CreateProject("phim", "", coord.OwnerUserID, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(c.Workspace, "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(c.Workspace, "dist", "index.html"), []byte("<h1>phim</h1>"), 0o644)
	m := NewEnvManager(cdb, NewPreviewManager(cdb))
	m.Provider = func(target string) (deploy.Provider, error) {
		if target != "cloudflare" {
			return deploy.For(target)
		}
		return &deploy.Cloudflare{Client: deploy.NewClient(deploy.Creds{})}, nil
	}
	return m, c
}

// A room shows both environments even before Cloudflare is connected: DEV
// from the disk, PROD saying exactly what to set.
func TestARoomShowsItsDevAndProdEnvironments(t *testing.T) {
	m, c := envProject(t)

	st := m.Status(context.Background(), c.ID)
	if st.Declared || st.Error != "" || st.Dev.Preview == nil {
		t.Fatalf("undeclared = %+v", st)
	}

	if err := deploy.Write(c.Workspace, &deploy.Spec{Target: "cloudflare", Static: "dist", Domain: "phim.bomclaw.org"}); err != nil {
		t.Fatal(err)
	}
	st = m.Status(context.Background(), c.ID)
	if !st.Declared || st.Dev.Build == nil || !st.Dev.Build.Built || st.Dev.Build.Files != 1 {
		t.Fatalf("dev = %+v", st.Dev.Build)
	}
	if st.Prod.Connected || !strings.Contains(st.Prod.Error, "CLOUDFLARE_API_TOKEN") || st.Prod.WantDomain != "phim.bomclaw.org" {
		t.Errorf("prod = %+v", st.Prod)
	}
}

// A real deploy needs Cloudflare; a dry run does not, and shows its work.
func TestADeployIsRefusedWithoutCloudflareButADryRunRuns(t *testing.T) {
	m, c := envProject(t)
	_ = deploy.Write(c.Workspace, &deploy.Spec{Target: "cloudflare", Static: "dist"})

	if _, err := m.Deploy(c.ID, deploy.Options{}); err == nil || !strings.Contains(err.Error(), "CLOUDFLARE_API_TOKEN") {
		t.Fatalf("deploy without creds: %v", err)
	}
	raw, err := handleEnv(context.Background(), Deps{Envs: m}, "env.deploy",
		json.RawMessage(`{"channel_id":"`+c.ID+`","dry_run":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"running":true`) {
		t.Errorf("deploy view = %s", raw)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && m.jobView(c.ID).Running {
		time.Sleep(20 * time.Millisecond)
	}
	v := m.jobView(c.ID)
	if v.Running || v.Error != "" || v.Result == nil || !v.Result.DryRun {
		t.Fatalf("job = %+v\n%s", v, m.Logs(c.ID))
	}
	if logs := m.Logs(c.ID); !strings.Contains(logs, "dry run") || !strings.Contains(logs, `"name": "phim"`) {
		t.Errorf("logs:\n%s", logs)
	}
	if v.Result.ConfigPath != "" {
		os.RemoveAll(filepath.Dir(v.Result.ConfigPath))
	}
}
