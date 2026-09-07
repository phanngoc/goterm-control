package daemon

import (
	"encoding/binary"
	"fmt"
	"strings"
	"unicode/utf16"
)

// taskXMLArgs holds the parameters for generating a Task Scheduler definition.
type taskXMLArgs struct {
	URI         string // task path, e.g. \BomClaw\bomclaw-gateway
	Description string // shown in the Task Scheduler UI
	UserID      string // DOMAIN\user the task runs as
	Command     string // absolute path to the executable
	Arguments   string // raw command-line string, already quoted by the caller
	WorkingDir  string // optional working directory
}

// buildTaskXML generates a Windows Task Scheduler task definition.
//
// This is the Windows counterpart of buildSystemdUnit and buildPlist. A
// Scheduled Task with a logon trigger — not a Windows Service — is the right
// analogue of a `systemd --user` unit or a LaunchAgent: all three run as the
// logged-in user, inside that user's interactive session. A real service would
// be placed in session 0, where it can see neither the desktop nor the user's
// browser profile, which is most of what this gateway exists to drive. It also
// would not install without Administrator.
//
// Two settings carry the weight here:
//
//   - ExecutionTimeLimit PT0S means "no limit". The default is 72 hours, after
//     which Task Scheduler would stop the gateway on its own.
//   - RestartOnFailure is the closest thing to systemd's Restart=always. It
//     only fires when the process exits non-zero, so a clean exit stays exited,
//     the same way SuccessExitStatus behaves in the systemd unit.
func buildTaskXML(a taskXMLArgs) string {
	description := a.Description
	if description == "" {
		description = "BomClaw Gateway"
	}

	var b strings.Builder

	b.WriteString(`<?xml version="1.0" encoding="UTF-16"?>` + "\n")
	b.WriteString(`<Task version="1.4" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">` + "\n")

	b.WriteString("  <RegistrationInfo>\n")
	fmt.Fprintf(&b, "    <Description>%s</Description>\n", xmlEscape(description))
	if a.URI != "" {
		fmt.Fprintf(&b, "    <URI>%s</URI>\n", xmlEscape(a.URI))
	}
	b.WriteString("  </RegistrationInfo>\n")

	// Start at logon rather than at boot: before logon there is no interactive
	// session for the gateway to attach to.
	b.WriteString("  <Triggers>\n")
	b.WriteString("    <LogonTrigger>\n")
	b.WriteString("      <Enabled>true</Enabled>\n")
	if a.UserID != "" {
		fmt.Fprintf(&b, "      <UserId>%s</UserId>\n", xmlEscape(a.UserID))
	}
	b.WriteString("    </LogonTrigger>\n")
	b.WriteString("  </Triggers>\n")

	b.WriteString("  <Principals>\n")
	b.WriteString(`    <Principal id="Author">` + "\n")
	if a.UserID != "" {
		fmt.Fprintf(&b, "      <UserId>%s</UserId>\n", xmlEscape(a.UserID))
	}
	// InteractiveToken: run only when the user is logged on, with their own
	// desktop and profile. LeastPrivilege keeps it out of an elevated token,
	// so installing needs no Administrator prompt.
	b.WriteString("      <LogonType>InteractiveToken</LogonType>\n")
	b.WriteString("      <RunLevel>LeastPrivilege</RunLevel>\n")
	b.WriteString("    </Principal>\n")
	b.WriteString("  </Principals>\n")

	b.WriteString("  <Settings>\n")
	// IgnoreNew: a manual `gateway start` while it is already up is a no-op
	// rather than a second gateway fighting for the port.
	b.WriteString("    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>\n")
	// A laptop on battery is still expected to answer Telegram.
	b.WriteString("    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>\n")
	b.WriteString("    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>\n")
	b.WriteString("    <AllowHardTerminate>true</AllowHardTerminate>\n")
	b.WriteString("    <StartWhenAvailable>true</StartWhenAvailable>\n")
	b.WriteString("    <RunOnlyIfNetworkAvailable>false</RunOnlyIfNetworkAvailable>\n")
	b.WriteString("    <IdleSettings>\n")
	b.WriteString("      <StopOnIdleEnd>false</StopOnIdleEnd>\n")
	b.WriteString("      <RestartOnIdle>false</RestartOnIdle>\n")
	b.WriteString("    </IdleSettings>\n")
	b.WriteString("    <AllowStartOnDemand>true</AllowStartOnDemand>\n")
	b.WriteString("    <Enabled>true</Enabled>\n")
	b.WriteString("    <Hidden>false</Hidden>\n")
	b.WriteString("    <RunOnlyIfIdle>false</RunOnlyIfIdle>\n")
	b.WriteString("    <DisallowStartOnRemoteAppSession>false</DisallowStartOnRemoteAppSession>\n")
	b.WriteString("    <UseUnifiedSchedulingEngine>true</UseUnifiedSchedulingEngine>\n")
	b.WriteString("    <WakeToRun>false</WakeToRun>\n")
	// PT0S = run indefinitely. The default is 72 hours.
	b.WriteString("    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>\n")
	b.WriteString("    <Priority>7</Priority>\n")
	b.WriteString("    <RestartOnFailure>\n")
	b.WriteString("      <Interval>PT1M</Interval>\n")
	b.WriteString("      <Count>99</Count>\n")
	b.WriteString("    </RestartOnFailure>\n")
	b.WriteString("  </Settings>\n")

	b.WriteString(`  <Actions Context="Author">` + "\n")
	b.WriteString("    <Exec>\n")
	fmt.Fprintf(&b, "      <Command>%s</Command>\n", xmlEscape(a.Command))
	if a.Arguments != "" {
		fmt.Fprintf(&b, "      <Arguments>%s</Arguments>\n", xmlEscape(a.Arguments))
	}
	if a.WorkingDir != "" {
		fmt.Fprintf(&b, "      <WorkingDirectory>%s</WorkingDirectory>\n", xmlEscape(a.WorkingDir))
	}
	b.WriteString("    </Exec>\n")
	b.WriteString("  </Actions>\n")

	b.WriteString("</Task>\n")
	return b.String()
}

