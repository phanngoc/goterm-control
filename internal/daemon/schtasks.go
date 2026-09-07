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

const (
	// schtasksName is the task path. The BomClaw folder keeps the gateway out
	// of the top-level task list, where it would sit among the tasks Windows
	// registers for itself.
	schtasksName = `\BomClaw\bomclaw-gateway`

	// schtasksImage is the process to look for when reporting the live PID.
	// The task itself runs cmd.exe (see wrapWithLogRedirect), so the task's
	// own PID is not the gateway's.
	schtasksImage = "bomclaw.exe"
)

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

	gatewayArgs := buildGatewayArgs(args)
	outLog := filepath.Join(s.logDir, "gateway.log")
	errLog := filepath.Join(s.logDir, "gateway.err.log")

	// Task Scheduler cannot redirect a task's output, so the action goes
	// through cmd.exe purely to produce the same gateway.log/gateway.err.log
	// pair the LaunchAgent and the systemd unit give. Without it a background
	// gateway on Windows has nowhere to report a startup failure.
	var command, arguments string
	if cmdSafe(append([]string{args.BinaryPath, outLog, errLog}, gatewayArgs...)) {
		command, arguments = wrapWithLogRedirect(args.BinaryPath, gatewayArgs, outLog, errLog)
	} else {
		// A path cmd.exe would not pass through verbatim. Run the gateway
		// directly and give up the log files rather than risk starting it with
		// mangled arguments.
		command, arguments = args.BinaryPath, buildTaskArguments(gatewayArgs)
		log.Printf("daemon: a path contains a quote or percent sign — installing without " +
			"log redirection; gateway output will only appear in Task Scheduler history")
	}

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
	return schtasks(ctx, "/Run", "/TN", s.taskName)
}

func (s *schtasksService) Stop(ctx context.Context) error {
	return schtasks(ctx, "/End", "/TN", s.taskName)
}

func (s *schtasksService) Restart(ctx context.Context) error {
	// schtasks has no /Restart. /End on a task that is not running reports an
	// error, which is not a failure to restart — and the gateway clears any
	// stale listener on its port at startup (KillStaleListeners), so a socket
	// still closing does not lose the race.
	if err := schtasks(ctx, "/End", "/TN", s.taskName); err != nil {
		log.Printf("daemon: end before start (may not be running): %v", err)
	}
	return schtasks(ctx, "/Run", "/TN", s.taskName)
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

	// A live process is the authority on "running", and unlike the field names
	// above it does not depend on the console locale: schtasks /V prints
	// translated keys on a non-English Windows, so the parse above can come up
	// empty on a machine where the gateway is plainly up.
	if pid := findProcessPID(schtasksImage); pid > 0 {
		rt.PID = pid
		rt.Status = "running"
	} else if rt.Status == "unknown" {
		rt.Status = "stopped"
	}

	return rt, nil
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

// wrapWithLogRedirect returns the Command and Arguments for a task action that
// runs bin through cmd.exe with stdout and stderr appended to log files.
func wrapWithLogRedirect(bin string, args []string, outLog, errLog string) (string, string) {
	comspec := os.Getenv("COMSPEC")
	if comspec == "" {
		comspec = `C:\Windows\System32\cmd.exe`
	}

	// cmd.exe does its own quote handling and does not understand the
	// backslash escaping CommandLineToArgvW uses, so every value here is
	// simply wrapped in double quotes. /s is what makes this deterministic: it
	// tells cmd to strip exactly the outermost pair of quotes and take the
	// rest literally. Without /s, cmd's rule for a line holding more than two
	// quotes is not something to rely on.
	var b strings.Builder
	b.WriteString(`/s /c "`)
	b.WriteString(cmdQuote(bin))
	for _, a := range args {
		b.WriteString(" ")
		b.WriteString(cmdQuote(a))
	}
	fmt.Fprintf(&b, ` 1>>%s 2>>%s"`, cmdQuote(outLog), cmdQuote(errLog))

	return comspec, b.String()
}

// cmdQuote wraps a value in double quotes for cmd.exe.
func cmdQuote(s string) string { return `"` + s + `"` }

// cmdSafe reports whether every value survives cmd.exe's parsing unchanged.
// cmd has no escape for a double quote, and a percent sign can be eaten by
// environment expansion even inside quotes, so a path holding either is not
// routed through the redirect wrapper.
func cmdSafe(vals []string) bool {
	for _, v := range vals {
		if strings.ContainsAny(v, `"%`) {
			return false
		}
	}
	return true
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

// findProcessPID returns the PID of the first running process with the given
// image name, or 0. CSV output with /NH is parsed so the result does not
// depend on the console locale's column headers.
func findProcessPID(image string) int {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	res, err := execCommand(ctx, "tasklist", "/FI", "IMAGENAME eq "+image, "/FO", "CSV", "/NH")
	if err != nil || res.ExitCode != 0 {
		return 0
	}
	// Rows look like: "bomclaw.exe","12345","Console","1","45,678 K"
	// With no match tasklist prints a prose INFO line instead, which has no
	// field separator and so falls through.
	for line := range strings.SplitSeq(res.Stdout, "\n") {
		fields := strings.Split(strings.TrimSpace(line), `","`)
		if len(fields) < 2 {
			continue
		}
		if pid, err := strconv.Atoi(strings.Trim(fields[1], `"`)); err == nil && pid > 0 {
			return pid
		}
	}
	return 0
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
