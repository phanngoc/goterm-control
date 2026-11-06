// Package infra is the owner's view of what is running on this machine, and
// the few things they may do about it.
//
// Before it, the answer to "what is running" was spread over four tools: pm2
// for some project services, launchd for the agents and others, nohup for
// whatever was started by hand, and nothing at all for a dev server left
// behind. On 2026-11-06 the machine had two copies of the same static server
// for one project, a launchd job the docs described that was never loaded, and
// a pm2 app restarting for the 565,000th time — none of it visible from the
// dashboard the owner actually looks at.
//
// Two rules hold everywhere here:
//
//   - Nothing is passed through that could carry a secret. pm2's jlist and a
//     plist both include the process environment; only named fields are read
//     out, and arguments that look like credentials are masked.
//   - Every action names its target from a fresh snapshot. A request to restart
//     "x" only runs if "x" is a pm2 app or a LaunchAgent that exists now; a kill
//     only reaches a pid that is listening now and nobody manages. There is no
//     path from a request to an arbitrary command.
package infra

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Runner runs a command and returns its stdout. Tests replace it.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

func execRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), "PATH="+pathWith(filepath.Dir(name)))
	var errb bytes.Buffer
	cmd.Stderr = &errb
	out, err := cmd.Output()
	if err != nil && errb.Len() > 0 {
		err = fmt.Errorf("%w: %s", err, strings.TrimSpace(cut(errb.String(), 400)))
	}
	return out, err
}

// pathWith puts dir in front of PATH. pm2 is a node script, and a launchd job
// (the dashboard is one) has a PATH with no node on it.
func pathWith(dir string) string {
	p := os.Getenv("PATH")
	if dir == "" || dir == "." {
		return p
	}
	return dir + string(os.PathListSeparator) + p
}

// Host is the machine at a glance.
type Host struct {
	Load      [3]float64 `json:"load"`
	CPUs      int        `json:"cpus"`
	MemTotal  int64      `json:"mem_total"`
	MemUsed   int64      `json:"mem_used"`
	DiskTotal int64      `json:"disk_total"`
	DiskFree  int64      `json:"disk_free"`
	BootedAt  time.Time  `json:"booted_at,omitempty"`
}

// PM2App is one pm2 process. Namespace is the project it belongs to.
type PM2App struct {
	Name        string  `json:"name"`
	Namespace   string  `json:"namespace"`
	Status      string  `json:"status"`
	PID         int     `json:"pid"`
	CPU         float64 `json:"cpu"`
	Mem         int64   `json:"mem"`
	StartedAt   int64   `json:"started_at"` // unix ms
	Restarts    int     `json:"restarts"`
	Unstable    int     `json:"unstable_restarts"`
	Cwd         string  `json:"cwd"`
	Script      string  `json:"script"`
	Interpreter string  `json:"interpreter,omitempty"`
	OutLog      string  `json:"out_log,omitempty"`
	ErrLog      string  `json:"err_log,omitempty"`
	Ports       []int   `json:"ports,omitempty"`
	Project     string  `json:"project,omitempty"`
	// Crashloop: restarting far more than a healthy service does. The thing
	// worth seeing first, which is why it is computed rather than left to the
	// reader of a restart count.
	Crashloop bool `json:"crashloop,omitempty"`
}

// Service is one LaunchAgent of this user.
type Service struct {
	Label     string   `json:"label"`
	Kind      string   `json:"kind"` // bomclaw | other
	Loaded    bool     `json:"loaded"`
	PID       int      `json:"pid,omitempty"`
	LastExit  int      `json:"last_exit"`
	Program   []string `json:"program"` // arguments, credentials masked
	Cwd       string   `json:"cwd,omitempty"`
	OutLog    string   `json:"out_log,omitempty"`
	ErrLog    string   `json:"err_log,omitempty"`
	KeepAlive bool     `json:"keep_alive"`
	Scheduled bool     `json:"scheduled"` // StartCalendarInterval / StartInterval: a job, not a server
	Ports     []int    `json:"ports,omitempty"`
	Plist     string   `json:"plist"`
	Project   string   `json:"project,omitempty"`
}

// Listener is one listening TCP socket, and who answers for it.
type Listener struct {
	Port    int    `json:"port"`
	Addr    string `json:"addr"`
	PID     int    `json:"pid"`
	Command string `json:"command"`
	Cwd     string `json:"cwd,omitempty"`
	// ManagedBy is "pm2:<name>", "launchd:<label>", or "" for a process
	// nothing will restart — and nothing will stop.
	ManagedBy string `json:"managed_by,omitempty"`
	System    bool   `json:"system,omitempty"` // macOS / app internals, hidden by default
	Project   string `json:"project,omitempty"`
}

