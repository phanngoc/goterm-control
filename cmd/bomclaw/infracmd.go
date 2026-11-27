package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/ngocp/goterm-control/internal/coord"
	"github.com/ngocp/goterm-control/internal/infra"
)

const infraUsage = `Usage: bomclaw infra <command> [flags]

  ls                          pm2 apps by project, LaunchAgents, listening ports, host load
  restart|stop|start <name>   a pm2 app (by name) or a LaunchAgent (by label)
  delete <pm2-app>            remove a pm2 app (its ecosystem file brings it back)
  kill <pid>                  SIGTERM a stray server: listening, and managed by nothing
  logs <name> [--lines N]     the tail of its stdout/stderr logs

  run --name N [--project P] [--cwd DIR] [--env K=V]... [--max-memory 1G]
      [--allow-read PATH]... [--allow-write PATH]... [--no-sandbox] -- <command> [args...]
                              run a project's server under pm2, sandboxed; re-running replaces it
  adopt <pid|:port> [--name N] [--project P]
                              move a stray server into pm2 as it was started (same command,
                              folder, PORT/HOST/BASE_PATH), sandboxed; the stray is stopped first
  remove <label>              unload a LaunchAgent whose program or folder is gone, and move its
                              plist to ~/.goterm-shared/pm2/backup/launchd/

Flags: --json (ls), --all (ls: include macOS/app listeners), --db <coord.db>

Project services run under pm2. run/adopt write ~/.goterm-shared/pm2/managed/<project>.json
(namespace = project) with a sandbox-exec profile per app: writes only inside the project
folder, temp and package caches; no reading ~/.ssh, ~/.aws, the bomclaw secret stores or pm2's
dump; a clean environment with HOST=127.0.0.1. Hand-written <project>.config.cjs files still work.
The dashboard's Admin → Infra shows and does the same.`

