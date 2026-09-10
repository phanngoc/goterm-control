package daemon

import (
	"context"
	"fmt"
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