// Snapshot is everything at once, taken together so the cross-references
// (which port is whose) agree.
type Snapshot struct {
	At        time.Time  `json:"at"`
	Host      Host       `json:"host"`
	PM2       []PM2App   `json:"pm2"`
	PM2Bin    string     `json:"pm2_bin,omitempty"`
	PM2Error  string     `json:"pm2_error,omitempty"`
	Services  []Service  `json:"services"`
	Listeners []Listener `json:"listeners"`
	Errors    []string   `json:"errors,omitempty"`
}

// Infra reads and acts. The zero value is not usable; use New.
type Infra struct {
	Run    Runner
	Home   string
	PM2Bin string // "" = discover
	UID    int
	Now    func() time.Time
	// Projects maps a project folder to its name (the room's), so whatever
	// runs inside one is shown as that project's.
	Projects  map[string]string
	readFile  func(string) ([]byte, error)
	globFiles func(string) ([]string, error)
}

// New uses the real machine.
func New() *Infra {
	home, _ := os.UserHomeDir()
	return &Infra{Run: execRunner, Home: home, UID: os.Getuid(), Now: time.Now,
		readFile: os.ReadFile, globFiles: filepath.Glob}
}

// pm2 finds the pm2 binary: $PM2_BIN, PATH, then the usual install places.
func (in *Infra) pm2() string {
	if in.PM2Bin != "" {
		return in.PM2Bin
	}
	if b := os.Getenv("PM2_BIN"); b != "" {
		return b
	}
	if p, err := exec.LookPath("pm2"); err == nil {
		return p
	}
	cands, _ := in.globFiles(filepath.Join(in.Home, ".nvm/versions/node/*/bin/pm2"))
	sort.Sort(sort.Reverse(sort.StringSlice(cands))) // newest node first
	cands = append(cands, "/opt/homebrew/bin/pm2", "/usr/local/bin/pm2")
	for _, c := range cands {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c
		}
	}
	return ""
}

// Snapshot reads everything. It never fails as a whole: a part that cannot be
// read is reported in Errors and the rest is still shown.
func (in *Infra) Snapshot(ctx context.Context) Snapshot {
	s := Snapshot{At: in.Now(), PM2: []PM2App{}, Services: []Service{}, Listeners: []Listener{}}
	s.Host = in.host(ctx)

	if bin := in.pm2(); bin == "" {
		s.PM2Error = "pm2 not found (PATH, ~/.nvm, /opt/homebrew)"
	} else {
		s.PM2Bin = bin
		apps, err := in.pm2List(ctx, bin)
		if err != nil {
			s.PM2Error = err.Error()
		}
		s.PM2 = apps
	}

	svcs, err := in.services(ctx)
	if err != nil {
		s.Errors = append(s.Errors, err.Error())
	}
	s.Services = svcs

	ls, err := in.listeners(ctx)
	if err != nil {
		s.Errors = append(s.Errors, err.Error())
	}
	s.Listeners = ls
	in.crossRef(ctx, &s)
	for i := range s.PM2 {
		s.PM2[i].Project = in.projectOf(s.PM2[i].Cwd)
	}
	for i := range s.Services {
		s.Services[i].Project = in.projectOf(s.Services[i].Cwd)
	}
	for i := range s.Listeners {
		s.Listeners[i].Project = in.projectOf(s.Listeners[i].Cwd)
	}
	return s
}

// projectOf names the project whose folder holds dir: the longest match, so a
// project nested in another is still its own.
func (in *Infra) projectOf(dir string) string {
	best, name := "", ""
	for d, n := range in.Projects {
		d = filepath.Clean(d)
		if (dir == d || strings.HasPrefix(dir, d+string(filepath.Separator))) && len(d) > len(best) {
			best, name = d, n
		}
	}
	return name
}

// ---------------------------------------------------------------- pm2

type jlistApp struct {
	Name  string `json:"name"`
	PID   int    `json:"pid"`
	Monit struct {
		Memory int64   `json:"memory"`
		CPU    float64 `json:"cpu"`
	} `json:"monit"`
	Env struct {
		Status      string `json:"status"`
		Namespace   string `json:"namespace"`
		Uptime      int64  `json:"pm_uptime"`
		Restarts    int    `json:"restart_time"`
		Unstable    int    `json:"unstable_restarts"`
		Cwd         string `json:"pm_cwd"`
		Script      string `json:"pm_exec_path"`
		Interpreter string `json:"exec_interpreter"`
		OutLog      string `json:"pm_out_log_path"`
		ErrLog      string `json:"pm_err_log_path"`
	} `json:"pm2_env"`
}

