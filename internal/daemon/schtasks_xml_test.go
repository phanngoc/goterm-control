package daemon

import (
	"strings"
	"testing"
)

func TestBuildTaskXMLCarriesTheAction(t *testing.T) {
	xml := buildTaskXML(taskXMLArgs{
		URI:         `\BomClaw\bomclaw-gateway`,
		Description: "BomClaw Gateway",
		UserID:      `DESKTOP-1\ngoc`,
		Command:     `C:\Users\ngoc\.bomclaw\bomclaw.exe`,
		Arguments:   `gateway --port 18789`,
		WorkingDir:  `C:\Users\ngoc\.bomclaw`,
	})

	for _, want := range []string{
		`encoding="UTF-16"`,
		`<URI>\BomClaw\bomclaw-gateway</URI>`,
		`<UserId>DESKTOP-1\ngoc</UserId>`,
		`<Command>C:\Users\ngoc\.bomclaw\bomclaw.exe</Command>`,
		`<Arguments>gateway --port 18789</Arguments>`,
		`<WorkingDirectory>C:\Users\ngoc\.bomclaw</WorkingDirectory>`,
		"<LogonTrigger>",
		"<LogonType>InteractiveToken</LogonType>",
	} {
		if !strings.Contains(xml, want) {
			t.Errorf("task XML missing %q", want)
		}
	}
}

// A task that stops after 72 hours, or refuses to run on battery, is not a
// gateway. Both are Task Scheduler defaults, so both have to be overridden.
func TestBuildTaskXMLRunsIndefinitely(t *testing.T) {
	xml := buildTaskXML(taskXMLArgs{Command: `C:\bomclaw.exe`})

	for _, want := range []string{
		"<ExecutionTimeLimit>PT0S</ExecutionTimeLimit>",
		"<DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>",
		"<StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>",
		"<RestartOnFailure>",
	} {
		if !strings.Contains(xml, want) {
			t.Errorf("task XML missing %q", want)
		}
	}
}

func TestBuildTaskXMLEscapesValues(t *testing.T) {
	xml := buildTaskXML(taskXMLArgs{
		Description: `Bom & Claw <gateway>`,
		Command:     `C:\bomclaw.exe`,
		Arguments:   `--note "a & b"`,
	})

	if strings.Contains(xml, "Bom & Claw") {
		t.Error("a bare & would make the definition invalid XML")
	}
	if !strings.Contains(xml, "Bom &amp; Claw &lt;gateway&gt;") {
		t.Errorf("description not escaped:\n%s", xml)
	}
	if !strings.Contains(xml, "--note &quot;a &amp; b&quot;") {
		t.Errorf("arguments not escaped:\n%s", xml)
	}
}

func TestBuildTaskXMLOmitsEmptyOptionalFields(t *testing.T) {
	xml := buildTaskXML(taskXMLArgs{Command: `C:\bomclaw.exe`})

	for _, unwanted := range []string{"<Arguments>", "<WorkingDirectory>", "<URI>", "<UserId>"} {
		if strings.Contains(xml, unwanted) {
			t.Errorf("expected no %s when the field is unset:\n%s", unwanted, xml)
		}
	}
	// The description still gets a default: an unlabelled task in the Task
	// Scheduler UI is not something anyone should have to identify.
	if !strings.Contains(xml, "<Description>BomClaw Gateway</Description>") {
		t.Error("expected a default description")
	}
}

