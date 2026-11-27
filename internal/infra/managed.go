package infra

// Managed apps: a project's long-running server, run by pm2 inside a sandbox.
//
// Before this, "put it in pm2" meant hand-writing a .cjs ecosystem file, and
// most servers never got there: on 2026-11-27 six were listening with nothing
// to restart or stop them (a Vite dev server from August, three agent-started
// node servers, a preview left over from a run). Run and Adopt make the managed
// way the short way: one command writes the entry, the sandbox profile and the
// log paths, and starts it.
//
// The sandbox (macOS sandbox-exec, the same mechanism as internal/fsguard):
//
//   - writes only inside the project folder, temp dirs and tool caches;
//   - cannot read credentials: ~/.ssh, ~/.aws, the agents' and the shared
//     secrets stores, pm2's own dump (it holds every app's environment);
//   - starts with a clean environment: /usr/bin/env -i, then only PATH, HOME,
//     HOST, LANG, USER, TMPDIR and what it is given. pm2's filter_env does not
//     do this — pm2 merges in the environment of whoever ran `pm2 start`, and
//     a test app on 2026-11-27 saw the calling shell's session tokens and
//     SSH agent socket. env -i execs in place, so pm2 still tracks the app.
//
// What it cannot do: confine listening to loopback. SBPL only knows "*" and
// "localhost" as hosts, and "localhost" also matches a wildcard bind (tested
// 2026-11-27). HOST=127.0.0.1 is passed instead, and a managed app that
// listens on all interfaces is flagged (Listener.Exposed).

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// sandboxExec runs a command under an SBPL profile, then execs it in place:
// the pid pm2 tracks is the program's own. envBin -i does the same with an
// emptied environment.
const (
	sandboxExec = "/usr/bin/sandbox-exec"
	envBin      = "/usr/bin/env"
)

// App is one managed service.
type App struct {
	Name    string            `json:"name"`
	Project string            `json:"project"`
	Cwd     string            `json:"cwd"`
	Command []string          `json:"command"` // argv; [0] is resolved to an absolute path
	Env     map[string]string `json:"env,omitempty"`
	// MaxMemory restarts the app above this RSS (pm2 syntax, e.g. "1G").
	MaxMemory string `json:"max_memory,omitempty"`
	// AllowRead / AllowWrite open named paths the profile otherwise denies —
	// one file, not its folder: film-studio reads ~/.bomclaw/.env for its
	// Telegram token and writes the coord database through `bomclaw task new`.
	// Granted after the denies, so they win (SBPL: last match).
	AllowRead  []string `json:"allow_read,omitempty"`
	AllowWrite []string `json:"allow_write,omitempty"`
	// NoSandbox runs it unconfined. For a program the sandbox breaks; the
	// entry still lives with the others and is still restarted.
	NoSandbox bool `json:"no_sandbox,omitempty"`
}

// ManagedDir holds the generated ecosystem files, profiles and logs. The
// hand-written <project>.config.cjs files sit one level up and are left alone.
func (in *Infra) ManagedDir() string {
	return filepath.Join(in.Home, ".goterm-shared", "pm2", "managed")
}

var appName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

// slug turns a folder or project name into an app name.
func slug(s string) string {
	s = strings.ToLower(s)
	s = regexp.MustCompile(`[^a-z0-9._-]+`).ReplaceAllString(s, "-")
	s = strings.Trim(s, "-._")
	if len(s) > 63 {
		s = s[:63]
	}
	return s
}

// secretPaths are never readable from inside the sandbox.
func (in *Infra) secretPaths() []string {
	h := in.Home
	return []string{
		filepath.Join(h, ".ssh"), filepath.Join(h, ".aws"), filepath.Join(h, ".gnupg"),
		filepath.Join(h, ".kube"), filepath.Join(h, ".docker"), filepath.Join(h, ".netrc"),
		filepath.Join(h, ".config", "gh"), filepath.Join(h, ".config", "gcloud"),
		filepath.Join(h, "Library", "Keychains"),
		filepath.Join(h, ".bomclaw"), filepath.Join(h, ".bomclaw2"), filepath.Join(h, ".bomclaw3"),
		filepath.Join(h, ".goterm"), filepath.Join(h, ".goterm2"), filepath.Join(h, ".goterm3"),
		filepath.Join(h, ".goterm-shared", "data"),
		filepath.Join(h, ".pm2"),
	}
}

