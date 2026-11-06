package infra

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fake answers the commands Snapshot runs, and records every command.
type fake struct {
	mu    sync.Mutex
	ran   []string
	jlist string
}

func (f *fake) run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	f.ran = append(f.ran, filepath.Base(name)+" "+strings.Join(args, " "))
	f.mu.Unlock()
	switch filepath.Base(name) {
	case "pm2":
		if args[0] == "jlist" {
			return []byte(f.jlist), nil
		}
		return []byte("[PM2] done\n"), nil
	case "launchctl":
		if args[0] == "list" {
			return []byte("PID\tStatus\tLabel\n111\t0\tcom.bomclaw.gateway\n-\t78\tcom.ngocp.job\n"), nil
		}
		return nil, nil
	case "plutil":
		switch filepath.Base(args[len(args)-1]) {
		case "com.bomclaw.gateway.plist":
			return []byte(`{"Label":"com.bomclaw.gateway","ProgramArguments":["/x/bomclaw","gateway","--token","s3cret","--api-key=abc"],
				"EnvironmentVariables":{"TELEGRAM_TOKEN":"leak-me"},"KeepAlive":true,"StandardOutPath":"/tmp/gw.log"}`), nil
		case "com.ngocp.job.plist":
			return []byte(`{"Label":"com.ngocp.job","ProgramArguments":["/bin/job"],"StartCalendarInterval":{"Hour":7},
				"WorkingDirectory":"/home/p/projects/shop"}`), nil
		}
	case "lsof":
		if args[0] == "-nP" {
			return []byte("p222\ncnode\nn127.0.0.1:8795\np333\ncpython3\nn*:8765\np444\ncrapportd\nn*:63147\n" +
				"p111\ncbomclaw\nn127.0.0.1:18789\n"), nil
		}
		return []byte("p222\nn/home/p/projects/film\np333\nn/home/p/projects/radar\np111\nn/\n"), nil
	case "ps":
		return []byte("111 1 /x/bomclaw\n222 1 /n/node\n333 1 /usr/bin/python3\n444 1 /usr/libexec/rapportd\n555 111 /x/child\n"), nil
	case "sysctl":
		switch args[1] {
		case "vm.loadavg":
			return []byte("{ 1.50 2.00 2.50 }\n"), nil
		case "hw.memsize":
			return []byte("17179869184\n"), nil
		case "hw.ncpu":
			return []byte("10\n"), nil
		}
	case "vm_stat":
		return []byte("Mach Virtual Memory Statistics: (page size of 16384 bytes)\nPages free: 1000.\nPages inactive: 1000.\n"), nil
	}
	return nil, fmt.Errorf("unexpected %s %v", name, args)
}

const jlist = `[{"name":"film-studio","pid":222,"monit":{"memory":38928384,"cpu":1.5},
  "pm2_env":{"status":"online","namespace":"phim-ngan-25d","pm_uptime":1,"restart_time":1,"unstable_restarts":0,
   "pm_cwd":"/home/p/projects/film","pm_exec_path":"/home/p/projects/film/bin/film","pm_out_log_path":"OUT",
   "env":{"OPENAI_API_KEY":"leak-me"}}},
 {"name":"okx-bot","pid":0,"monit":{"memory":0,"cpu":0},
  "pm2_env":{"status":"waiting restart","namespace":"default","restart_time":565731,"unstable_restarts":1,"pm_cwd":"/home/p/ws/okx"}}]`

