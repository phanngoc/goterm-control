package deploy

import (
	"bytes"
	"context"
	"database/sql"
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

func TestASpecFillsItsDefaultsAndRefusesWhatIsNotBuilt(t *testing.T) {
	s, err := Parse([]byte(`{"preview":{"static":"."},"deploy":{"static":"dist","database":{"file":"data/app.db"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	r := s.Resolve("/p", "ch_phim-ngan-25d")
	if r.Target != "cloudflare" || r.Name != "phim-ngan-25d" || r.DBName != "phim-ngan-25d-db" || r.DBBinding != "DB" {
		t.Errorf("resolved %+v", r)
	}
	if r.StaticDir != "/p/dist" || r.DBFile != "/p/data/app.db" {
		t.Errorf("paths %q %q", r.StaticDir, r.DBFile)
	}
	if s, err := Parse([]byte(`{"preview":{}}`)); s != nil || err != nil {
		t.Errorf("no deploy should be nil, nil: %v %v", s, err)
	}
	for _, bad := range []string{
		`{"deploy":{"target":"vercel","static":"dist"}}`,
		`{"deploy":{}}`,
		`{"deploy":{"static":"dist","name":"Bad Name"}}`,
		`{"deploy":{"static":"dist","database":{"engine":"supabase"}}}`,
		`{"deploy":{"target":"vercel","static":"dist"}}`,
		`{"deploy":{"target":"fly","main":"w.js"}}`,
		`{"deploy":{"target":"fly","static":"dist","database":{"file":"a.db"}}}`,
		`{"deploy":{"target":"cloudflare","dockerfile":"Dockerfile"}}`,
	} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
	if DefaultName("ch_Bất_Động_Sản") == "" || strings.ContainsAny(DefaultName("ch_Bất_Động_Sản"), "_ ") {
		t.Errorf("unsafe name %q", DefaultName("ch_Bất_Động_Sản"))
	}
}

func sampleDB(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{
		`CREATE TABLE films (id INTEGER PRIMARY KEY AUTOINCREMENT, title TEXT)`,
		`INSERT INTO films (title) VALUES ('Đậu và Bí'), ('Tích phân')`,
		`CREATE TABLE shots (film_id INTEGER REFERENCES films(id), n INTEGER)`,
		`INSERT INTO shots VALUES (1, 1), (1, 2)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
}

// D1 takes SQL, refuses BEGIN/COMMIT and sqlite_sequence, and an import on
// top of existing tables would double every row — so the upload drops and
// recreates exactly the tables the file has.
func TestTheDatabaseUploadReplacesTheProdTables(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.db")
	sampleDB(t, path)
	dump, err := DumpForD1(path)
	if err != nil {
		t.Skipf("sqlite3 CLI: %v", err)
	}
	s := string(dump)
	for _, bad := range []string{"BEGIN TRANSACTION", "COMMIT;", "sqlite_sequence"} {
		if strings.Contains(s, bad) {
			t.Errorf("dump still has %q", bad)
		}
	}
	for _, want := range []string{`DROP TABLE IF EXISTS "films";`, `DROP TABLE IF EXISTS "shots";`, "CREATE TABLE films", "Đậu và Bí"} {
		if !strings.Contains(s, want) {
			t.Errorf("dump lacks %q:\n%s", want, s)
		}
	}
	if strings.Index(s, "DROP TABLE") > strings.Index(s, "CREATE TABLE") {
		t.Error("tables are created before the old ones are dropped")
	}

	st := LocalDB(path)
	if !st.Exists || st.Tables != 2 || st.Bytes == 0 {
		t.Errorf("local db = %+v", st)
	}
}

type call struct {
	dir  string
	env  []string
	name string
	args []string
}

type fakeRun struct {
	mu    sync.Mutex
	calls []call
}

func (f *fakeRun) run(_ context.Context, dir string, env []string, out io.Writer, name string, args ...string) error {
	f.mu.Lock()
	f.calls = append(f.calls, call{dir, env, name, args})
	f.mu.Unlock()
	if name == "sh" {
		// the build writes the static folder
		_ = os.MkdirAll(filepath.Join(dir, "dist"), 0o755)
		return os.WriteFile(filepath.Join(dir, "dist", "index.html"), []byte("<h1>hi</h1>"), 0o644)
	}
	if len(args) > 2 && args[1] == "deploy" {
		io.WriteString(out, "Uploaded phim (1.2 sec)\n  https://phim.ngocp.workers.dev\nCurrent Version ID: abc\n")
	}
	return nil
}

func project(t *testing.T, deploy string) Resolved {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, Manifest), []byte(`{"deploy":`+deploy+`}`), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Read(dir)
	if err != nil || s == nil {
		t.Fatalf("read: %v %v", s, err)
	}
	return s.Resolve(dir, "ch_phim")
}

// Without credentials a dry run still builds and shows exactly what would go
// to Cloudflare — and nothing does.
func TestADryRunBuildsAndStopsBeforeCloudflare(t *testing.T) {
	r := project(t, `{"build":"npm run build","static":"dist","spa":true,"domain":"phim.bomclaw.org"}`)
	f := &fakeRun{}
	var log bytes.Buffer
	res, err := (&Cloudflare{Run: f.run}).Deploy(context.Background(), r, Options{DryRun: true}, &log)
	if err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	defer os.RemoveAll(filepath.Dir(res.ConfigPath))
	if len(f.calls) != 1 || f.calls[0].name != "sh" {
		t.Fatalf("calls = %+v, want only the build", f.calls)
	}
	raw, _ := os.ReadFile(res.ConfigPath)
	var cfg map[string]any
	_ = json.Unmarshal(raw, &cfg)
	if cfg["name"] != "phim" {
		t.Errorf("name = %v", cfg["name"])
	}
	assets, _ := cfg["assets"].(map[string]any)
	if assets["not_found_handling"] != "single-page-application" || assets["directory"] != r.StaticDir {
		t.Errorf("assets = %v", assets)
	}
	if !strings.Contains(string(raw), `"custom_domain": true`) {
		t.Errorf("no custom domain route:\n%s", raw)
	}
}

func fakeCloudflare(t *testing.T) (*Client, *[]string) {
	t.Helper()
	var mu sync.Mutex
	seen := []string{}
	created := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.Path)
		mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(403)
			io.WriteString(w, `{"success":false,"errors":[{"code":10000,"message":"Authentication error"}]}`)
			return
		}
		ok := func(v any) {
			b, _ := json.Marshal(map[string]any{"success": true, "result": v})
			w.Write(b)
		}
		switch {
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/d1/database"):
			if created {
				// The listing lags: it still says empty after an import.
				ok([]D1{{UUID: "d1-uuid", Name: "phim-db", FileSize: 8192, NumTables: 0}})
			} else {
				ok([]D1{})
			}
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/d1/database/d1-uuid"):
			ok(D1{UUID: "d1-uuid", Name: "phim-db", FileSize: 20480, NumTables: 2})
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/d1/database"):
			created = true
			ok(D1{UUID: "d1-uuid", Name: "phim-db"})
		case strings.HasSuffix(r.URL.Path, "/workers/scripts/phim/deployments"):
			ok(map[string]any{"deployments": []map[string]any{{"id": "v1", "created_on": "2026-11-08T08:00:00Z"}}})
		case strings.HasSuffix(r.URL.Path, "/workers/scripts/nothing/deployments"):
			w.WriteHeader(404)
			io.WriteString(w, `{"success":false,"errors":[{"code":10007,"message":"not found"}]}`)
		case strings.HasSuffix(r.URL.Path, "/workers/subdomain"):
			ok(map[string]string{"subdomain": "ngocp"})
		case strings.HasSuffix(r.URL.Path, "/workers/domains"):
			ok([]map[string]string{{"hostname": "phim.bomclaw.org", "service": "phim"}})
		default:
			w.WriteHeader(404)
			io.WriteString(w, `{"success":false}`)
		}
	}))
	t.Cleanup(srv.Close)
	c := NewClient(Creds{Token: "tok", AccountID: "acct"})
	c.Base = srv.URL
	return c, &seen
}

