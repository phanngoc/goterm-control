package daemon

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const launchdLabel = "com.bomclaw.gateway"

type launchdService struct {
	label     string
	plistPath string
	logDir    string
}

func newLaunchdService() (*launchdService, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve home dir: %w", err)
	}
	return &launchdService{
		label:     launchdLabel,
		plistPath: filepath.Join(home, "Library", "LaunchAgents", launchdLabel+".plist"),
		logDir:    filepath.Join(home, ".goterm", "logs"),
	}, nil
}

func (s *launchdService) Label() string { return "LaunchAgent" }

func (s *launchdService) UnitPath() string { return s.plistPath }

func (s *launchdService) Install(ctx context.Context, args InstallArgs) error {
	// 1. Create directories
	plistDir := filepath.Dir(s.plistPath)
	if err := os.MkdirAll(plistDir, 0755); err != nil {
		return fmt.Errorf("create LaunchAgents dir: %w", err)
	}
	if err := os.MkdirAll(s.logDir, 0755); err != nil {
		return fmt.Errorf("create log dir: %w", err)
	}

	// 2. Build program arguments
	progArgs := []string{args.BinaryPath, "gateway"}
	if args.ConfigPath != "" {
		progArgs = append(progArgs, "--config", args.ConfigPath)
	}
	if args.EnvFile != "" {
		progArgs = append(progArgs, "--env", args.EnvFile)
	}
	if args.Bind != "" {
		progArgs = append(progArgs, "--bind", args.Bind)
	}
	if args.Port > 0 {
		progArgs = append(progArgs, "--port", fmt.Sprintf("%d", args.Port))
	}

	// 3. Generate plist
	plist := buildPlist(plistArgs{
		Label:       s.label,
		ProgramArgs: progArgs,
		Environment: args.Environment,
		StdoutPath:  filepath.Join(s.logDir, "gateway.log"),
		StderrPath:  filepath.Join(s.logDir, "gateway.err.log"),
	})

	// 4. Backup existing plist
	if _, err := os.Stat(s.plistPath); err == nil {
		bakPath := s.plistPath + ".bak"
		data, _ := os.ReadFile(s.plistPath)
		if len(data) > 0 {
			_ = os.WriteFile(bakPath, data, 0644)
		}
	}

	// 5. Write plist
	if err := os.WriteFile(s.plistPath, []byte(plist), 0644); err != nil {
		return fmt.Errorf("write plist: %w", err)
	}
	log.Printf("daemon: wrote %s", s.plistPath)

	// 6. Try to bootout existing (ignore errors — may not be loaded)
	domain := s.domain()
	_ = launchctl(ctx, "bootout", domain+"/"+s.label)

	// 7. Enable and bootstrap
	if err := launchctl(ctx, "enable", domain+"/"+s.label); err != nil {
		log.Printf("daemon: launchctl enable warning: %v", err)
	}
	if err := launchctl(ctx, "bootstrap", domain, s.plistPath); err != nil {
		return fmt.Errorf("launchctl bootstrap: %w", err)
	}

	log.Printf("daemon: LaunchAgent %s installed and started", s.label)
	return nil
}

func (s *launchdService) Uninstall(ctx context.Context) error {
	domain := s.domain()
	_ = launchctl(ctx, "bootout", domain+"/"+s.label)

	if err := os.Remove(s.plistPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove plist: %w", err)
	}

	log.Printf("daemon: LaunchAgent %s uninstalled", s.label)
	return nil
}

func (s *launchdService) Start(ctx context.Context) error {
	domain := s.domain()
	return launchctl(ctx, "bootstrap", domain, s.plistPath)
}

func (s *launchdService) Stop(ctx context.Context) error {
	domain := s.domain()
	return launchctl(ctx, "bootout", domain+"/"+s.label)
}

func (s *launchdService) Restart(ctx context.Context) error {
	domain := s.domain()
	return launchctl(ctx, "kickstart", "-k", domain+"/"+s.label)
}

func (s *launchdService) IsInstalled() (bool, error) {
	_, err := os.Stat(s.plistPath)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	// Check if actually loaded
	domain := s.domain()
	err = launchctl(context.Background(), "print", domain+"/"+s.label)
	return err == nil, nil
}

func (s *launchdService) ReadRuntime() (*ServiceRuntime, error) {
	rt := &ServiceRuntime{Status: "unknown"}

	domain := s.domain()
	res, err := execCommand(context.Background(),
		"launchctl", "print", domain+"/"+s.label,
	)
	if err != nil {
		return rt, err
	}
	if res.ExitCode != 0 {
		rt.Status = "stopped"
		return rt, nil
	}

	// Parse output for pid and state
	for _, line := range strings.Split(res.Stdout, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "pid = ") {
			pidStr := strings.TrimPrefix(line, "pid = ")
			rt.PID, _ = strconv.Atoi(strings.TrimSpace(pidStr))
		}
		if strings.HasPrefix(line, "state = ") {
			state := strings.TrimPrefix(line, "state = ")
			state = strings.TrimSpace(state)
			rt.SubState = state
			if state == "running" {
				rt.Status = "running"
			} else if state == "waiting" {
				rt.Status = "stopped"
			} else {
				rt.Status = state
			}
		}
		if strings.HasPrefix(line, "last exit code = ") {
			codeStr := strings.TrimPrefix(line, "last exit code = ")
			rt.ExitCode, _ = strconv.Atoi(strings.TrimSpace(codeStr))
		}
	}

	return rt, nil
}

// domain returns the launchd domain target for the current user.
func (s *launchdService) domain() string {
	return fmt.Sprintf("gui/%d", os.Getuid())
}

// launchctl runs a launchctl command and returns an error if it fails.
func launchctl(ctx context.Context, args ...string) error {
	res, err := execCommand(ctx, "launchctl", args...)
	if err != nil {
		return fmt.Errorf("launchctl %s: %w", strings.Join(args, " "), err)
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("launchctl %s (exit %d): %s",
			strings.Join(args, " "), res.ExitCode, strings.TrimSpace(res.Stderr))
	}
	return nil
}