func newFake(t *testing.T) (*Infra, *fake) {
	t.Helper()
	home := t.TempDir()
	agents := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(agents, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"com.bomclaw.gateway.plist", "com.ngocp.job.plist"} {
		if err := os.WriteFile(filepath.Join(agents, f), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	f := &fake{jlist: jlist}
	in := &Infra{Run: f.run, Home: home, PM2Bin: "/n/bin/pm2", UID: 501, Now: time.Now,
		readFile: os.ReadFile, globFiles: filepath.Glob,
		Projects: map[string]string{"/home/p/projects/film": "phim-ngan-25d", "/home/p/projects/radar": "radar", "/home/p/projects/shop": "shop"}}
	return in, f
}

func TestSnapshotTiesEverythingTogether(t *testing.T) {
	in, _ := newFake(t)
	s := in.Snapshot(context.Background())

	if len(s.PM2) != 2 || s.PM2[1].Name != "film-studio" || s.PM2[1].Namespace != "phim-ngan-25d" {
		t.Fatalf("pm2: %+v", s.PM2)
	}
	film, okx := s.PM2[1], s.PM2[0]
	if len(film.Ports) != 1 || film.Ports[0] != 8795 || film.Project != "phim-ngan-25d" || film.Mem != 38928384 {
		t.Errorf("film-studio: %+v", film)
	}
	if !okx.Crashloop || film.Crashloop {
		t.Errorf("crashloop: okx %v film %v", okx.Crashloop, film.Crashloop)
	}

	var gw, job Service
	for _, sv := range s.Services {
		switch sv.Label {
		case "com.bomclaw.gateway":
			gw = sv
		case "com.ngocp.job":
			job = sv
		}
	}
	if gw.Kind != "bomclaw" || !gw.Loaded || gw.PID != 111 || !gw.KeepAlive || len(gw.Ports) != 1 {
		t.Errorf("gateway: %+v", gw)
	}
	if !job.Scheduled || job.LastExit != 78 || job.PID != 0 || job.Project != "shop" {
		t.Errorf("job: %+v", job)
	}

	byPort := map[int]Listener{}
	for _, l := range s.Listeners {
		byPort[l.Port] = l
	}
	if byPort[8795].ManagedBy != "pm2:film-studio" || byPort[18789].ManagedBy != "launchd:com.bomclaw.gateway" {
		t.Errorf("managed: %+v", byPort)
	}
	if l := byPort[8765]; l.ManagedBy != "" || l.Project != "radar" || l.System {
		t.Errorf("stray server: %+v", l)
	}
	if !byPort[63147].System {
		t.Error("rapportd should be marked system")
	}
	if s.Host.Load[0] != 1.5 || s.Host.CPUs != 10 || s.Host.MemUsed != 17179869184-2000*16384 {
		t.Errorf("host: %+v", s.Host)
	}
}

// Neither pm2's env nor a plist's EnvironmentVariables reaches the output, and
// credential-looking arguments are masked.
func TestSnapshotLeaksNoSecrets(t *testing.T) {
	in, _ := newFake(t)
	b, _ := json.Marshal(in.Snapshot(context.Background()))
	for _, leak := range []string{"leak-me", "s3cret", "abc", "OPENAI_API_KEY", "TELEGRAM_TOKEN"} {
		if strings.Contains(string(b), leak) {
			t.Errorf("snapshot contains %q", leak)
		}
	}
	if !strings.Contains(string(b), `"--token","•••"`) || !strings.Contains(string(b), `--api-key=•••`) {
		t.Errorf("arguments not masked: %s", b)
	}
}

func TestActionsOnlyReachWhatExists(t *testing.T) {
	in, f := newFake(t)
	ctx := context.Background()
	bad := []Action{
		{Kind: KindPM2, Target: "nope", Verb: "restart"},
		{Kind: KindPM2, Target: "film-studio", Verb: "exec"},
		{Kind: KindPM2, Target: "film-studio; rm -rf /", Verb: "restart"},
		{Kind: KindLaunchd, Target: "com.apple.Finder", Verb: "stop"},
		{Kind: KindPID, Target: "222", Verb: "kill"}, // managed by pm2: it would come back
		{Kind: KindPID, Target: "444", Verb: "kill"}, // system
		{Kind: KindPID, Target: "999", Verb: "kill"}, // not listening
		{Kind: KindPID, Target: "1", Verb: "kill"},
		{Kind: "shell", Target: "ls", Verb: "run"},
	}
	for _, a := range bad {
		f.ran = nil
		if _, err := in.Do(ctx, a); err == nil {
			t.Errorf("%+v was allowed", a)
		}
		for _, r := range f.ran {
			if !strings.HasPrefix(r, "pm2 jlist") && !strings.HasPrefix(r, "launchctl list") && !strings.HasPrefix(r, "plutil") &&
				!strings.HasPrefix(r, "lsof") && !strings.HasPrefix(r, "ps") && !strings.HasPrefix(r, "sysctl") && r != "vm_stat " {
				t.Errorf("%+v ran %q", a, r)
			}
		}
	}

	f.ran = nil
	if r, err := in.Do(ctx, Action{Kind: KindPM2, Target: "film-studio", Verb: "restart"}); err != nil || !r.OK {
		t.Fatalf("restart: %v %+v", err, r)
	}
	if !contains(f.ran, "pm2 restart film-studio") || !contains(f.ran, "pm2 save") {
		t.Errorf("ran %v", f.ran)
	}

	f.ran = nil
	if _, err := in.Do(ctx, Action{Kind: KindLaunchd, Target: "com.bomclaw.gateway", Verb: "restart"}); err != nil {
		t.Fatal(err)
	}
	if !contains(f.ran, "launchctl kickstart -k gui/501/com.bomclaw.gateway") {
		t.Errorf("ran %v", f.ran)
	}
	f.ran = nil
	if _, err := in.Do(ctx, Action{Kind: KindLaunchd, Target: "com.bomclaw.gateway", Verb: "stop"}); err != nil {
		t.Fatal(err)
	}
	if !contains(f.ran, "launchctl bootout gui/501/com.bomclaw.gateway") {
		t.Errorf("stop of a KeepAlive job must bootout: %v", f.ran)
	}
}

// Restarting the process that answers would cut off its own reply.
func TestRestartingItselfAnswersFirst(t *testing.T) {
	in, f := newFake(t)
	old := SelfLabel
	SelfLabel = "com.bomclaw.gateway"
	defer func() { SelfLabel = old }()
	r, err := in.Do(context.Background(), Action{Kind: KindLaunchd, Target: "com.bomclaw.gateway", Verb: "restart"})
	if err != nil || !r.Pending {
		t.Fatalf("%+v %v", r, err)
	}
	time.Sleep(700 * time.Millisecond)
	f.mu.Lock()
	defer f.mu.Unlock()
	if !contains(f.ran, "launchctl kickstart -k gui/501/com.bomclaw.gateway") {
		t.Errorf("never ran: %v", f.ran)
	}
}

func TestLogsComeFromTheTargetsOwnFiles(t *testing.T) {
	in, _ := newFake(t)
	log := filepath.Join(t.TempDir(), "out.log")
	var b strings.Builder
	for i := 0; i < 500; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	os.WriteFile(log, []byte(b.String()), 0o644)
	in.Run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if filepath.Base(name) == "pm2" && args[0] == "jlist" {
			return []byte(strings.Replace(jlist, `"OUT"`, fmt.Sprintf("%q", log), 1)), nil
		}
		return (&fake{jlist: jlist}).run(ctx, name, args...)
	}
	got, err := in.Logs(context.Background(), KindPM2, "film-studio", 3)
	if err != nil || got["out"] != "line 497\nline 498\nline 499" {
		t.Fatalf("%q %v", got["out"], err)
	}
	if _, err := in.Logs(context.Background(), KindPM2, "../../etc/passwd", 3); err == nil {
		t.Error("a name that is not an app read something")
	}
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}