func runInfra(args []string) {
	fs := flag.NewFlagSet("infra", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "Print JSON")
	all := fs.Bool("all", false, "Include macOS and app listeners")
	lines := fs.Int("lines", 80, "logs: how many lines")
	dbPath := fs.String("db", coord.DefaultPath(), "Shared coordination database (project names)")
	name := fs.String("name", "", "run/adopt: pm2 app name")
	project := fs.String("project", "", "run/adopt: project (pm2 namespace)")
	cwd := fs.String("cwd", "", "run: the project folder (default: current folder)")
	maxMem := fs.String("max-memory", "", "run: restart above this memory (default 1G)")
	noSandbox := fs.Bool("no-sandbox", false, "run: do not confine it")
	var envs, allowRead, allowWrite multiFlag
	fs.Var(&envs, "env", "run: K=V, repeatable")
	fs.Var(&allowRead, "allow-read", "run: a path the sandbox may read though it is otherwise denied, repeatable")
	fs.Var(&allowWrite, "allow-write", "run: a path the sandbox may read and write though otherwise denied, repeatable")
	fs.Usage = func() { fmt.Fprintln(os.Stderr, infraUsage) }

	sub, rest := "ls", args
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, rest = args[0], args[1:]
	}
	var command []string
	for i, a := range rest {
		if a == "--" {
			rest, command = rest[:i], rest[i+1:]
			break
		}
	}
	var pos []string
	for len(rest) > 0 {
		fs.Parse(rest)
		rest = fs.Args()
		if len(rest) > 0 {
			pos, rest = append(pos, rest[0]), rest[1:]
		}
	}

	in := infra.New()
	in.Projects = map[string]string{}
	if db, err := coord.Open(*dbPath); err == nil {
		if rooms, err := db.ListChannels("", ""); err == nil {
			for _, c := range rooms {
				if c.Workspace != "" {
					in.Projects[c.Workspace] = c.Name
				}
			}
		}
		db.Close()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	switch sub {
	case "ls", "list", "status":
		snap := in.Snapshot(ctx)
		if *asJSON {
			b, _ := json.MarshalIndent(snap, "", "  ")
			fmt.Println(string(b))
			return
		}
		printInfra(snap, *all)
	case "restart", "stop", "start", "delete", "kill":
		if len(pos) != 1 {
			fs.Usage()
			os.Exit(1)
		}
		kind := infraKind(ctx, in, sub, pos[0])
		r, err := in.Do(ctx, infra.Action{Kind: kind, Target: pos[0], Verb: sub})
		infraExit(err)
		fmt.Println(r.Ran)
		if r.Output != "" {
			fmt.Println(r.Output)
		}
	case "run":
		if *name == "" || len(command) == 0 {
			fs.Usage()
			os.Exit(1)
		}
		dir := *cwd
		if dir == "" {
			dir, _ = os.Getwd()
		}
		dir, _ = filepath.Abs(dir)
		env := map[string]string{}
		for _, kv := range envs {
			k, v, ok := strings.Cut(kv, "=")
			if !ok || k == "" {
				infraExit(fmt.Errorf("--env %q: want K=V", kv))
			}
			env[k] = v
		}
		r, err := in.RunApp(ctx, infra.App{Name: *name, Project: *project, Cwd: dir, Command: command,
			Env: env, MaxMemory: *maxMem, NoSandbox: *noSandbox,
			AllowRead: absAll(allowRead), AllowWrite: absAll(allowWrite)})
		infraExit(err)
		fmt.Println(r.Ran)
		if r.Output != "" {
			fmt.Println(r.Output)
		}
	case "adopt":
		if len(pos) != 1 {
			fs.Usage()
			os.Exit(1)
		}
		pid, err := strayPID(ctx, in, pos[0])
		infraExit(err)
		r, err := in.Do(ctx, infra.Action{Kind: infra.KindPID, Target: strconv.Itoa(pid), Verb: "adopt", Name: *name, Project: *project})
		infraExit(err)
		fmt.Println(r.Ran)
		if r.Output != "" {
			fmt.Println(r.Output)
		}
	case "remove":
		if len(pos) != 1 {
			fs.Usage()
			os.Exit(1)
		}
		r, err := in.Do(ctx, infra.Action{Kind: infra.KindLaunchd, Target: pos[0], Verb: "remove"})
		infraExit(err)
		fmt.Println(r.Ran)
		if r.Output != "" {
			fmt.Println(r.Output)
		}
	case "logs", "log":
		if len(pos) != 1 {
			fs.Usage()
			os.Exit(1)
		}
		logs, err := in.Logs(ctx, infraKind(ctx, in, sub, pos[0]), pos[0], *lines)
		infraExit(err)
		for _, k := range []string{"out", "err"} {
			if v, ok := logs[k]; ok {
				fmt.Printf("==> %s <==\n%s\n", logs[k+"_path"], v)
			}
		}
	default:
		fs.Usage()
		os.Exit(1)
	}
}

// strayPID reads "1234" as a pid and ":5173" (or "5173" when no such pid
// listens) as the port a stray listens on.
func strayPID(ctx context.Context, in *infra.Infra, s string) (int, error) {
	port := strings.HasPrefix(s, ":")
	n, err := strconv.Atoi(strings.TrimPrefix(s, ":"))
	if err != nil {
		return 0, fmt.Errorf("%q: want a pid or :port", s)
	}
	var byPort []infra.Listener
	for _, l := range in.Snapshot(ctx).Listeners {
		if !port && l.PID == n {
			return n, nil
		}
		if l.Port == n {
			byPort = append(byPort, l)
		}
	}
	for _, l := range byPort {
		if l.ManagedBy == "" && !l.System && !l.Tool {
			return l.PID, nil
		}
	}
	if len(byPort) > 0 {
		return 0, fmt.Errorf(":%d is %s's, not a stray", n, orNone(byPort[0].ManagedBy, byPort[0].Command))
	}
	return 0, fmt.Errorf("nothing listens as %s", s)
}

func absAll(ps []string) []string {
	home, _ := os.UserHomeDir()
	var out []string
	for _, p := range ps {
		if strings.HasPrefix(p, "~/") {
			p = filepath.Join(home, p[2:])
		}
		a, _ := filepath.Abs(p)
		out = append(out, a)
	}
	return out
}

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