// crashloopRestarts: a service that has restarted this often is not flapping
// once, it is failing on every start.
const crashloopRestarts = 50

func (in *Infra) pm2List(ctx context.Context, bin string) ([]PM2App, error) {
	out, err := in.Run(ctx, bin, "jlist")
	if err != nil {
		return []PM2App{}, fmt.Errorf("pm2 jlist: %w", err)
	}
	// pm2 may print a banner before the JSON when its daemon was just started.
	if i := bytes.IndexByte(out, '['); i > 0 {
		out = out[i:]
	}
	var raw []jlistApp
	if err := json.Unmarshal(out, &raw); err != nil {
		return []PM2App{}, fmt.Errorf("pm2 jlist: %w", err)
	}
	apps := make([]PM2App, 0, len(raw))
	for _, a := range raw {
		ns := a.Env.Namespace
		if ns == "" {
			ns = "default"
		}
		apps = append(apps, PM2App{
			Name: a.Name, Namespace: ns, Status: a.Env.Status, PID: a.PID,
			CPU: a.Monit.CPU, Mem: a.Monit.Memory, StartedAt: a.Env.Uptime,
			Restarts: a.Env.Restarts, Unstable: a.Env.Unstable,
			Cwd: a.Env.Cwd, Script: a.Env.Script, Interpreter: a.Env.Interpreter,
			OutLog: a.Env.OutLog, ErrLog: a.Env.ErrLog,
			Crashloop: a.Env.Restarts >= crashloopRestarts && a.Env.Status != "online" || a.Env.Unstable > 0,
		})
	}
	sort.Slice(apps, func(i, j int) bool {
		if apps[i].Namespace != apps[j].Namespace {
			return apps[i].Namespace < apps[j].Namespace
		}
		return apps[i].Name < apps[j].Name
	})
	return apps, nil
}

// ---------------------------------------------------------------- launchd

type plist struct {
	Label                 string            `json:"Label"`
	ProgramArguments      []string          `json:"ProgramArguments"`
	Program               string            `json:"Program"`
	WorkingDirectory      string            `json:"WorkingDirectory"`
	StandardOutPath       string            `json:"StandardOutPath"`
	StandardErrorPath     string            `json:"StandardErrorPath"`
	KeepAlive             json.RawMessage   `json:"KeepAlive"`
	StartCalendarInterval json.RawMessage   `json:"StartCalendarInterval"`
	StartInterval         int               `json:"StartInterval"`
	EnvironmentVariables  map[string]string `json:"-"` // never read: it holds secrets
}

func (in *Infra) agentsDir() string { return filepath.Join(in.Home, "Library", "LaunchAgents") }

func (in *Infra) services(ctx context.Context) ([]Service, error) {
	files, _ := in.globFiles(filepath.Join(in.agentsDir(), "*.plist"))
	loaded := map[string][2]int{} // label -> pid, last exit
	if out, err := in.Run(ctx, "launchctl", "list"); err == nil {
		for _, line := range strings.Split(string(out), "\n")[1:] {
			f := strings.Fields(line)
			if len(f) != 3 {
				continue
			}
			pid, _ := strconv.Atoi(f[0]) // "-" -> 0
			code, _ := strconv.Atoi(f[1])
			loaded[f[2]] = [2]int{pid, code}
		}
	}
	var out []Service
	var errs []string
	for _, f := range files {
		raw, err := in.Run(ctx, "plutil", "-convert", "json", "-o", "-", f)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", filepath.Base(f), err))
			continue
		}
		var p plist
		if err := json.Unmarshal(raw, &p); err != nil || p.Label == "" {
			continue
		}
		args := p.ProgramArguments
		if len(args) == 0 && p.Program != "" {
			args = []string{p.Program}
		}
		s := Service{
			Label: p.Label, Kind: "other", Program: maskArgs(args), Cwd: p.WorkingDirectory,
			OutLog: p.StandardOutPath, ErrLog: p.StandardErrorPath, Plist: f,
			KeepAlive: truthy(p.KeepAlive),
			Scheduled: len(p.StartCalendarInterval) > 0 || p.StartInterval > 0,
		}
		if strings.HasPrefix(p.Label, "com.bomclaw") {
			s.Kind = "bomclaw"
		}
		if st, ok := loaded[p.Label]; ok {
			s.Loaded, s.PID, s.LastExit = true, st[0], st[1]
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Label < out[j].Label
	})
	if out == nil {
		out = []Service{}
	}
	if len(errs) > 0 {
		return out, fmt.Errorf("launchd: %s", strings.Join(errs, "; "))
	}
	return out, nil
}