// The full path: the D1 database is created the first time, the local file is
// uploaded only because it was asked for, wrangler deploys with the
// credentials in its environment, and the live address comes back.
func TestADeployCreatesTheDatabaseUploadsItAndPublishes(t *testing.T) {
	r := project(t, `{"name":"phim","static":"dist","domain":"phim.bomclaw.org","database":{"file":"app.db","name":"phim-db"}}`)
	sampleDB(t, filepath.Join(r.ProjectDir, "app.db"))
	_ = os.MkdirAll(r.StaticDir, 0o755)
	_ = os.WriteFile(filepath.Join(r.StaticDir, "index.html"), []byte("x"), 0o644)
	if _, err := DumpForD1(filepath.Join(r.ProjectDir, "app.db")); err != nil {
		t.Skipf("sqlite3 CLI: %v", err)
	}
	c, seen := fakeCloudflare(t)
	f := &fakeRun{}
	var log bytes.Buffer
	res, err := (&Cloudflare{Client: c, Run: f.run}).Deploy(context.Background(), r, Options{WithDB: true}, &log)
	if err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	if res.URL != "https://phim.bomclaw.org" || !res.DBUploaded {
		t.Errorf("result = %+v", res)
	}
	if !strings.Contains(strings.Join(*seen, "\n"), "POST /accounts/acct/d1/database") {
		t.Errorf("the database was not created: %v", *seen)
	}
	var execd, deployed bool
	for _, c := range f.calls {
		joined := strings.Join(c.args, " ")
		if strings.Contains(joined, "d1 execute phim-db --remote --file") {
			execd = true
		}
		if strings.Contains(joined, "deploy --config") {
			deployed = true
			if !strings.Contains(strings.Join(c.env, " "), "CLOUDFLARE_API_TOKEN=tok") {
				t.Error("wrangler ran without the token")
			}
		}
	}
	if !execd || !deployed {
		t.Errorf("calls = %+v", f.calls)
	}

	// A site-only redeploy leaves the live data alone.
	f2 := &fakeRun{}
	if _, err := (&Cloudflare{Client: c, Run: f2.run}).Deploy(context.Background(), r, Options{}, io.Discard); err != nil {
		t.Fatal(err)
	}
	for _, c := range f2.calls {
		if strings.Contains(strings.Join(c.args, " "), "d1 execute") {
			t.Error("a deploy without --with-db touched the PROD database")
		}
	}
}

