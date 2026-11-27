package deploy

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeVercel is the slice of Vercel's API a publish touches.
type fakeVercel struct {
	mu       sync.Mutex
	project  bool
	files    map[string][]byte // sha → body
	deployed map[string]any
	polls    int
	env      []string
}

func (f *fakeVercel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer tok" {
		w.WriteHeader(403)
		return
	}
	j := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	switch {
	case r.Method == "GET" && r.URL.Path == "/v9/projects/site":
		if !f.project {
			w.WriteHeader(404)
			return
		}
		p := map[string]any{"id": "prj_1", "name": "site"}
		if f.deployed != nil {
			p["targets"] = map[string]any{"production": map[string]any{"id": "dpl_1", "readyState": "READY",
				"createdAt": time.Date(2026, 11, 27, 3, 0, 0, 0, time.UTC).UnixMilli(),
				"meta": f.deployed["meta"], "alias": []string{"site.vercel.app"}}}
		}
		j(p)
	case r.Method == "POST" && r.URL.Path == "/v11/projects":
		f.project = true
		j(map[string]any{"id": "prj_1", "name": "site"})
	case r.Method == "POST" && strings.HasPrefix(r.URL.Path, "/v10/projects/prj_1/env"):
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		f.env = append(f.env, b["key"].(string))
		j(map[string]any{})
	case r.Method == "POST" && r.URL.Path == "/v2/files":
		b, _ := io.ReadAll(r.Body)
		sum := sha1.Sum(b)
		if hex.EncodeToString(sum[:]) != r.Header.Get("x-vercel-digest") {
			w.WriteHeader(400)
			return
		}
		f.files[r.Header.Get("x-vercel-digest")] = b
		j(map[string]any{})
	case r.Method == "POST" && r.URL.Path == "/v13/deployments":
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		for _, x := range b["files"].([]any) {
			if _, ok := f.files[x.(map[string]any)["sha"].(string)]; !ok {
				w.WriteHeader(400)
				j(map[string]any{"error": map[string]any{"code": "missing_files", "message": "missing"}})
				return
			}
		}
		f.deployed = b
		j(map[string]any{"id": "dpl_1", "url": "site-abc.vercel.app", "readyState": "BUILDING"})
	case r.Method == "GET" && r.URL.Path == "/v13/deployments/dpl_1":
		f.polls++
		j(map[string]any{"id": "dpl_1", "url": "site-abc.vercel.app", "readyState": "READY", "alias": []string{"site.vercel.app"}})
	case r.Method == "GET" && r.URL.Path == "/v9/projects/site/domains":
		j(map[string]any{"domains": []any{}})
	case r.Method == "GET" && r.URL.Path == "/v2/user":
		j(map[string]any{"user": map[string]any{"username": "ngoc", "billing": map[string]any{"plan": "hobby"}}})
	default:
		w.WriteHeader(404)
	}
}

// A static site goes up file by file, the deployment is followed until it is
// ready, and status then reads back the live version and its commit.
func TestVercelPublishesAStaticSiteAndReadsItBack(t *testing.T) {
	fake := &fakeVercel{files: map[string][]byte{}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	v := &Vercel{Token: "tok", API: srv.URL, HTTP: srv.Client(), Poll: time.Millisecond}

	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "dist", "assets"), 0o755)
	os.WriteFile(filepath.Join(dir, "dist", "index.html"), []byte("<h1>hi</h1>"), 0o644)
	os.WriteFile(filepath.Join(dir, "dist", "assets", "app.js"), []byte("1"), 0o644)
	s := &Spec{Target: "vercel", Static: "dist", SPA: true, Name: "site"}
	if err := v.Validate(s); err != nil {
		t.Fatal(err)
	}
	r := s.Resolve(dir, "ch_site")

	var log bytes.Buffer
	res, err := v.Deploy(context.Background(), r, Options{}, &log)
	if err != nil {
		t.Fatalf("deploy: %v\n%s", err, log.String())
	}
	if res.URL != "https://site.vercel.app" {
		t.Errorf("url %q", res.URL)
	}
	files := fake.deployed["files"].([]any)
	names := []string{}
	for _, f := range files {
		names = append(names, f.(map[string]any)["file"].(string))
	}
	if strings.Join(names, ",") != "assets/app.js,index.html,vercel.json" {
		t.Errorf("files %v (an SPA gets a generated vercel.json)", names)
	}
	if fake.deployed["target"] != "production" {
		t.Errorf("target %v", fake.deployed["target"])
	}

	st := v.Status(context.Background(), r)
	if !st.Deployed || st.URL != "https://site.vercel.app" || st.Version == nil || st.Version.ID != "dpl_1" {
		t.Fatalf("status %+v %+v", st, st.Version)
	}
	if st.Account == nil || st.Account.Plan != "Hobby" {
		t.Errorf("account %+v", st.Account)
	}
}

// A source upload leaves out what Vercel rebuilds and what must never leave
// the machine: dependencies, git, and .env files.
func TestVercelSourceUploadSkipsDependenciesAndSecrets(t *testing.T) {
	dir := t.TempDir()
	for _, p := range []string{"package.json", "app/page.tsx", ".env", ".env.local", "node_modules/x/i.js", ".git/HEAD", ".next/a"} {
		os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0o755)
		os.WriteFile(filepath.Join(dir, p), []byte(p), 0o644)
	}
	files, err := collectFiles(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range files {
		got = append(got, f.File)
	}
	if strings.Join(got, ",") != "app/page.tsx,package.json" {
		t.Errorf("uploaded %v", got)
	}
}

func TestVercelRefusesWhatItCannotHost(t *testing.T) {
	v := &Vercel{}
	for _, s := range []*Spec{
		{Dockerfile: "Dockerfile"}, {Main: "w.js", Static: "dist"},
		{Static: "dist", Database: &Database{Engine: "sqlite", File: "a.db"}}, {},
	} {
		if v.Validate(s) == nil {
			t.Errorf("accepted %+v", s)
		}
	}
	if v.Validate(&Spec{Framework: "nextjs"}) != nil {
		t.Error("a framework build is what Vercel is for")
	}
}
