package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	// Before PROD is declared the room still says where it would go and
	// whether that is connected: the header leads with it.
	if st.Prod.Provider != "cloudflare" || st.Prod.Title != "Cloudflare" || st.Prod.Connected ||
		!strings.Contains(st.Prod.Error, "CLOUDFLARE_API_TOKEN") {
		t.Errorf("undeclared prod = %+v", st.Prod)
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

// fakeProvider publishes without going anywhere.
type fakeProvider struct{ fail bool }

func (fakeProvider) Name() string                { return "cloudflare" }
func (fakeProvider) Title() string               { return "Cloudflare" }
func (fakeProvider) Validate(*deploy.Spec) error { return nil }
func (fakeProvider) Connected() (bool, string)   { return true, "" }
func (fakeProvider) Status(context.Context, deploy.Resolved) deploy.ProdStatus {
	return deploy.ProdStatus{}
}
func (f fakeProvider) Deploy(_ context.Context, r deploy.Resolved, _ deploy.Options, log io.Writer) (*deploy.Result, error) {
	fmt.Fprintln(log, "▸ uploading")
	if f.fail {
		return nil, fmt.Errorf("wrangler exited 1")
	}
	return &deploy.Result{URL: "https://" + r.Name + ".bomclaw.org"}, nil
}

// A publish ends with a line in the project's room, by whoever started it —
// the team learns a version is live where they talk about the project.
func TestAPublishIsAnnouncedInTheRoom(t *testing.T) {
	m, c := envProject(t)
	if err := deploy.Write(c.Workspace, &deploy.Spec{Target: "cloudflare", Static: "dist"}); err != nil {
		t.Fatal(err)
	}
	for _, fail := range []bool{false, true} {
		m.Provider = func(string) (deploy.Provider, error) { return fakeProvider{fail: fail}, nil }
		if _, err := m.DeployBy(c.ID, deploy.Options{}, coord.Member{Kind: coord.MemberAgent, ID: "bomclaw2"}); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		v, err := m.Wait(ctx, c.ID)
		cancel()
		if err != nil || v.Running || (v.Error != "") != fail {
			t.Fatalf("fail=%v: job = %+v, %v", fail, v, err)
		}
	}
	msgs, err := m.coord.ChannelMessages(c.ID, 10, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	var said []string
	for _, msg := range msgs {
		if msg.AuthorID == "bomclaw2" {
			said = append(said, msg.Body)
		}
	}
	joined := strings.Join(said, "\n")
	if len(said) != 2 || !strings.Contains(joined, "https://phim.bomclaw.org") || !strings.Contains(joined, "wrangler exited 1") {
		t.Errorf("room heard %q", said)
	}
	// A dry run is a rehearsal: nobody is told.
	m.Provider = func(string) (deploy.Provider, error) { return fakeProvider{}, nil }
	_, _ = m.DeployBy(c.ID, deploy.Options{DryRun: true}, coord.Member{Kind: coord.MemberAgent, ID: "bomclaw2"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = m.Wait(ctx, c.ID)
	if after, _ := m.coord.ChannelMessages(c.ID, 10, time.Time{}); len(after) != len(msgs) {
		t.Errorf("a dry run was announced")
	}
}
