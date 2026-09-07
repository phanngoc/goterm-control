package daemon

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// schtasksName is the task path. The BomClaw folder keeps the gateway out of
// the top-level task list, where it would sit among the tasks Windows
// registers for itself.
const schtasksName = `\BomClaw\bomclaw-gateway`

type schtasksService struct {
	taskName string
	xmlPath  string
	logDir   string
}

func newSchtasksService() (*schtasksService, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve home dir: %w", err)
	}
	// Task Scheduler keeps its own copy of a registered task; this file is the
	// definition we generated, kept for inspection and for reinstalls.
	defDir := os.Getenv("LOCALAPPDATA")
	if defDir == "" {
		defDir = filepath.Join(home, "AppData", "Local")
	}
	return &schtasksService{
		taskName: schtasksName,
		xmlPath:  filepath.Join(defDir, "BomClaw", "bomclaw-gateway.xml"),
		logDir:   filepath.Join(home, ".goterm", "logs"),
	}, nil
}

func (s *schtasksService) Label() string { return "Scheduled Task" }

func (s *schtasksService) UnitPath() string { return s.xmlPath }

func (s *schtasksService) Install(ctx context.Context, args InstallArgs) error {
	if err := os.MkdirAll(filepath.Dir(s.xmlPath), 0755); err != nil {
		return fmt.Errorf("create task definition dir: %w", err)
	}
	if err := os.MkdirAll(s.logDir, 0755); err != nil {
		return fmt.Errorf("create log dir: %w", err)
	}

	// The gateway is the task's action directly, and writes its own log via
	// --log-file. Task Scheduler cannot redirect output, and wrapping the
	// action in `cmd /c ... >>log` to get around that was a mistake: Task
	// Scheduler then manages cmd instead of the gateway, so /End orphaned a
	// live gateway which kept the inherited log handle, and every later start
	// failed because cmd could not reopen a file the orphan still held. Status,
	// stop and the keep-alive all depend on the task owning the real process.
	gatewayArgs := append(buildGatewayArgs(args), "--log-file", filepath.Join(s.logDir, "gateway.log"))
	command := args.BinaryPath
	arguments := buildTaskArguments(gatewayArgs)

	warnUnexportableEnv(args.Environment)

	userID := ""
	if u, err := user.Current(); err == nil {
		userID = u.Username
	}

	xml := buildTaskXML(taskXMLArgs{
		URI:         s.taskName,
		Description: args.Description,
		UserID:      userID,
		Command:     command,
		Arguments:   arguments,
		WorkingDir:  filepath.Dir(args.BinaryPath),
	})

	// Backup existing definition
	if _, err := os.Stat(s.xmlPath); err == nil {
		if data, _ := os.ReadFile(s.xmlPath); len(data) > 0 {
			_ = os.WriteFile(s.xmlPath+".bak", data, 0644)
			log.Printf("daemon: backed up existing definition to %s.bak", s.xmlPath)
		}
	}

	if err := os.WriteFile(s.xmlPath, encodeUTF16LE(xml), 0644); err != nil {
		return fmt.Errorf("write task definition: %w", err)
	}
	log.Printf("daemon: wrote %s", s.xmlPath)

	// /F overwrites an existing registration, which is both what --force
	// expects and how a reinstall picks up changed flags.
	if err := schtasks(ctx, "/Create", "/TN", s.taskName, "/XML", s.xmlPath, "/F"); err != nil {
		return fmt.Errorf("register task: %w", err)
	}

	// /Create registers the task but does not run it — the logon trigger would
	// not fire until the next logon.
	if err := s.Restart(ctx); err != nil {
		return fmt.Errorf("start task: %w", err)
	}

	log.Printf("daemon: scheduled task %s installed and started", s.taskName)
	return nil
}

func (s *schtasksService) Uninstall(ctx context.Context) error {
	// /End first: /Delete on a running task removes the registration and
	// leaves the process behind.
	if err := schtasks(ctx, "/End", "/TN", s.taskName); err != nil {
		log.Printf("daemon: end before delete (may not be running): %v", err)
	}

	if err := schtasks(ctx, "/Delete", "/TN", s.taskName, "/F"); err != nil {
		return fmt.Errorf("delete task: %w", err)
	}

	if err := os.Remove(s.xmlPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove task definition: %w", err)
	}

	log.Printf("daemon: scheduled task %s uninstalled", s.taskName)
	return nil
}

func (s *schtasksService) Start(ctx context.Context) error {
	// Enable first: Stop disables the task, and /Run on a disabled task is
	// refused.
	if err := schtasks(ctx, "/Change", "/TN", s.taskName, "/ENABLE"); err != nil {
		return fmt.Errorf("enable task: %w", err)
	}
	return schtasks(ctx, "/Run", "/TN", s.taskName)
}

func (s *schtasksService) Stop(ctx context.Context) error {
	// /End alone is not a stop. The keep-alive trigger fires every minute and
	// knows nothing about an administrative stop, so it brought the gateway
	// straight back — `gateway stop` lasted under a minute, measured. Task
	// Scheduler has no "stopped until started" state, so disabling the task is
	// what makes it durable; Start re-enables.
	if err := schtasks(ctx, "/End", "/TN", s.taskName); err != nil {
		log.Printf("daemon: end task (may not have been running): %v", err)
	}
	return schtasks(ctx, "/Change", "/TN", s.taskName, "/DISABLE")
}