// writablePaths are the places outside the project a normal dev toolchain
// writes to: package caches. Temp dirs are outside home and stay writable.
func (in *Infra) writablePaths() []string {
	h := in.Home
	return []string{
		filepath.Join(h, ".npm"), filepath.Join(h, ".cache"), filepath.Join(h, "Library", "Caches"),
		filepath.Join(h, ".bun"), filepath.Join(h, ".pnpm-store"), filepath.Join(h, "Library", "pnpm"),
		filepath.Join(h, ".yarn"),
	}
}

// Profile is the SBPL for an app working in cwd, plus its grants.
func (in *Infra) Profile(cwd string, grants ...[]string) string {
	var b strings.Builder
	b.WriteString("(version 1)\n(allow default)\n")
	b.WriteString(";; writes: the project, temp, package caches\n")
	fmt.Fprintf(&b, "(deny file-write* (subpath %s))\n", sbQuote(in.Home))
	allow := append([]string{cwd, in.ManagedDir()}, in.writablePaths()...)
	b.WriteString("(allow file-write*")
	for _, p := range allow {
		fmt.Fprintf(&b, "\n  (subpath %s)", sbQuote(p))
	}
	b.WriteString(")\n;; credentials: never readable\n(deny file-read* file-write*")
	for _, p := range in.secretPaths() {
		// A project that lives inside one of these (it should not) keeps
		// its own folder: denying it would only make the app fail.
		if cwd == p || strings.HasPrefix(cwd, p+string(filepath.Separator)) {
			continue
		}
		fmt.Fprintf(&b, "\n  (subpath %s)", sbQuote(p))
	}
	b.WriteString(")\n")
	ops := []string{"file-read*", "file-read* file-write*"}
	for i, paths := range grants {
		if i >= len(ops) || len(paths) == 0 {
			continue
		}
		fmt.Fprintf(&b, ";; granted to this app\n(allow %s", ops[i])
		for _, p := range paths {
			fmt.Fprintf(&b, "\n  (subpath %s)", sbQuote(p))
		}
		b.WriteString(")\n")
	}
	// A granted file is reached through its denied folders: let those be
	// stat'ed (not listed, not read). Go's MkdirAll on the coord folder
	// failed without it.
	parents := map[string]bool{}
	for _, paths := range grants {
		for _, p := range paths {
			for d := filepath.Dir(p); d != in.Home && d != "/" && d != "."; d = filepath.Dir(d) {
				parents[d] = true
			}
		}
	}
	if len(parents) > 0 {
		ds := make([]string, 0, len(parents))
		for d := range parents {
			ds = append(ds, d)
		}
		sort.Strings(ds)
		b.WriteString("(allow file-read-metadata")
		for _, d := range ds {
			fmt.Fprintf(&b, "\n  (literal %s)", sbQuote(d))
		}
		b.WriteString(")\n")
	}
	return b.String()
}

func sbQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// ecosystem is pm2's JSON ecosystem file.
type ecosystem struct {
	Apps []pm2Entry `json:"apps"`
}

type pm2Entry struct {
	Name             string            `json:"name"`
	Namespace        string            `json:"namespace"`
	Cwd              string            `json:"cwd"`
	Script           string            `json:"script"`
	Args             []string          `json:"args"`
	Interpreter      string            `json:"interpreter"`
	Env              map[string]string `json:"env,omitempty"`
	OutFile          string            `json:"out_file"`
	ErrorFile        string            `json:"error_file"`
	MaxMemoryRestart string            `json:"max_memory_restart,omitempty"`
	Autorestart      bool              `json:"autorestart"`
	RestartDelay     int               `json:"restart_delay"`
	ExpBackoff       int               `json:"exp_backoff_restart_delay"`
	// Bomclaw keeps the request it was made from, so a re-run or an adopt of
	// the same app can rewrite the entry without parsing args back.
	Bomclaw App `json:"bomclaw"`
}

func (in *Infra) ecosystemPath(project string) string {
	return filepath.Join(in.ManagedDir(), project+".json")
}

// baseEnv is what every managed app starts with; the daemon's own environment
// (whatever shell started pm2, keys included) is filtered out.
func (in *Infra) baseEnv(bin string) map[string]string {
	path := strings.Join(uniq([]string{filepath.Dir(bin), "/opt/homebrew/bin", "/usr/local/bin", "/usr/bin", "/bin", "/usr/sbin", "/sbin"}), ":")
	env := map[string]string{"PATH": path, "HOME": in.Home, "HOST": "127.0.0.1", "LANG": "en_US.UTF-8"}
	if u := os.Getenv("USER"); u != "" {
		env["USER"] = u
	}
	if t := os.Getenv("TMPDIR"); t != "" {
		env["TMPDIR"] = t
	}
	return env
}