func truthy(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	return s != "" && s != "false" && s != "null"
}

var secretArg = regexp.MustCompile(`(?i)(token|secret|password|passwd|api[_-]?key|auth)[^=]*=`)

// maskArgs hides values that look like credentials: `--token=abc`, or the
// argument after `--token`.
func maskArgs(args []string) []string {
	out := make([]string, len(args))
	hideNext := false
	for i, a := range args {
		switch {
		case hideNext:
			out[i], hideNext = "•••", false
		case secretArg.MatchString(a):
			out[i] = a[:secretArg.FindStringIndex(a)[1]] + "•••"
		default:
			out[i] = a
			low := strings.ToLower(a)
			hideNext = strings.HasPrefix(low, "-") && regexp.MustCompile(`(token|secret|password|api-?key)$`).MatchString(low)
		}
	}
	return out
}

// ---------------------------------------------------------------- ports

// systemPaths: a process whose binary lives here belongs to macOS or to an
// installed app (OrbStack, Chrome, Control Center), not to anything the owner
// runs. Shown only on request. Decided by path, not name: lsof truncates names.
var systemPaths = []string{"/System/", "/usr/libexec/", "/usr/sbin/", "/Applications/", "/Library/", "/sbin/"}

func isSystemPath(exe string) bool {
	for _, p := range systemPaths {
		if strings.HasPrefix(exe, p) {
			return true
		}
	}
	return false
}

func (in *Infra) listeners(ctx context.Context) ([]Listener, error) {
	out, err := in.Run(ctx, "lsof", "-nP", "-iTCP", "-sTCP:LISTEN", "-F", "pcn")
	if err != nil && len(out) == 0 {
		return []Listener{}, fmt.Errorf("lsof: %w", err)
	}
	type key struct{ pid, port int }
	seen := map[key]bool{}
	var res []Listener
	var pid int
	var command string
	for _, line := range strings.Split(string(out), "\n") {
		if line == "" {
			continue
		}
		switch line[0] {
		case 'p':
			pid, _ = strconv.Atoi(line[1:])
		case 'c':
			command = line[1:]
		case 'n':
			addr := line[1:]
			i := strings.LastIndexByte(addr, ':')
			if i < 0 {
				continue
			}
			port, err := strconv.Atoi(addr[i+1:])
			if err != nil || seen[key{pid, port}] {
				continue
			}
			seen[key{pid, port}] = true
			res = append(res, Listener{Port: port, Addr: addr[:i], PID: pid, Command: command})
		}
	}
	sort.Slice(res, func(i, j int) bool { return res[i].Port < res[j].Port })
	if res == nil {
		res = []Listener{}
	}
	return res, nil
}

// crossRef ties ports to the pm2 app or LaunchAgent that owns the process,
// walking up the parent chain: a preview server a gateway spawned is the
// gateway's, and stops when it does.
func (in *Infra) crossRef(ctx context.Context, s *Snapshot) {
	owner := map[int]string{}
	for _, a := range s.PM2 {
		if a.PID > 0 {
			owner[a.PID] = "pm2:" + a.Name
		}
	}
	for _, sv := range s.Services {
		if sv.PID > 0 {
			owner[sv.PID] = "launchd:" + sv.Label
		}
	}
	parents, exes := in.processes(ctx)
	for i := range s.Listeners {
		s.Listeners[i].System = isSystemPath(exes[s.Listeners[i].PID])
	}
	cwds := in.cwds(ctx, s.Listeners)
	type portOwner struct {
		port  int
		owner string
	}
	shown := map[portOwner]bool{}
	kept := s.Listeners[:0]
	for i := range s.Listeners {
		l := s.Listeners[i]
		l.Cwd = cwds[l.PID]
		for p, hops := l.PID, 0; p > 1 && hops < 6; p, hops = parents[p], hops+1 {
			if o, ok := owner[p]; ok {
				l.ManagedBy = o
				break
			}
		}
		// nginx's master and workers all listen on the same port: one row.
		if l.ManagedBy != "" && shown[portOwner{l.Port, l.ManagedBy}] {
			continue
		}
		shown[portOwner{l.Port, l.ManagedBy}] = true
		kept = append(kept, l)
		if l.ManagedBy == "" {
			continue
		}
		kind, name, _ := strings.Cut(l.ManagedBy, ":")
		if kind == "pm2" {
			for j := range s.PM2 {
				if s.PM2[j].Name == name && !containsInt(s.PM2[j].Ports, l.Port) {
					s.PM2[j].Ports = append(s.PM2[j].Ports, l.Port)
				}
			}
		} else {
			for j := range s.Services {
				if s.Services[j].Label == name && !containsInt(s.Services[j].Ports, l.Port) {
					s.Services[j].Ports = append(s.Services[j].Ports, l.Port)
				}
			}
		}
	}
	s.Listeners = kept
}