// buildTaskArguments joins arguments into the single string the Arguments
// element holds, quoting each one so CreateProcess splits it back the same way.
func buildTaskArguments(args []string) string {
	parts := make([]string, len(args))
	for i, a := range args {
		parts[i] = windowsQuoteArg(a)
	}
	return strings.Join(parts, " ")
}

// windowsQuoteArg quotes one argument for a Windows command line, following the
// CommandLineToArgvW rules that CreateProcess applies. A config path under
// "C:\Users\Some Name\..." has to survive the round trip intact.
func windowsQuoteArg(s string) string {
	if s == "" {
		return `""`
	}
	if !strings.ContainsAny(s, " \t\n\v\"") {
		return s
	}

	var b strings.Builder
	b.WriteByte('"')
	backslashes := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			backslashes++
		case '"':
			// Backslashes before a quote are doubled, then the quote escaped.
			b.WriteString(strings.Repeat(`\`, backslashes*2+1))
			b.WriteByte('"')
			backslashes = 0
		default:
			b.WriteString(strings.Repeat(`\`, backslashes))
			b.WriteByte(s[i])
			backslashes = 0
		}
	}
	// Trailing backslashes precede the closing quote, so they are doubled too.
	b.WriteString(strings.Repeat(`\`, backslashes*2))
	b.WriteByte('"')
	return b.String()
}

// encodeUTF16LE encodes s as UTF-16LE with a byte-order mark.
//
// schtasks /Create /XML rejects a definition whose bytes do not match its
// declared encoding — it fails with "The task XML contains a value which is
// incorrectly formatted or out of range", which says nothing about encoding.
// buildTaskXML declares UTF-16, so the file written to disk has to be UTF-16LE
// with a BOM.
func encodeUTF16LE(s string) []byte {
	units := utf16.Encode([]rune(s))
	buf := make([]byte, 0, 2+len(units)*2)
	buf = binary.LittleEndian.AppendUint16(buf, 0xFEFF) // BOM
	for _, u := range units {
		buf = binary.LittleEndian.AppendUint16(buf, u)
	}
	return buf
}
