package daemon

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"
)

// InstallArgs holds everything needed to write and activate a service.
type InstallArgs struct {
	BinaryPath  string            // absolute path to the bomclaw binary
	Port        int               // gateway port
	Bind        string            // bind address
	ConfigPath  string            // absolute path to config.yaml
	EnvFile     string            // absolute path to .env (used as EnvironmentFile=)
	Environment map[string]string // extra env vars (HOME, API keys)
	Description string            // service description
	Force       bool              // force reinstall
}

// ServiceRuntime holds the live state of a managed service.
type ServiceRuntime struct {
	Status     string // "running", "stopped", "failed", "unknown"
	SubState   string // platform-specific detail (e.g. "running", "dead")
	PID        int    // main process PID (0 if not running)
	ExitCode   int    // last exit code
	ExitReason string // last exit reason
}

// Service is the platform-agnostic interface for daemon lifecycle management.
type Service interface {
	// Label returns the human-readable backend name (e.g. "systemd", "LaunchAgent").
	Label() string

	// Install writes the service configuration and activates it.
	Install(ctx context.Context, args InstallArgs) error

	// Uninstall stops and removes the service.
	Uninstall(ctx context.Context) error

	// Start starts the service.
	Start(ctx context.Context) error

	// Stop stops the service.
	Stop(ctx context.Context) error

	// Restart restarts the service.
	Restart(ctx context.Context) error

	// IsInstalled returns true if the service is registered and enabled.
	IsInstalled() (bool, error)

	// ReadRuntime reads the live runtime state.
	ReadRuntime() (*ServiceRuntime, error)

	// UnitPath returns the path to the service definition file.
	UnitPath() string
}

// DefaultAgentID is the agent a service command acts on when none is named.
// It is also the one agent whose unit keeps the original, unsuffixed name, so
// upgrading does not orphan the service that is already running.
const DefaultAgentID = "bomclaw"

// unitSuffix is what distinguishes one agent's service from another's.
// Agent 2 was set up by hand as com.bomclaw2.gateway before any of this
// existed; keeping that shape means the existing plists stay valid and only
// new agents are new.
func unitSuffix(agentID string) string {
	if agentID == "" || agentID == DefaultAgentID {
		return ""
	}
	return strings.TrimPrefix(agentID, DefaultAgentID)
}

// Resolve returns the Service that manages one agent's gateway. Every agent
// on this machine runs its own service off the same binary, so the id is what
// selects between them.
func Resolve(agentID string) (Service, error) {
	switch runtime.GOOS {
	case "linux":
		return newSystemdService(agentID)
	case "darwin":
		return newLaunchdService(agentID)
	case "windows":
		return newSchtasksService(agentID)
	default:
		return nil, fmt.Errorf("daemon service not supported on %s", runtime.GOOS)
	}
}

// Revive brings one agent's gateway up, whatever state it is in: running,
// stopped, or booted out of the service manager entirely. It exists because
// the one moment a person most wants a restart button is the moment the agent
// is not answering — and every path that asks the agent to restart itself is
// closed exactly then.
//
// The unit file is the locality check. Every agent on this machine has one;
// an agent that registered from somewhere else does not, and saying so names
// the real problem instead of failing inside launchctl with a service label
// nobody recognises.
func Revive(ctx context.Context, agentID string) error {
	svc, err := Resolve(agentID)
	if err != nil {
		return err
	}
	if _, err := os.Stat(svc.UnitPath()); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("no %s for %s on this machine (%s) — it is managed somewhere else",
				svc.Label(), agentID, svc.UnitPath())
		}
		return err
	}
	// Restart is the right verb for a job the manager still holds, and the
	// wrong one for a job that was booted out: launchctl kickstart answers
	// "Could not find service" for something whose plist is sitting right
	// there. Start is what loads it back. Trying the second only after the
	// first fails keeps a running agent from being stopped and left down.
	rerr := svc.Restart(ctx)
	if rerr == nil {
		return nil
	}
	if serr := svc.Start(ctx); serr != nil {
		return fmt.Errorf("restart: %v; start: %w", rerr, serr)
	}
	return nil
}
