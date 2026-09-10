package daemon

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestXmlEscape(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"simple", "simple"},
		{"a & b", "a &amp; b"},
		{"<tag>", "&lt;tag&gt;"},
		{`he said "hi"`, `he said &quot;hi&quot;`},
		{"it's", "it&apos;s"},
		{`a & "b" <c> 'd'`, `a &amp; &quot;b&quot; &lt;c&gt; &apos;d&apos;`},
	}

	for _, tt := range tests {
		got := xmlEscape(tt.input)
		if got != tt.want {
			t.Errorf("xmlEscape(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestBuildPlist(t *testing.T) {
	p := plistArgs{
		Label:       "com.bomclaw.gateway",
		ProgramArgs: []string{"/usr/local/bin/bomclaw", "gateway", "--port", "18789"},
		Environment: map[string]string{
			"HOME":              "/Users/test",
			"ANTHROPIC_API_KEY": "sk-test",
		},
		StdoutPath: "/Users/test/.goterm/logs/gateway.log",
		StderrPath: "/Users/test/.goterm/logs/gateway.err.log",
	}

	plist := buildPlist(p)

	checks := []string{
		`<?xml version="1.0"`,
		`<key>Label</key>`,
		`<string>com.bomclaw.gateway</string>`,
		`<key>RunAtLoad</key>`,
		`<true/>`,
		`<key>KeepAlive</key>`,
		`<key>ProgramArguments</key>`,
		`<string>/usr/local/bin/bomclaw</string>`,
		`<string>gateway</string>`,
		`<key>EnvironmentVariables</key>`,
		`<key>HOME</key>`,
		`<string>/Users/test</string>`,
		`<key>StandardOutPath</key>`,
		`<string>/Users/test/.goterm/logs/gateway.log</string>`,
		`<key>StandardErrorPath</key>`,
	}

	for _, check := range checks {
		if !strings.Contains(plist, check) {
			t.Errorf("plist missing %q\nGot:\n%s", check, plist)
		}
	}
}

func TestBuildPlistEscaping(t *testing.T) {
	p := plistArgs{
		Label:       "com.test",
		ProgramArgs: []string{"/path/to/binary", "--flag=value with <special> & chars"},
		Environment: map[string]string{
			"KEY": `value "with" quotes & <brackets>`,
		},
	}

	plist := buildPlist(p)

	// Verify escaping
	if strings.Contains(plist, `& chars`) && !strings.Contains(plist, `&amp; chars`) {
		t.Error("unescaped & in program args")
	}
	if strings.Contains(plist, `<special>`) && !strings.Contains(plist, `&lt;special&gt;`) {
		t.Error("unescaped < > in program args")
	}
}

func TestEachAgentGetsItsOwnLogDir(t *testing.T) {
	// Agent 1 keeps the original path — anything already tailing it, or any
	// runbook naming it, must not break on an upgrade.
	// Expected paths go through filepath.Join too: this file is built on every
	// platform even though launchd is darwin-only, and a hardcoded "/" fails
	// on Windows without teaching anyone anything.
	home := filepath.Join("/Users", "x")
	agent1 := filepath.Join(home, ".goterm", "logs")

	if got := launchdLogDirFor(home, "bomclaw"); got != agent1 {
		t.Errorf("agent 1 log dir moved to %q", got)
	}
	if got := launchdLogDirFor(home, ""); got != agent1 {
		t.Errorf("default log dir = %q", got)
	}
	// Everyone else is separate, or three startups interleave in one file.
	if got, want := launchdLogDirFor(home, "bomclaw3"), filepath.Join(home, ".goterm3", "logs"); got != want {
		t.Errorf("agent 3 log dir = %q, want %q", got, want)
	}
	if launchdLogDirFor(home, "bomclaw2") == launchdLogDirFor(home, "bomclaw3") {
		t.Error("two agents share a log file")
	}
}

func TestServiceNamesAreDerivedFromTheAgent(t *testing.T) {
	cases := map[string]string{
		"":         "com.bomclaw.gateway",
		"bomclaw":  "com.bomclaw.gateway",
		"bomclaw2": "com.bomclaw2.gateway",
		"bomclaw3": "com.bomclaw3.gateway",
	}
	for id, want := range cases {
		if got := launchdLabelFor(id); got != want {
			t.Errorf("launchdLabelFor(%q) = %q, want %q", id, got, want)
		}
	}
}