func uniq(ss []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range ss {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// resolveBin makes argv[0] absolute: as given, against the project's
// node_modules/.bin, then PATH.
func resolveBin(cwd, name string) (string, error) {
	if filepath.IsAbs(name) {
		if st, err := os.Stat(name); err != nil || st.IsDir() {
			return "", fmt.Errorf("%s: not an executable file", name)
		}
		return name, nil
	}
	if strings.Contains(name, "/") {
		return resolveBin(cwd, filepath.Join(cwd, name))
	}
	if p := filepath.Join(cwd, "node_modules", ".bin", name); fileExists(p) {
		return p, nil
	}
	p, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("%s: not found on PATH", name)
	}
	return filepath.Abs(p)
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// entry builds the pm2 entry, and writes the app's sandbox profile.
func (in *Infra) entry(a App) (pm2Entry, error) {
	bin, err := resolveBin(a.Cwd, a.Command[0])
	if err != nil {
		return pm2Entry{}, err
	}
	a.Command = append([]string{bin}, a.Command[1:]...)
	env := in.baseEnv(bin)
	for k, v := range a.Env {
		env[k] = v
	}
	logs := filepath.Join(in.ManagedDir(), "logs")
	if err := os.MkdirAll(logs, 0o755); err != nil {
		return pm2Entry{}, err
	}
	e := pm2Entry{
		Name: a.Name, Namespace: a.Project, Cwd: a.Cwd, Interpreter: "none",
		Script:    envBin,
		OutFile:   filepath.Join(logs, a.Name+".out.log"),
		ErrorFile: filepath.Join(logs, a.Name+".err.log"),
		// A failing start backs off (100ms doubling, pm2 caps it at 15s)
		// instead of restarting 565,000 times.
		Autorestart: true, ExpBackoff: 100,
		MaxMemoryRestart: firstNonEmpty(a.MaxMemory, "1G"),
		Bomclaw:          a,
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	e.Args = []string{"-i"}
	for _, k := range keys {
		e.Args = append(e.Args, k+"="+env[k])
	}
	if !a.NoSandbox {
		sb := filepath.Join(in.ManagedDir(), a.Name+".sb")
		if err := os.WriteFile(sb, []byte(in.Profile(a.Cwd, a.AllowRead, a.AllowWrite)), 0o644); err != nil {
			return pm2Entry{}, err
		}
		e.Args = append(e.Args, sandboxExec, "-f", sb)
	}
	e.Args = append(e.Args, a.Command...)
	return e, nil
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// upsert writes the entry into its project's ecosystem file, replacing one of
// the same name, and drops that name from any other project's file.
func (in *Infra) upsert(e pm2Entry) (string, error) {
	files, _ := in.globFiles(filepath.Join(in.ManagedDir(), "*.json"))
	for _, f := range files {
		if f == in.ecosystemPath(e.Namespace) {
			continue
		}
		eco, err := readEcosystem(f)
		if err != nil {
			continue
		}
		if kept := without(eco.Apps, e.Name); len(kept) != len(eco.Apps) {
			eco.Apps = kept
			if err := writeEcosystem(f, eco); err != nil {
				return "", err
			}
		}
	}
	path := in.ecosystemPath(e.Namespace)
	eco, err := readEcosystem(path)
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	eco.Apps = append(without(eco.Apps, e.Name), e)
	sort.Slice(eco.Apps, func(i, j int) bool { return eco.Apps[i].Name < eco.Apps[j].Name })
	return path, writeEcosystem(path, eco)
}

func without(apps []pm2Entry, name string) []pm2Entry {
	out := apps[:0:0]
	for _, a := range apps {
		if a.Name != name {
			out = append(out, a)
		}
	}
	return out
}

func readEcosystem(path string) (ecosystem, error) {
	var eco ecosystem
	b, err := os.ReadFile(path)
	if err != nil {
		return eco, err
	}
	return eco, json.Unmarshal(b, &eco)
}

func writeEcosystem(path string, eco ecosystem) error {
	if eco.Apps == nil {
		eco.Apps = []pm2Entry{}
	}
	b, _ := json.MarshalIndent(eco, "", "  ")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Run writes the app's entry and (re)starts it under pm2.
func (in *Infra) RunApp(ctx context.Context, a App) (Result, error) {
	if !appName.MatchString(a.Name) {
		return Result{}, fmt.Errorf("app name %q: lowercase letters, digits, . _ - (max 63)", a.Name)
	}
	if a.Project == "" {
		a.Project = a.Name
	}
	if !appName.MatchString(a.Project) {
		return Result{}, fmt.Errorf("project %q: lowercase letters, digits, . _ - (max 63)", a.Project)
	}
	if len(a.Command) == 0 {
		return Result{}, fmt.Errorf("no command")
	}
	if !filepath.IsAbs(a.Cwd) {
		return Result{}, fmt.Errorf("cwd %q must be absolute", a.Cwd)
	}
	if st, err := os.Stat(a.Cwd); err != nil || !st.IsDir() {
		return Result{}, fmt.Errorf("cwd %s: not a folder", a.Cwd)
	}
	for _, p := range append(append([]string{}, a.AllowRead...), a.AllowWrite...) {
		if !filepath.IsAbs(p) || filepath.Clean(p) == in.Home || filepath.Clean(p) == "/" {
			return Result{}, fmt.Errorf("grant %q: an absolute path narrower than home", p)
		}
	}
	if a.Cwd == in.Home || a.Cwd == "/" {
		return Result{}, fmt.Errorf("cwd %s: a project needs its own folder, the sandbox would allow writing all of it", a.Cwd)
	}
	bin := in.pm2()
	if bin == "" {
		return Result{}, fmt.Errorf("pm2 not found")
	}
	if err := os.MkdirAll(in.ManagedDir(), 0o755); err != nil {
		return Result{}, err
	}
	e, err := in.entry(a)
	if err != nil {
		return Result{}, err
	}
	path, err := in.upsert(e)
	if err != nil {
		return Result{}, err
	}
	// A previous app of this name (hand-made, or from an older entry) goes
	// first: pm2 start on an existing name only restarts the old definition.
	for _, x := range in.pm2Names(ctx, bin) {
		if x == a.Name {
			if _, err := in.Run(ctx, bin, "delete", a.Name); err != nil {
				return Result{}, fmt.Errorf("pm2 delete %s: %w", a.Name, err)
			}
		}
	}
	out, err := in.Run(ctx, bin, "start", path, "--only", a.Name)
	if err != nil {
		return Result{}, fmt.Errorf("pm2 start %s: %w", a.Name, err)
	}
	_, _ = in.Run(ctx, bin, "save")
	how := "sandboxed"
	if a.NoSandbox {
		how = "unsandboxed"
	}
	return Result{OK: true, Ran: fmt.Sprintf("pm2 start %s --only %s (%s)", path, a.Name, how), Output: pm2Said(string(out))}, nil
}

// pm2Said keeps pm2's own messages and drops the process table it prints
// after every start.
func pm2Said(out string) string {
	var keep []string
	for _, l := range strings.Split(out, "\n") {
		t := strings.TrimSpace(l)
		if t == "" || strings.ContainsAny(t[:1], "│┌└├┐┘┤─") {
			continue
		}
		keep = append(keep, t)
	}
	return lastLines(strings.Join(keep, "\n"), 6)
}

func (in *Infra) pm2Names(ctx context.Context, bin string) []string {
	apps, _ := in.pm2List(ctx, bin)
	names := make([]string, len(apps))
	for i, a := range apps {
		names[i] = a.Name
	}
	return names
}

// carriedEnv are the variables an adopted server keeps from its old process:
// the ones that say where and how it serves. Everything else (API keys the
// starting shell happened to have) stays behind.
var carriedEnv = []string{"PORT", "HOST", "BASE_PATH", "NODE_ENV", "PYTHONPATH", "VIRTUAL_ENV"}

// Adopt takes a stray server (listening, managed by nothing) into pm2: same
// program, arguments and folder, now sandboxed. The stray is stopped first,
// since the new one needs its port.
func (in *Infra) Adopt(ctx context.Context, pid int, name, project string) (Result, error) {
	snap := in.Snapshot(ctx)
	var l *Listener
	for i := range snap.Listeners {
		if snap.Listeners[i].PID == pid {
			l = &snap.Listeners[i]
			break
		}
	}
	switch {
	case l == nil:
		return Result{}, fmt.Errorf("pid %d is not listening on any port", pid)
	case l.ManagedBy != "":
		return Result{}, fmt.Errorf("pid %d is already managed by %s", pid, l.ManagedBy)
	case l.System:
		return Result{}, fmt.Errorf("pid %d (%s) is part of macOS or an app", pid, l.Command)
	case l.Tool:
		return Result{}, fmt.Errorf("pid %d (%s) is a CLI tool's own daemon; the tool starts and stops it", pid, l.Command)
	case l.Cwd == "":
		return Result{}, fmt.Errorf("pid %d: cannot read its working folder", pid)
	}
	exe, argv, env, err := in.procArgs(pid)
	if err != nil {
		return Result{}, fmt.Errorf("pid %d: read its command line: %w", pid, err)
	}
	if len(argv) == 0 {
		return Result{}, fmt.Errorf("pid %d: empty command line", pid)
	}
	// The kernel's exec path, not argv[0]: "node" on a PATH pm2 will not
	// have becomes the exact binary that was running.
	if exe != "" {
		argv[0] = exe
	}
	a := App{Name: name, Project: firstNonEmpty(project, l.Project), Cwd: l.Cwd, Command: argv, Env: map[string]string{}}
	if a.Project == "" {
		a.Project = slug(filepath.Base(l.Cwd))
	}
	a.Project = slug(a.Project)
	if a.Name == "" {
		a.Name = a.Project
		if base := slug(filepath.Base(l.Cwd)); base != a.Project {
			a.Name = a.Project + "-" + base
		}
	}
	for _, k := range carriedEnv {
		if v, ok := env[k]; ok {
			a.Env[k] = v
		}
	}
	// Validate everything that does not need the port before stopping the
	// stray: a refusal here leaves it running as it was.
	if !appName.MatchString(a.Name) || !appName.MatchString(a.Project) {
		return Result{}, fmt.Errorf("app %q / project %q: lowercase letters, digits, . _ - (max 63)", a.Name, a.Project)
	}
	// A process keeps running in a folder deleted under it, and lsof still
	// names the old path: motor-3d's dev server served a deleted project for
	// three months. There is nothing to restart it from; stop it instead.
	if st, err := os.Stat(a.Cwd); err != nil || !st.IsDir() {
		return Result{}, fmt.Errorf("pid %d runs in %s, which no longer exists — nothing to run it from; stop it instead", pid, a.Cwd)
	}
	if _, err := resolveBin(a.Cwd, a.Command[0]); err != nil {
		return Result{}, err
	}
	if err := killTERM(pid); err != nil {
		return Result{}, fmt.Errorf("stop pid %d: %w", pid, err)
	}
	if !in.waitGone(pid, 8*time.Second) {
		_ = killKILL(pid)
		in.waitGone(pid, 2*time.Second)
	}
	r, err := in.RunApp(ctx, a)
	if err != nil {
		return Result{}, fmt.Errorf("stopped pid %d, then: %w — its command was: %s", pid, err, strings.Join(argv, " "))
	}
	r.Ran = fmt.Sprintf("kill %d (:%d) → %s", pid, l.Port, r.Ran)
	return r, nil
}

func (in *Infra) waitGone(pid int, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if !alive(pid) {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return !alive(pid)
}

// parseProcArgs reads the kern.procargs2 layout: argc (int32, host order),
// the exec path, NUL padding, argv, then the environment.
func parseProcArgs(b []byte) (exe string, argv []string, env map[string]string, err error) {
	if len(b) < 4 {
		return "", nil, nil, fmt.Errorf("short procargs")
	}
	argc := int(int32(uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24))
	rest := b[4:]
	i := indexNUL(rest)
	exe, rest = string(rest[:i]), rest[i:]
	for len(rest) > 0 && rest[0] == 0 {
		rest = rest[1:]
	}
	env = map[string]string{}
	for n := 0; len(rest) > 0; n++ {
		i := indexNUL(rest)
		s := string(rest[:i])
		if i < len(rest) {
			rest = rest[i+1:]
		} else {
			rest = nil
		}
		if n < argc {
			argv = append(argv, s)
			continue
		}
		if s == "" {
			break
		}
		if k, v, ok := strings.Cut(s, "="); ok {
			env[k] = v
		}
	}
	if len(argv) != argc {
		return "", nil, nil, fmt.Errorf("procargs: %d of %d arguments", len(argv), argc)
	}
	return exe, argv, env, nil
}

func indexNUL(b []byte) int {
	for i, c := range b {
		if c == 0 {
			return i
		}
	}
	return len(b)
}

// sandboxed says whether a pm2 app runs under sandbox-exec, directly or
// behind env -i.
func sandboxed(script string, args []string) bool {
	if script == sandboxExec {
		return true
	}
	if script != envBin {
		return false
	}
	for _, a := range args {
		if a == sandboxExec {
			return true
		}
	}
	return false
}
