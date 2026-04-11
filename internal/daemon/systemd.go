package daemon

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
)

const systemdServiceName = "bomclaw-gateway"

type systemdService struct {
	unitName string
	unitDir  string
	unitPath string
}

func newSystemdService() (*systemdService, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve home dir: %w", err)
	}
	unitDir := filepath.Join(home, ".config", "systemd", "user")
	unitName := systemdServiceName + ".service"
	return &systemdService{
		unitName: unitName,
		unitDir:  unitDir,
		unitPath: filepath.Join(unitDir, unitName),
	}, nil
}

func (s *systemdService) Label() string { return "systemd" }

func (s *systemdService) UnitPath() string { return s.unitPath }

func (s *systemdService) Install(ctx context.Context, args InstallArgs) error {
	// 1. Create unit directory
	if err := os.MkdirAll(s.unitDir, 0755); err != nil {
		return fmt.Errorf("create unit dir: %w", err)
	}

	// 2. Backup existing unit file
	if _, err := os.Stat(s.unitPath); err == nil {
		bakPath := s.unitPath + ".bak"
		data, _ := os.ReadFile(s.unitPath)
		if len(data) > 0 {
			_ = os.WriteFile(bakPath, data, 0644)
			log.Printf("daemon: backed up existing unit to %s", bakPath)
		}
	}

	// 3. Generate and write unit file
	unit := buildSystemdUnit(args)
	if err := os.WriteFile(s.unitPath, []byte(unit), 0644); err != nil {
		return fmt.Errorf("write unit file: %w", err)
	}
	log.Printf("daemon: wrote %s", s.unitPath)

	// 4. Reload, enable, restart
	if _, err := systemctlUser(ctx, "daemon-reload"); err != nil {
		return fmt.Errorf("daemon-reload: %w", err)
	}
	if _, err := systemctlUser(ctx, "enable", s.unitName); err != nil {
		return fmt.Errorf("enable: %w", err)
	}
	if _, err := systemctlUser(ctx, "restart", s.unitName); err != nil {
		return fmt.Errorf("restart: %w", err)
	}

	log.Printf("daemon: service %s installed and started", s.unitName)
	return nil
}

func (s *systemdService) Uninstall(ctx context.Context) error {
	// disable --now: disable + stop in one command
	if _, err := systemctlUser(ctx, "disable", "--now", s.unitName); err != nil {
		log.Printf("daemon: disable failed (may not be loaded): %v", err)
	}

	if err := os.Remove(s.unitPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove unit file: %w", err)
	}

	if _, err := systemctlUser(ctx, "daemon-reload"); err != nil {
		log.Printf("daemon: daemon-reload after uninstall: %v", err)
	}

	log.Printf("daemon: service %s uninstalled", s.unitName)
	return nil
}

func (s *systemdService) Start(ctx context.Context) error {
	_, err := systemctlUser(ctx, "start", s.unitName)
	return err
}

func (s *systemdService) Stop(ctx context.Context) error {
	_, err := systemctlUser(ctx, "stop", s.unitName)
	return err
}

func (s *systemdService) Restart(ctx context.Context) error {
	_, err := systemctlUser(ctx, "restart", s.unitName)
	return err
}

func (s *systemdService) IsInstalled() (bool, error) {
	res, err := systemctlUser(context.Background(), "is-enabled", s.unitName)
	if err != nil {
		return false, err
	}
	stdout := strings.TrimSpace(res.Stdout)
	// "enabled" or "enabled-runtime" means installed
	return stdout == "enabled" || stdout == "enabled-runtime", nil
}

func (s *systemdService) ReadRuntime() (*ServiceRuntime, error) {
	res, err := systemctlUser(context.Background(),
		"show", s.unitName,
		"--no-page",
		"--property=ActiveState,SubState,MainPID,ExecMainStatus,ExecMainCode",
	)
	if err != nil {
		return nil, err
	}

	rt := &ServiceRuntime{Status: "unknown"}
	for _, line := range strings.Split(res.Stdout, "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch k {
		case "ActiveState":
			switch v {
			case "active":
				rt.Status = "running"
			case "failed":
				rt.Status = "failed"
			case "inactive":
				rt.Status = "stopped"
			default:
				rt.Status = v
			}
		case "SubState":
			rt.SubState = v
		case "MainPID":
			rt.PID, _ = strconv.Atoi(v)
		case "ExecMainStatus":
			rt.ExitCode, _ = strconv.Atoi(v)
		case "ExecMainCode":
			rt.ExitReason = v
		}
	}

	return rt, nil
}

// systemctlUser runs systemctl --user with fallback for sudo context.
// Ported from openclaw's execSystemctlUser which handles SUDO_USER
// and user bus unavailability.
func systemctlUser(ctx context.Context, args ...string) (*ExecResult, error) {
	if !commandExists("systemctl") {
		return nil, fmt.Errorf("systemctl not found — is systemd installed?")
	}

	// Primary: systemctl --user <args>
	fullArgs := append([]string{"--user"}, args...)
	res, err := execCommand(ctx, "systemctl", fullArgs...)
	if err != nil {
		return nil, err
	}

	// Check for user bus unavailability (common in SSH/container contexts)
	if res.ExitCode != 0 && isUserBusError(res.Stderr) {
		// Fallback: try --machine <user>@ scope (works when XDG_RUNTIME_DIR is missing)
		u, uErr := user.Current()
		if uErr == nil {
			machineArgs := append([]string{"--machine", u.Username + "@", "--user"}, args...)
			res2, err2 := execCommand(ctx, "systemctl", machineArgs...)
			if err2 == nil && res2.ExitCode == 0 {
				return res2, nil
			}
		}
		return res, fmt.Errorf(
			"systemd user bus unavailable (exit %d).\n"+
				"If via SSH, try: export XDG_RUNTIME_DIR=/run/user/$(id -u)\n"+
				"If WSL, enable systemd in /etc/wsl.conf:\n"+
				"  [boot]\n  systemd=true\n"+
				"stderr: %s", res.ExitCode, strings.TrimSpace(res.Stderr))
	}

	if res.ExitCode != 0 {
		// Non-zero exit is normal for is-enabled when disabled
		if len(args) > 0 && args[0] == "is-enabled" {
			return res, nil
		}
		return res, fmt.Errorf("systemctl %s failed (exit %d): %s",
			strings.Join(args, " "), res.ExitCode, strings.TrimSpace(res.Stderr))
	}

	return res, nil
}

// isUserBusError detects systemd user bus/session unavailability from stderr.
func isUserBusError(stderr string) bool {
	s := strings.ToLower(stderr)
	return strings.Contains(s, "failed to connect to bus") ||
		strings.Contains(s, "no such file or directory") && strings.Contains(s, "dbus") ||
		strings.Contains(s, "xdg_runtime_dir")
}
