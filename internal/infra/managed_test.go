package infra

import (
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAStoppedAppIsNotACrashloopWhateverItsCount(t *testing.T) {
	cases := []struct {
		status   string
		restarts int
		unstable int
		want     bool
	}{
		{"stopped", 565883, 0, false}, // okx-bot, stopped by the owner on 2026-11-06
		{"online", 565883, 0, false},
		{"errored", 565883, 0, true},
		{"waiting restart", 60, 0, true},
		{"launching", 1, 3, true},
		{"errored", 3, 0, false},
	}
	for _, c := range cases {
		if got := crashloop(c.status, c.restarts, c.unstable); got != c.want {
			t.Errorf("crashloop(%q, %d, %d) = %v", c.status, c.restarts, c.unstable, got)
		}
	}
}

func procargs(argc int, exe string, argv, env []string) []byte {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, uint32(argc))
	b = append(b, exe...)
	b = append(b, 0, 0, 0, 0)
	for _, s := range append(argv, env...) {
		b = append(append(b, s...), 0)
	}
	return append(b, 0, 0)
}

func TestProcArgsKeepsArgumentsWholeAndSplitsTheEnvironment(t *testing.T) {
	raw := procargs(3, "/n/bin/node", []string{"node", "server.js", "--title=a b"}, []string{"PORT=8731", "API_KEY=x=y"})
	exe, argv, env, err := parseProcArgs(raw)
	if err != nil {
		t.Fatal(err)
	}
	if exe != "/n/bin/node" || len(argv) != 3 || argv[2] != "--title=a b" {
		t.Errorf("exe %q argv %q", exe, argv)
	}
	if env["PORT"] != "8731" || env["API_KEY"] != "x=y" {
		t.Errorf("env %v", env)
	}
	if _, _, _, err := parseProcArgs(procargs(5, "/x", []string{"a"}, nil)); err == nil {
		t.Error("a short argv must be an error, not a truncated command")
	}
}

func TestTheProfileConfinesWritesAndHidesSecrets(t *testing.T) {
	in := &Infra{Home: "/Users/u"}
	p := in.Profile("/Users/u/projects/shop")
	for _, want := range []string{
		`(deny file-write* (subpath "/Users/u"))`,
		`(subpath "/Users/u/projects/shop")`,
		`(subpath "/Users/u/.ssh")`,
		`(subpath "/Users/u/.goterm-shared/data")`,
		`(subpath "/Users/u/.pm2")`,
	} {
		if !strings.Contains(p, want) {
			t.Errorf("profile lacks %s:\n%s", want, p)
		}
	}
	// The deny for secrets comes after the allow for the project, so a
	// project cannot be used to reach into them (SBPL: last match wins).
	if strings.Index(p, "credentials") < strings.Index(p, `"/Users/u/projects/shop"`) {
		t.Error("secret denies must follow the project allow")
	}
	// A project inside a denied folder keeps its own folder.
	if q := in.Profile("/Users/u/.goterm/w"); strings.Contains(q, `(subpath "/Users/u/.goterm")`+"\n") {
		t.Errorf("project folder denied to itself:\n%s", q)
	}
}

func TestAnEntryStartsCleanAndSandboxed(t *testing.T) {
	home := t.TempDir()
	proj := filepath.Join(home, "shop")
	os.MkdirAll(filepath.Join(proj, "node_modules", ".bin"), 0o755)
	vite := filepath.Join(proj, "node_modules", ".bin", "vite")
	os.WriteFile(vite, []byte("#!/bin/sh\n"), 0o755)
	in := &Infra{Home: home}

	e, err := in.entry(App{Name: "shop-web", Project: "shop", Cwd: proj, Command: []string{"vite", "--port", "5173"}, Env: map[string]string{"PORT": "5173"}})
	if err != nil {
		t.Fatal(err)
	}
	if e.Script != envBin || e.Args[0] != "-i" || e.Interpreter != "none" || e.Namespace != "shop" {
		t.Fatalf("entry: %+v", e)
	}
	joined := strings.Join(e.Args, " ")
	for _, want := range []string{"HOST=127.0.0.1", "PORT=5173", "HOME=" + home, sandboxExec + " -f ", vite + " --port 5173"} {
		if !strings.Contains(joined, want) {
			t.Errorf("args lack %q: %s", want, joined)
		}
	}
	if !sandboxed(e.Script, e.Args) {
		t.Error("not detected as sandboxed")
	}
	if b, err := os.ReadFile(filepath.Join(in.ManagedDir(), "shop-web.sb")); err != nil || !strings.Contains(string(b), proj) {
		t.Errorf("profile: %v %s", err, b)
	}

	e2, _ := in.entry(App{Name: "raw", Project: "shop", Cwd: proj, Command: []string{vite}, NoSandbox: true})
	if sandboxed(e2.Script, e2.Args) || strings.Contains(strings.Join(e2.Args, " "), sandboxExec) {
		t.Errorf("no-sandbox entry is sandboxed: %v", e2.Args)
	}
	if e2.Args[0] != "-i" {
		t.Error("no-sandbox must still start with a clean environment")
	}
}