func TestWindowsQuoteArg(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "gateway", "gateway"},
		{"empty", "", `""`},
		{"path without spaces", `C:\bomclaw\config.yaml`, `C:\bomclaw\config.yaml`},
		{"path with spaces", `C:\Some Name\config.yaml`, `"C:\Some Name\config.yaml"`},
		// A trailing backslash before the closing quote would escape it and
		// swallow the next argument, so it has to be doubled.
		{"trailing backslash", `C:\Some Name\`, `"C:\Some Name\\"`},
		{"embedded quote", `a "b" c`, `"a \"b\" c"`},
		{"backslashes before a quote", `a\\"b`, `"a\\\\\"b"`},
		{"tab", "a\tb", "\"a\tb\""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := windowsQuoteArg(tc.in); got != tc.want {
				t.Errorf("windowsQuoteArg(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestBuildTaskArgumentsQuotesOnlyWhatNeedsIt(t *testing.T) {
	got := buildTaskArguments([]string{
		"gateway", "--config", `C:\Some Name\config.yaml`, "--port", "18789",
	})
	want := `gateway --config "C:\Some Name\config.yaml" --port 18789`
	if got != want {
		t.Errorf("buildTaskArguments = %q, want %q", got, want)
	}
}

func TestBuildGatewayArgs(t *testing.T) {
	got := buildGatewayArgs(InstallArgs{
		BinaryPath: `C:\bomclaw.exe`,
		ConfigPath: `C:\cfg.yaml`,
		EnvFile:    `C:\.env`,
		Bind:       "127.0.0.1",
		Port:       18789,
	})
	want := []string{
		"gateway",
		"--config", `C:\cfg.yaml`,
		"--env", `C:\.env`,
		"--bind", "127.0.0.1",
		"--port", "18789",
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("buildGatewayArgs = %v, want %v", got, want)
	}

	// Zero values are flags the gateway should fall back to its own defaults
	// for, not flags passed empty.
	if got := buildGatewayArgs(InstallArgs{}); len(got) != 1 || got[0] != "gateway" {
		t.Errorf("empty InstallArgs = %v, want [gateway]", got)
	}
}

func TestWrapWithLogRedirect(t *testing.T) {
	t.Setenv("COMSPEC", `C:\Windows\System32\cmd.exe`)

	command, arguments := wrapWithLogRedirect(
		`C:\Program Files\bomclaw.exe`,
		[]string{"gateway", "--port", "18789"},
		`C:\logs\gateway.log`,
		`C:\logs\gateway.err.log`,
	)

	if command != `C:\Windows\System32\cmd.exe` {
		t.Errorf("command = %q, want COMSPEC", command)
	}

	want := `/s /c "` +
		`"C:\Program Files\bomclaw.exe" "gateway" "--port" "18789"` +
		` 1>>"C:\logs\gateway.log" 2>>"C:\logs\gateway.err.log""`
	if arguments != want {
		t.Errorf("arguments =\n  %q\nwant\n  %q", arguments, want)
	}

	// /s is what makes the quoting deterministic — without it cmd's handling of
	// a line with more than two quotes is not something to rely on.
	if !strings.HasPrefix(arguments, "/s /c ") {
		t.Error("the wrapper must pass /s")
	}
}

func TestWrapWithLogRedirectFallsBackWithoutComspec(t *testing.T) {
	t.Setenv("COMSPEC", "")
	command, _ := wrapWithLogRedirect(`C:\bomclaw.exe`, nil, "o", "e")
	if command != `C:\Windows\System32\cmd.exe` {
		t.Errorf("command = %q, want the hardcoded cmd.exe fallback", command)
	}
}

func TestCmdSafe(t *testing.T) {
	safe := []string{`C:\Users\ngoc\.bomclaw\bomclaw.exe`, `C:\Some Name\cfg.yaml`, "--port", "18789"}
	if !cmdSafe(safe) {
		t.Errorf("ordinary paths must be considered safe: %v", safe)
	}

	// cmd.exe has no escape for a double quote, and expands %VAR% even inside
	// quotes — either one silently changes what gets executed.
	for _, bad := range []string{`C:\a"b\cfg.yaml`, `C:\100%%\cfg.yaml`, `C:\%TEMP%\cfg.yaml`} {
		if cmdSafe([]string{`C:\bomclaw.exe`, bad}) {
			t.Errorf("cmdSafe should reject %q", bad)
		}
	}
}

// schtasks /Create /XML rejects a definition whose bytes disagree with its
// declared encoding, and buildTaskXML declares UTF-16.
func TestEncodeUTF16LE(t *testing.T) {
	got := encodeUTF16LE("Ab")

	want := []byte{0xFF, 0xFE, 'A', 0x00, 'b', 0x00}
	if len(got) != len(want) {
		t.Fatalf("encodeUTF16LE(%q) = % x, want % x", "Ab", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("encodeUTF16LE(%q) = % x, want % x", "Ab", got, want)
		}
	}
}

func TestEncodeUTF16LEHandlesAstralPlane(t *testing.T) {
	// A surrogate pair must come out as two code units, not one replacement
	// character — the description field can hold anything the user typed.
	got := encodeUTF16LE("\U0001F980") // 🦀
	want := []byte{0xFF, 0xFE, 0x3E, 0xD8, 0x80, 0xDD}
	if len(got) != len(want) {
		t.Fatalf("got % x, want % x", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got % x, want % x", got, want)
		}
	}
}

func TestParseTaskResult(t *testing.T) {
	tests := map[string]int{
		"0":        0,
		"267011":   267011,
		"0x1":      1,
		"0x41301":  267009,
		"":         0,
		"not a no": 0,
	}
	for in, want := range tests {
		if got := parseTaskResult(in); got != want {
			t.Errorf("parseTaskResult(%q) = %d, want %d", in, got, want)
		}
	}
}
