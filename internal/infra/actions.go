package infra

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

// Target kinds an action can name.
const (
	KindPM2     = "pm2"
	KindLaunchd = "launchd"
	KindPID     = "pid"
)

// Verbs per kind. Deliberately short: these are the things an owner does from
// a phone at night, and each is undoable by another one of them — except
// pm2 delete, which forgets the app (its ecosystem file still brings it back).
var verbs = map[string][]string{
	KindPM2:     {"restart", "stop", "start", "delete"},
	KindLaunchd: {"restart", "stop", "start"},
	KindPID:     {"kill"},
}

// Action is one request. Target is a pm2 app name, a LaunchAgent label, or a pid.
type Action struct {
	Kind   string `json:"kind"`
	Target string `json:"target"`
	Verb   string `json:"verb"`
}

// Result says what ran.
type Result struct {
	OK      bool   `json:"ok"`
	Ran     string `json:"ran"`
	Output  string `json:"output,omitempty"`
	Pending bool   `json:"pending,omitempty"` // runs after the reply: it restarts the process answering
}

// SelfLabel is the LaunchAgent this process runs as, if any. Restarting it
// kills the request that asked, so that one is answered first and done after.
var SelfLabel = os.Getenv("XPC_SERVICE_NAME")

// Do validates the action against a fresh snapshot and runs it.
func (in *Infra) Do(ctx context.Context, a Action) (Result, error) {
	if !allowed(a.Kind, a.Verb) {
		return Result{}, fmt.Errorf("%s cannot %q (allowed: %s)", a.Kind, a.Verb, strings.Join(verbs[a.Kind], ", "))
	}
	snap := in.Snapshot(ctx)
	switch a.Kind {
	case KindPM2:
		if snap.PM2Bin == "" {
			return Result{}, fmt.Errorf("pm2 not found")
		}
		if !hasPM2(snap.PM2, a.Target) {
			return Result{}, fmt.Errorf("no pm2 app %q", a.Target)
		}
		out, err := in.Run(ctx, snap.PM2Bin, a.Verb, a.Target)
		if err != nil {
			return Result{}, fmt.Errorf("pm2 %s %s: %w", a.Verb, a.Target, err)
		}
		// Persist, so a reboot (pm2 resurrect) comes back to what the owner
		// chose rather than to the last time someone remembered to save.
		_, _ = in.Run(ctx, snap.PM2Bin, "save")
		return Result{OK: true, Ran: "pm2 " + a.Verb + " " + a.Target, Output: lastLines(string(out), 6)}, nil

	case KindLaunchd:
		var svc *Service
		for i := range snap.Services {
			if snap.Services[i].Label == a.Target {
				svc = &snap.Services[i]
			}
		}
		if svc == nil {
			return Result{}, fmt.Errorf("no LaunchAgent %q in %s", a.Target, in.agentsDir())
		}
		domain := fmt.Sprintf("gui/%d", in.UID)
		var args []string
		switch a.Verb {
		case "restart":
			if !svc.Loaded {
				args = []string{"bootstrap", domain, svc.Plist}
			} else {
				args = []string{"kickstart", "-k", domain + "/" + a.Target}
			}
		case "start":
			if !svc.Loaded {
				args = []string{"bootstrap", domain, svc.Plist}
			} else {
				args = []string{"kickstart", domain + "/" + a.Target}
			}
		case "stop":
			// A KeepAlive job comes straight back from a kill; bootout is
			// what stops it. `start` bootstraps it again.
			args = []string{"bootout", domain + "/" + a.Target}
		}
		ran := "launchctl " + strings.Join(args, " ")
		if a.Target == SelfLabel && SelfLabel != "" {
			go func() {
				time.Sleep(500 * time.Millisecond)
				_, _ = in.Run(context.Background(), "launchctl", args...)
			}()
			return Result{OK: true, Ran: ran, Pending: true}, nil
		}
		out, err := in.Run(ctx, "launchctl", args...)
		if err != nil {
			return Result{}, fmt.Errorf("%s: %w", ran, err)
		}
		return Result{OK: true, Ran: ran, Output: lastLines(string(out), 6)}, nil

	case KindPID:
		pid, err := strconv.Atoi(a.Target)
		if err != nil || pid <= 1 {
			return Result{}, fmt.Errorf("bad pid %q", a.Target)
		}
		var l *Listener
		for i := range snap.Listeners {
			if snap.Listeners[i].PID == pid {
				l = &snap.Listeners[i]
			}
		}
		switch {
		case l == nil:
			return Result{}, fmt.Errorf("pid %d is not listening on any port — only a stray server can be killed from here", pid)
		case l.ManagedBy != "":
			return Result{}, fmt.Errorf("pid %d belongs to %s — stop it there, or it comes back", pid, l.ManagedBy)
		case l.System:
			return Result{}, fmt.Errorf("pid %d (%s) is part of macOS or an app", pid, l.Command)
		}
		if err := killTERM(pid); err != nil {
			return Result{}, fmt.Errorf("kill %d: %w", pid, err)
		}
		return Result{OK: true, Ran: fmt.Sprintf("kill -TERM %d (%s :%d)", pid, l.Command, l.Port)}, nil
	}
	return Result{}, fmt.Errorf("unknown kind %q", a.Kind)
}

func allowed(kind, verb string) bool {
	for _, v := range verbs[kind] {
		if v == verb {
			return true
		}
	}
	return false
}

func hasPM2(apps []PM2App, name string) bool {
	for _, a := range apps {
		if a.Name == name {
			return true
		}
	}
	return false
}

// Logs returns the last lines of a target's stdout and stderr logs, as named
// by pm2 or the plist — never a path from the request.
func (in *Infra) Logs(ctx context.Context, kind, target string, lines int) (map[string]string, error) {
	if lines <= 0 || lines > 2000 {
		lines = 200
	}
	snap := in.Snapshot(ctx)
	var out, errLog string
	switch kind {
	case KindPM2:
		for _, a := range snap.PM2 {
			if a.Name == target {
				out, errLog = a.OutLog, a.ErrLog
			}
		}
	case KindLaunchd:
		for _, s := range snap.Services {
			if s.Label == target {
				out, errLog = s.OutLog, s.ErrLog
			}
		}
	default:
		return nil, fmt.Errorf("logs are for pm2 apps and LaunchAgents")
	}
	if out == "" && errLog == "" {
		return nil, fmt.Errorf("%s %q has no log files", kind, target)
	}
	res := map[string]string{}
	if out != "" {
		res["out"] = tailFile(out, lines)
		res["out_path"] = out
	}
	if errLog != "" && errLog != out {
		res["err"] = tailFile(errLog, lines)
		res["err_path"] = errLog
	}
	return res, nil
}

// tailFile reads at most the last 256 KB, then the last n lines of that.
func tailFile(path string, n int) string {
	f, err := os.Open(path)
	if err != nil {
		return "(" + err.Error() + ")"
	}
	defer f.Close()
	const max = 256 << 10
	if st, err := f.Stat(); err == nil && st.Size() > max {
		_, _ = f.Seek(st.Size()-max, io.SeekStart)
	}
	b, _ := io.ReadAll(f)
	return lastLines(string(b), n)
}

func lastLines(s string, n int) string {
	s = strings.TrimRight(s, "\n")
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