func (s *schtasksService) Restart(ctx context.Context) error {
	// schtasks has no /Restart. /End on a task that is not running reports an
	// error, which is not a failure to restart — and the gateway clears any
	// stale listener on its port at startup (KillStaleListeners), so a socket
	// still closing does not lose the race. Start re-enables, so a restart
	// after a stop works.
	if err := schtasks(ctx, "/End", "/TN", s.taskName); err != nil {
		log.Printf("daemon: end before start (may not be running): %v", err)
	}
	return s.Start(ctx)
}

func (s *schtasksService) IsInstalled() (bool, error) {
	res, err := execCommand(context.Background(), "schtasks", "/Query", "/TN", s.taskName)
	if err != nil {
		return false, err
	}
	// An unregistered task exits non-zero. That is an answer, not a failure.
	return res.ExitCode == 0, nil
}

func (s *schtasksService) ReadRuntime() (*ServiceRuntime, error) {
	rt := &ServiceRuntime{Status: "unknown"}

	res, err := execCommand(context.Background(),
		"schtasks", "/Query", "/TN", s.taskName, "/FO", "LIST", "/V",
	)
	if err != nil {
		return rt, err
	}
	if res.ExitCode != 0 {
		rt.Status = "stopped"
		return rt, nil
	}

	for line := range strings.SplitSeq(res.Stdout, "\n") {
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		switch key {
		case "Status":
			rt.SubState = val
			switch val {
			case "Running":
				rt.Status = "running"
			case "Ready", "Disabled":
				rt.Status = "stopped"
			default:
				rt.Status = strings.ToLower(val)
			}
		case "Last Result":
			rt.ExitCode = parseTaskResult(val)
		}
	}

	// schtasks /V prints translated field names on a non-English Windows, so
	// the parse above can come up empty on a machine where the task is plainly
	// registered and running. Get-ScheduledTask reports State as a .NET enum
	// name, which is not localised.
	if rt.Status == "unknown" {
		rt.Status, rt.SubState = s.stateViaPowerShell()
	}

	// PID is deliberately left at 0. Neither schtasks nor Get-ScheduledTask
	// reports the pid of a running task, and scanning by image name is not a
	// substitute: every bomclaw subcommand is also bomclaw.exe, so
	// `bomclaw gateway status` found *itself* and reported the service as
	// running with its own pid while the task sat at Ready. Reporting no pid is
	// honest, and `gateway status` establishes liveness from the port anyway.

	return rt, nil
}

// stateViaPowerShell reads the task state as a locale-independent enum name.
// Returns ("unknown", "") if the task or the ScheduledTasks module is absent.
func (s *schtasksService) stateViaPowerShell() (status, subState string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	res, err := execCommand(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command",
		"(Get-ScheduledTask -TaskPath '\\BomClaw\\' -TaskName 'bomclaw-gateway').State")
	if err != nil || res.ExitCode != 0 {
		return "unknown", ""
	}

	state := strings.TrimSpace(res.Stdout)
	switch state {
	case "Running":
		return "running", state
	case "Ready", "Disabled":
		return "stopped", state
	case "":
		return "unknown", ""
	default:
		return strings.ToLower(state), state
	}
}

// buildGatewayArgs builds the `gateway` subcommand arguments from InstallArgs.
func buildGatewayArgs(args InstallArgs) []string {
	out := []string{"gateway"}
	if args.ConfigPath != "" {
		out = append(out, "--config", args.ConfigPath)
	}
	if args.EnvFile != "" {
		out = append(out, "--env", args.EnvFile)
	}
	if args.Bind != "" {
		out = append(out, "--bind", args.Bind)
	}
	if args.Port > 0 {
		out = append(out, "--port", strconv.Itoa(args.Port))
	}
	return out
}

// warnUnexportableEnv reports environment variables the task definition cannot
// carry. Task Scheduler has no equivalent of systemd's Environment= or
// launchd's EnvironmentVariables — a task inherits the environment the user's
// session had at logon. Values that live only in the installing shell would
// silently go missing, so name them (never their values).
func warnUnexportableEnv(env map[string]string) {
	var unset []string
	for k := range env {
		switch k {
		case "HOME", "PATH":
			// HOME is a POSIX concept; os.UserHomeDir reads USERPROFILE here.
			// PATH comes from the user's session either way.
			continue
		}
		unset = append(unset, k)
	}
	if len(unset) == 0 {
		return
	}
	sort.Strings(unset)
	log.Printf("daemon: Task Scheduler stores no environment block — %s must be in the "+
		"--env file or a persisted user variable (setx) to reach the gateway",
		strings.Join(unset, ", "))
}

// parseTaskResult parses a schtasks "Last Result" value, which some Windows
// builds print in hex.
func parseTaskResult(v string) int {
	v = strings.TrimSpace(v)
	if hex, ok := strings.CutPrefix(v, "0x"); ok {
		n, err := strconv.ParseInt(hex, 16, 64)
		if err != nil {
			return 0
		}
		return int(n)
	}
	n, _ := strconv.Atoi(v)
	return n
}

// schtasks runs a schtasks command and returns an error if it fails.
func schtasks(ctx context.Context, args ...string) error {
	if !commandExists("schtasks") {
		return fmt.Errorf("schtasks not found — it ships with every supported Windows edition; check PATH")
	}
	res, err := execCommand(ctx, "schtasks", args...)
	if err != nil {
		return fmt.Errorf("schtasks %s: %w", strings.Join(args, " "), err)
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("schtasks %s (exit %d): %s",
			strings.Join(args, " "), res.ExitCode,
			strings.TrimSpace(res.Stderr+res.Stdout))
	}
	return nil
}
