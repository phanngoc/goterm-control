package daemon

import (
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
		Label:       "com.nanoclaw.gateway",
		ProgramArgs: []string{"/usr/local/bin/nanoclaw", "gateway", "--port", "18789"},
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
		`<string>com.nanoclaw.gateway</string>`,
		`<key>RunAtLoad</key>`,
		`<true/>`,
		`<key>KeepAlive</key>`,
		`<key>ProgramArguments</key>`,
		`<string>/usr/local/bin/nanoclaw</string>`,
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
