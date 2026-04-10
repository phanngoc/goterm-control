package daemon

import (
	"strings"
	"testing"
)

func TestSystemdEscapeValue(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"simple", "simple"},
		{"", `""`},
		{"has space", `"has space"`},
		{`has"quote`, `"has\"quote"`},
		{`has\back`, `"has\\back"`},
		{"has\nnewline", `"has\nnewline"`},
		{"/usr/bin/node", "/usr/bin/node"},
		{"sk-ant-api03-abc123", "sk-ant-api03-abc123"},
		{`path with "quotes" and spaces`, `"path with \"quotes\" and spaces"`},
		{`C:\Users\test`, `"C:\\Users\\test"`},
	}

	for _, tt := range tests {
		got := systemdEscapeValue(tt.input)
		if got != tt.want {
			t.Errorf("systemdEscapeValue(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestBuildExecStart(t *testing.T) {
	args := []string{"/usr/local/bin/nanoclaw", "gateway", "--port", "18789"}
	got := buildExecStart(args)
	want := "/usr/local/bin/nanoclaw gateway --port 18789"
	if got != want {
		t.Errorf("buildExecStart = %q, want %q", got, want)
	}

	// With path containing spaces
	args2 := []string{"/opt/my app/nanoclaw", "gateway"}
	got2 := buildExecStart(args2)
	if !strings.HasPrefix(got2, `"/opt/my app/nanoclaw"`) {
		t.Errorf("buildExecStart with spaces = %q, expected quoted path", got2)
	}
}

func TestBuildEnvironmentLines(t *testing.T) {
	env := map[string]string{
		"HOME":             "/home/user",
		"ANTHROPIC_API_KEY": "sk-test-123",
	}
	got := buildEnvironmentLines(env)

	if !strings.Contains(got, "Environment=HOME=/home/user") {
		t.Errorf("missing HOME in env lines: %s", got)
	}
	if !strings.Contains(got, "Environment=ANTHROPIC_API_KEY=sk-test-123") {
		t.Errorf("missing API key in env lines: %s", got)
	}

	// Should skip keys/values with newlines
	envBad := map[string]string{"BAD": "has\nnewline"}
	gotBad := buildEnvironmentLines(envBad)
	if strings.Contains(gotBad, "BAD") {
		t.Errorf("should skip env with newlines: %s", gotBad)
	}
}

func TestBuildSystemdUnit(t *testing.T) {
	args := InstallArgs{
		BinaryPath:  "/usr/local/bin/nanoclaw",
		Port:        18789,
		Bind:        "127.0.0.1",
		ConfigPath:  "/home/user/config.yaml",
		EnvFile:     "/home/user/.env",
		Description: "NanoClaw Gateway",
		Environment: map[string]string{
			"HOME": "/home/user",
		},
	}

	unit := buildSystemdUnit(args)

	checks := []string{
		"[Unit]",
		"Description=NanoClaw Gateway",
		"After=network-online.target",
		"[Service]",
		"ExecStart=/usr/local/bin/nanoclaw gateway --config /home/user/config.yaml --env /home/user/.env --bind 127.0.0.1 --port 18789",
		"Restart=always",
		"RestartSec=5",
		"SuccessExitStatus=0 143",
		"KillMode=control-group",
		"EnvironmentFile=-/home/user/.env",
		"Environment=HOME=/home/user",
		"[Install]",
		"WantedBy=default.target",
	}

	for _, check := range checks {
		if !strings.Contains(unit, check) {
			t.Errorf("unit file missing %q\nGot:\n%s", check, unit)
		}
	}
}