func TestUpsertMovesAnAppBetweenProjects(t *testing.T) {
	in := &Infra{Home: t.TempDir(), globFiles: filepath.Glob}
	os.MkdirAll(in.ManagedDir(), 0o755)
	if _, err := in.upsert(pm2Entry{Name: "web", Namespace: "a"}); err != nil {
		t.Fatal(err)
	}
	in.upsert(pm2Entry{Name: "api", Namespace: "a"})
	in.upsert(pm2Entry{Name: "web", Namespace: "b", Cwd: "/new"})

	a, _ := readEcosystem(in.ecosystemPath("a"))
	b, _ := readEcosystem(in.ecosystemPath("b"))
	if len(a.Apps) != 1 || a.Apps[0].Name != "api" {
		t.Errorf("a: %+v", a.Apps)
	}
	if len(b.Apps) != 1 || b.Apps[0].Cwd != "/new" {
		t.Errorf("b: %+v", b.Apps)
	}
	raw, _ := os.ReadFile(in.ecosystemPath("b"))
	var generic map[string]any
	if json.Unmarshal(raw, &generic) != nil || generic["apps"] == nil {
		t.Errorf("not a pm2 ecosystem: %s", raw)
	}
}

func TestRunRefusesWhatTheSandboxCannotContain(t *testing.T) {
	in := &Infra{Home: t.TempDir(), PM2Bin: "/bin/true"}
	cases := map[string]App{
		"bad name":     {Name: "Web App", Cwd: in.Home + "/x", Command: []string{"node"}},
		"relative cwd": {Name: "web", Cwd: "x", Command: []string{"node"}},
		"home as cwd":  {Name: "web", Cwd: in.Home, Command: []string{"node"}},
		"no command":   {Name: "web", Cwd: in.Home},
	}
	for name, a := range cases {
		if _, err := in.RunApp(t.Context(), a); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestAJobWhoseProgramIsGoneIsBrokenAndOnlyThatCanBeRemoved(t *testing.T) {
	in, f := newFake(t)
	in.statFile = func(p string) (os.FileInfo, error) {
		if p == "/bin/job" {
			return nil, os.ErrNotExist
		}
		return os.Stat("/")
	}
	s := in.Snapshot(t.Context())
	var job, gw Service
	for _, sv := range s.Services {
		switch sv.Label {
		case "com.ngocp.job":
			job = sv
		case "com.bomclaw.gateway":
			gw = sv
		}
	}
	if job.Missing != "program /bin/job" || gw.Missing != "" {
		t.Fatalf("missing: job %q gw %q", job.Missing, gw.Missing)
	}
	if _, err := in.Do(t.Context(), Action{Kind: KindLaunchd, Target: "com.bomclaw.gateway", Verb: "remove"}); err == nil {
		t.Error("removed a bomclaw agent")
	}
	plist := filepath.Join(in.agentsDir(), "com.ngocp.job.plist")
	r, err := in.Do(t.Context(), Action{Kind: KindLaunchd, Target: "com.ngocp.job", Verb: "remove"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(plist); !os.IsNotExist(err) {
		t.Error("plist still in LaunchAgents")
	}
	if _, err := os.Stat(filepath.Join(in.Home, ".goterm-shared", "pm2", "backup", "launchd", "com.ngocp.job.plist")); err != nil {
		t.Errorf("not backed up: %v (%s)", err, r.Ran)
	}
	if !strings.Contains(strings.Join(f.ran, "\n"), "launchctl bootout gui/") {
		t.Errorf("not unloaded: %v", f.ran)
	}
}

func TestAGlobalCLIsDaemonIsAToolNotAStray(t *testing.T) {
	if !isToolPath("/Users/u/.nvm/versions/node/v24.13.0/lib/node_modules/agent-browser/bin/agent-browser-darwin-arm64") {
		t.Error("agent-browser daemon not a tool")
	}
	if isToolPath("/Users/u/projects/shop/node_modules/.bin/vite") || isToolPath("/n/bin/node") {
		t.Error("a project's server taken for a tool")
	}
}

func TestAGrantOpensOneFileAfterTheDenies(t *testing.T) {
	in := &Infra{Home: "/Users/u"}
	p := in.Profile("/Users/u/p", []string{"/Users/u/.bomclaw/.env"}, []string{"/Users/u/.goterm-shared/data/coord.db"})
	deny := strings.Index(p, `(subpath "/Users/u/.bomclaw")`)
	read := strings.Index(p, "(allow file-read*\n  (subpath \"/Users/u/.bomclaw/.env\")")
	write := strings.Index(p, "(allow file-read* file-write*\n  (subpath \"/Users/u/.goterm-shared/data/coord.db\")")
	if deny < 0 || read < deny || write < deny {
		t.Errorf("grants must follow the denies:\n%s", p)
	}
	if _, err := (&Infra{Home: "/Users/u", PM2Bin: "/bin/true"}).RunApp(t.Context(),
		App{Name: "x", Cwd: "/tmp", Command: []string{"true"}, AllowRead: []string{"/Users/u"}}); err == nil {
		t.Error("a grant of all of home was accepted")
	}
}

func TestPM2SaidDropsTheColoredTable(t *testing.T) {
	out := "\x1b[32m[PM2]\x1b[39m Starting /x in fork_mode\n" +
		"┌────┬──────┐\n│ \x1b[1m\x1b[36m0\x1b[39m\x1b[22m  │ okx-bot │\n└────┴──────┘\n"
	if got := pm2Said(out); got != "[PM2] Starting /x in fork_mode" {
		t.Errorf("pm2Said = %q", got)
	}
}
