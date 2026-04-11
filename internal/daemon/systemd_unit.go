package daemon

import (
	"fmt"
	"strings"
)

// systemdEscapeValue escapes a value for use in a systemd Environment= directive.
// Ported from openclaw src/daemon/systemd-unit.ts.
// Rules: if value contains space, quote, or backslash, wrap in quotes with
// backslashes doubled and quotes escaped.
func systemdEscapeValue(s string) string {
	if s == "" {
		return `""`
	}
	needsQuote := false
	for _, c := range s {
		if c == ' ' || c == '\t' || c == '"' || c == '\\' || c == '\n' {
			needsQuote = true
			break
		}
	}
	if !needsQuote {
		return s
	}
	var b strings.Builder
	b.WriteByte('"')
	for _, c := range s {
		switch c {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		default:
			b.WriteRune(c)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// systemdEscapeExecArg escapes a single argument for ExecStart=.
// Systemd splits ExecStart on unquoted whitespace; arguments containing
// spaces, quotes, or backslashes must be quoted.
func systemdEscapeExecArg(s string) string {
	return systemdEscapeValue(s)
}

// buildExecStart builds the ExecStart= line from program arguments.
func buildExecStart(args []string) string {
	parts := make([]string, len(args))
	for i, a := range args {
		parts[i] = systemdEscapeExecArg(a)
	}
	return strings.Join(parts, " ")
}

// buildEnvironmentLines generates Environment= directives for the unit file.
func buildEnvironmentLines(env map[string]string) string {
	if len(env) == 0 {
		return ""
	}
	var b strings.Builder
	for k, v := range env {
		if k == "" || strings.ContainsAny(k, "\n\r") {
			continue
		}
		if strings.ContainsAny(v, "\n\r") {
			continue // systemd doesn't allow CR/LF in env values
		}
		fmt.Fprintf(&b, "Environment=%s=%s\n", k, systemdEscapeValue(v))
	}
	return b.String()
}

// buildSystemdUnit generates a complete systemd user service unit file.
func buildSystemdUnit(args InstallArgs) string {
	description := args.Description
	if description == "" {
		description = "BomClaw Gateway"
	}

	// Build ExecStart arguments
	execArgs := []string{args.BinaryPath, "gateway"}
	if args.ConfigPath != "" {
		execArgs = append(execArgs, "--config", args.ConfigPath)
	}
	if args.EnvFile != "" {
		execArgs = append(execArgs, "--env", args.EnvFile)
	}
	if args.Bind != "" {
		execArgs = append(execArgs, "--bind", args.Bind)
	}
	if args.Port > 0 {
		execArgs = append(execArgs, "--port", fmt.Sprintf("%d", args.Port))
	}

	var b strings.Builder

	// [Unit]
	b.WriteString("[Unit]\n")
	fmt.Fprintf(&b, "Description=%s\n", description)
	b.WriteString("After=network-online.target\n")
	b.WriteString("Wants=network-online.target\n")
	b.WriteString("\n")

	// [Service]
	b.WriteString("[Service]\n")
	fmt.Fprintf(&b, "ExecStart=%s\n", buildExecStart(execArgs))
	b.WriteString("Restart=always\n")
	b.WriteString("RestartSec=5\n")
	b.WriteString("TimeoutStopSec=30\n")
	b.WriteString("TimeoutStartSec=30\n")
	b.WriteString("SuccessExitStatus=0 143\n")
	b.WriteString("KillMode=control-group\n")

	// EnvironmentFile (optional, prefix with - so missing file is not an error)
	if args.EnvFile != "" {
		fmt.Fprintf(&b, "EnvironmentFile=-%s\n", args.EnvFile)
	}

	// Inline environment variables
	b.WriteString(buildEnvironmentLines(args.Environment))
	b.WriteString("\n")

	// [Install]
	b.WriteString("[Install]\n")
	b.WriteString("WantedBy=default.target\n")

	return b.String()
}