// processes maps every pid to its parent and its executable path.
func (in *Infra) processes(ctx context.Context) (map[int]int, map[int]string) {
	out, err := in.Run(ctx, "ps", "-axo", "pid=,ppid=,comm=")
	parents, exes := map[int]int{}, map[int]string{}
	if err != nil {
		return parents, exes
	}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		p, _ := strconv.Atoi(f[0])
		pp, _ := strconv.Atoi(f[1])
		parents[p] = pp
		exes[p] = strings.Join(f[2:], " ")
	}
	return parents, exes
}

func (in *Infra) cwds(ctx context.Context, ls []Listener) map[int]string {
	var pids []string
	seen := map[int]bool{}
	for _, l := range ls {
		if !l.System && !seen[l.PID] {
			seen[l.PID] = true
			pids = append(pids, strconv.Itoa(l.PID))
		}
	}
	m := map[int]string{}
	if len(pids) == 0 {
		return m
	}
	out, _ := in.Run(ctx, "lsof", "-a", "-d", "cwd", "-F", "pn", "-p", strings.Join(pids, ","))
	var pid int
	for _, line := range strings.Split(string(out), "\n") {
		if line == "" {
			continue
		}
		switch line[0] {
		case 'p':
			pid, _ = strconv.Atoi(line[1:])
		case 'n':
			m[pid] = line[1:]
		}
	}
	return m
}

func containsInt(l []int, v int) bool {
	for _, x := range l {
		if x == v {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------- host

func (in *Infra) host(ctx context.Context) Host {
	var h Host
	if out, err := in.Run(ctx, "sysctl", "-n", "vm.loadavg"); err == nil {
		f := strings.Fields(strings.Trim(strings.TrimSpace(string(out)), "{}"))
		for i := 0; i < 3 && i < len(f); i++ {
			h.Load[i], _ = strconv.ParseFloat(f[i], 64)
		}
	}
	if out, err := in.Run(ctx, "sysctl", "-n", "hw.ncpu"); err == nil {
		h.CPUs, _ = strconv.Atoi(strings.TrimSpace(string(out)))
	}
	if out, err := in.Run(ctx, "sysctl", "-n", "hw.memsize"); err == nil {
		h.MemTotal, _ = strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	}
	if out, err := in.Run(ctx, "vm_stat"); err == nil && h.MemTotal > 0 {
		h.MemUsed = h.MemTotal - vmFree(string(out))
	}
	if out, err := in.Run(ctx, "sysctl", "-n", "kern.boottime"); err == nil {
		if m := regexp.MustCompile(`sec = (\d+)`).FindStringSubmatch(string(out)); m != nil {
			sec, _ := strconv.ParseInt(m[1], 10, 64)
			h.BootedAt = time.Unix(sec, 0)
		}
	}
	h.DiskTotal, h.DiskFree = diskUsage("/")
	return h
}

// vmFree is what macOS can hand out without paging: free + inactive +
// speculative + purgeable pages. "Free" alone reads near zero on any Mac that
// has been up an hour and says nothing.
func vmFree(vmstat string) int64 {
	page := int64(16384)
	if m := regexp.MustCompile(`page size of (\d+) bytes`).FindStringSubmatch(vmstat); m != nil {
		page, _ = strconv.ParseInt(m[1], 10, 64)
	}
	var pages int64
	for _, k := range []string{"Pages free", "Pages inactive", "Pages speculative", "Pages purgeable"} {
		if m := regexp.MustCompile(k + `:\s+(\d+)`).FindStringSubmatch(vmstat); m != nil {
			n, _ := strconv.ParseInt(m[1], 10, 64)
			pages += n
		}
	}
	return pages * page
}

func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