// infraKind decides what a bare name means: a pid for kill, else a pm2 app if
// one has that name, else a LaunchAgent label.
func infraKind(ctx context.Context, in *infra.Infra, verb, name string) string {
	if verb == "kill" {
		return infra.KindPID
	}
	for _, a := range in.Snapshot(ctx).PM2 {
		if a.Name == name {
			return infra.KindPM2
		}
	}
	return infra.KindLaunchd
}

func printInfra(s infra.Snapshot, all bool) {
	h := s.Host
	fmt.Printf("host  load %.2f %.2f %.2f / %d cpu · mem %s/%s · disk free %s/%s\n\n",
		h.Load[0], h.Load[1], h.Load[2], h.CPUs, gib(h.MemUsed), gib(h.MemTotal), gib(h.DiskFree), gib(h.DiskTotal))

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "PM2 (%s)\tPROJECT\tSTATUS\tPID\tPORTS\tCPU\tMEM\t↺\n", orNone(s.PM2Bin))
	for _, a := range s.PM2 {
		st := a.Status
		if a.Crashloop {
			st += " ⚠ crashloop"
		}
		if a.Sandboxed {
			st += " ⛨"
		}
		fmt.Fprintf(w, "  %s\t%s\t%s\t%d\t%s\t%.0f%%\t%s\t%d\n", a.Name, orNone(a.Project, a.Namespace), st, a.PID,
			ports(a.Ports), a.CPU, mib(a.Mem), a.Restarts)
	}
	if s.PM2Error != "" {
		fmt.Fprintf(w, "  ! %s\n", s.PM2Error)
	}
	w.Flush()
	fmt.Println()
	w = tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "LAUNCHD\tPROJECT\tSTATE\tPID\tPORTS")
	for _, sv := range s.Services {
		state := "not loaded"
		switch {
		case sv.Missing != "":
			state = "broken: " + sv.Missing + " is gone (bomclaw infra remove " + sv.Label + ")"
		case sv.Loaded && sv.PID > 0:
			state = "running"
		case sv.Loaded && sv.Scheduled:
			state = fmt.Sprintf("scheduled (last exit %d)", sv.LastExit)
		case sv.Loaded:
			state = fmt.Sprintf("stopped (exit %d)", sv.LastExit)
		}
		fmt.Fprintf(w, "  %s\t%s\t%s\t%s\t%s\n", sv.Label, orNone(sv.Project, sv.Kind), state, pidOr(sv.PID), ports(sv.Ports))
	}
	w.Flush()
	fmt.Println()
	w = tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "PORT\tPROJECT\tMANAGED BY\tPID\tCOMMAND\tCWD")
	for _, l := range s.Listeners {
		if l.System && !all {
			continue
		}
		by := l.ManagedBy
		if by == "" && l.Tool {
			by = "(tool daemon)"
		}
		if by == "" {
			by = "— (stray: bomclaw infra adopt " + strconv.Itoa(l.PID) + ")"
		}
		if l.Exposed {
			by += " · all interfaces"
		}
		fmt.Fprintf(w, "  %d\t%s\t%s\t%d\t%s\t%s\n", l.Port, orNone(l.Project), by, l.PID, l.Command, l.Cwd)
	}
	w.Flush()
	for _, e := range s.Errors {
		fmt.Fprintln(os.Stderr, "warning:", e)
	}
}

func orNone(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return "-"
}

func pidOr(p int) string {
	if p == 0 {
		return "-"
	}
	return fmt.Sprint(p)
}

func ports(ps []int) string {
	if len(ps) == 0 {
		return "-"
	}
	s := make([]string, len(ps))
	for i, p := range ps {
		s[i] = fmt.Sprint(p)
	}
	return strings.Join(s, ",")
}

func gib(b int64) string { return fmt.Sprintf("%.1fG", float64(b)/(1<<30)) }
func mib(b int64) string { return fmt.Sprintf("%.0fM", float64(b)/(1<<20)) }

func infraExit(err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "infra: %v\n", err)
		os.Exit(1)
	}
}
