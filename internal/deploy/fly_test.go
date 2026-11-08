package deploy

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func fakeFly(t *testing.T, appExists bool) (*Fly, *[]string) {
	t.Helper()
	var mu sync.Mutex
	seen := []string{}
	exists := appExists
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.Path)
		mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer flytok" {
			w.WriteHeader(401)
			return
		}
		switch {
		case r.URL.Path == "/graphql":
			io.WriteString(w, `{"data":{"app":{"certificate":{"clientStatus":"Ready"}}}}`)
		case r.Method == "POST" && r.URL.Path == "/v1/apps":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["app_name"] != "phim" || body["org_slug"] != "personal" {
				w.WriteHeader(400)
				return
			}
			exists = true
			w.WriteHeader(201)
		case r.URL.Path == "/v1/apps/phim":
			if !exists {
				w.WriteHeader(404)
				return
			}
			io.WriteString(w, `{"name":"phim"}`)
		case r.URL.Path == "/v1/apps/phim/machines":
			io.WriteString(w, `[{"id":"m1","state":"started","region":"sin","updated_at":"2026-11-08T09:00:00Z"}]`)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	f := NewFly()
	f.Token, f.Org, f.Region = "flytok", "personal", "sin"
	f.Machines, f.GraphQL = srv.URL+"/v1", srv.URL+"/graphql"
	f.Flyctl = "flyctl"
	return f, &seen
}

func flyProject(t *testing.T, deploy string) Resolved {
	t.Helper()
	r := project(t, deploy)
	_ = os.MkdirAll(r.StaticDir, 0o755)
	_ = os.WriteFile(filepath.Join(r.StaticDir, "index.html"), []byte("<h1>phim</h1>"), 0o644)
	return r
}

// A static site on Fly is an nginx image around the built folder, generated
// outside the project, with a fly.toml whose machines sleep when idle.
func TestADryRunOnFlyShowsTheImageAndTheConfig(t *testing.T) {
	r := flyProject(t, `{"target":"fly","name":"phim","static":"dist","spa":true}`)
	var log bytes.Buffer
	res, err := (&Fly{Region: "sin", Run: (&fakeRun{}).run}).Deploy(context.Background(), r, Options{DryRun: true}, &log)
	if err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	defer os.RemoveAll(filepath.Dir(res.ConfigPath))
	out := log.String()
	for _, want := range []string{`app = "phim"`, `primary_region = "sin"`, "internal_port = 8080", `auto_stop_machines = "stop"`,
		"FROM nginx", "COPY dist /usr/share/nginx/html", "try_files $uri $uri/ /index.html"} {
		if !strings.Contains(out, want) {
			t.Errorf("dry run lacks %q:\n%s", want, out)
		}
	}
	if _, err := os.Stat(filepath.Join(r.ProjectDir, "Dockerfile")); err == nil {
		t.Error("the generated Dockerfile was written into the project")
	}
}

// First deploy: the app is created, flyctl builds remotely and releases with
// the token in its environment, the domain gets its certificate.
func TestAFirstDeployOnFlyCreatesTheAppAndReleases(t *testing.T) {
	r := flyProject(t, `{"target":"fly","name":"phim","static":"dist","domain":"phim.bomclaw.org"}`)
	f, seen := fakeFly(t, false)
	fr := &fakeRun{}
	f.Run = fr.run
	var log bytes.Buffer
	res, err := f.Deploy(context.Background(), r, Options{}, &log)
	if err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	if res.URL != "https://phim.bomclaw.org" {
		t.Errorf("url = %q", res.URL)
	}
	if !strings.Contains(strings.Join(*seen, "\n"), "POST /v1/apps") {
		t.Errorf("app was not created: %v", *seen)
	}
	var deployed, cert bool
	for _, c := range fr.calls {
		a := strings.Join(c.args, " ")
		if c.name == "flyctl" && strings.HasPrefix(a, "deploy ") && strings.Contains(a, "--remote-only") {
			deployed = true
			if !strings.Contains(strings.Join(c.env, " "), "FLY_API_TOKEN=flytok") {
				t.Error("flyctl ran without the token")
			}
		}
		if c.name == "flyctl" && strings.HasPrefix(a, "certs add phim.bomclaw.org -a phim") {
			cert = true
		}
	}
	if !deployed || !cert {
		t.Errorf("calls = %+v", fr.calls)
	}
	if _, err := f.Deploy(context.Background(), r, Options{WithDB: true}, io.Discard); err == nil {
		t.Error("--with-db on Fly should say there is no database yet")
	}
}

func TestFlyStatusSaysWhatIsLiveAndWhere(t *testing.T) {
	r := flyProject(t, `{"target":"fly","name":"phim","static":"dist","domain":"phim.bomclaw.org"}`)
	f, _ := fakeFly(t, true)
	st := f.Status(context.Background(), r)
	if st.Error != "" || !st.Deployed || st.URL != "https://phim.bomclaw.org" || st.WorkersDev != "https://phim.fly.dev" || st.Title != "Fly.io" {
		t.Errorf("status = %+v", st)
	}
	f2, _ := fakeFly(t, false)
	if st := f2.Status(context.Background(), r); st.Deployed || st.Error != "" {
		t.Errorf("never deployed = %+v", st)
	}
	if st := (&Fly{}).Status(context.Background(), r); st.Connected || !strings.Contains(st.Error, "FLY_API_TOKEN") {
		t.Errorf("no token = %+v", st)
	}
}