func TestProdStatusSaysWhatIsLiveAndWhere(t *testing.T) {
	c, _ := fakeCloudflare(t)
	r := project(t, `{"name":"phim","static":"dist","domain":"phim.bomclaw.org","database":{"file":"app.db","name":"phim-db"}}`)
	st := Prod(context.Background(), c, r)
	if st.Error != "" || !st.Deployed || st.URL != "https://phim.bomclaw.org" || st.WorkersDev != "https://phim.ngocp.workers.dev" {
		t.Errorf("status = %+v", st)
	}
	if st.Database == nil || st.Database.Exists {
		t.Errorf("db before creation = %+v", st.Database)
	}

	none := project(t, `{"name":"nothing","static":"dist"}`)
	if st := Prod(context.Background(), c, none); st.Deployed || st.Error != "" {
		t.Errorf("never-deployed = %+v", st)
	}
	if st := Prod(context.Background(), NewClient(Creds{}), r); st.Connected || !strings.Contains(st.Error, "CLOUDFLARE_API_TOKEN") {
		t.Errorf("no creds = %+v", st)
	}
}

// Providers are chosen by deploy.target. A planned one answers with what it
// will be, so an agent that reaches for Fly.io learns it is coming rather than
// that it made a typo.
func TestProvidersAreChosenByTargetAndPlannedOnesSaySo(t *testing.T) {
	p, err := For("")
	if err != nil || p.Name() != "cloudflare" {
		t.Fatalf("default provider = %v, %v", p, err)
	}
	if p, err := For("fly"); err != nil || p.Title() != "Fly.io" {
		t.Errorf("fly: %v %v", p, err)
	}
	if _, err := For("vercel"); err == nil || !strings.Contains(err.Error(), "planned") || !strings.Contains(err.Error(), "fly") {
		t.Errorf("vercel: %v", err)
	}
	if _, err := For("heroku"); err == nil || strings.Contains(err.Error(), "planned") {
		t.Errorf("heroku: %v", err)
	}
	if ok, hint := (&Cloudflare{Client: NewClient(Creds{})}).Connected(); ok || !strings.Contains(hint, "CLOUDFLARE_API_TOKEN") {
		t.Errorf("connected without creds: %v %q", ok, hint)
	}
}

// Right after an import the D1 listing still reported "0 tables" for a
// database that held the data; status reads the database's own endpoint.
func TestD1StatusIsReadFromTheDatabaseNotTheLaggingListing(t *testing.T) {
	c, _ := fakeCloudflare(t)
	if _, err := c.EnsureD1(context.Background(), "phim-db"); err != nil {
		t.Fatal(err)
	}
	d, err := c.FindD1(context.Background(), "phim-db")
	if err != nil || d == nil || d.NumTables != 2 || d.FileSize != 20480 {
		t.Errorf("d1 = %+v, %v", d, err)
	}
}
