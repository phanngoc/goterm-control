package daemon

import (
	"fmt"
	"strings"
)

// xmlEscape escapes a string for use in an XML plist value.
func xmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, "\"", "&quot;")
	s = strings.ReplaceAll(s, "'", "&apos;")
	return s
}

// plistArgs holds the parameters for generating a launchd plist.
type plistArgs struct {
	Label       string
	ProgramArgs []string
	Environment map[string]string
	WorkingDir  string
	StdoutPath  string
	StderrPath  string
}

// buildPlist generates a launchd plist XML string.
// Ported from openclaw src/daemon/launchd-plist.ts.
func buildPlist(p plistArgs) string {
	var b strings.Builder

	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
	b.WriteString("\n")
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">`)
	b.WriteString("\n")
	b.WriteString(`<plist version="1.0">`)
	b.WriteString("\n<dict>\n")

	// Label
	fmt.Fprintf(&b, "\t<key>Label</key>\n\t<string>%s</string>\n", xmlEscape(p.Label))

	// RunAtLoad + KeepAlive
	b.WriteString("\t<key>RunAtLoad</key>\n\t<true/>\n")
	b.WriteString("\t<key>KeepAlive</key>\n\t<true/>\n")
	b.WriteString("\t<key>ThrottleInterval</key>\n\t<integer>1</integer>\n")

	// ProgramArguments
	b.WriteString("\t<key>ProgramArguments</key>\n\t<array>\n")
	for _, arg := range p.ProgramArgs {
		fmt.Fprintf(&b, "\t\t<string>%s</string>\n", xmlEscape(arg))
	}
	b.WriteString("\t</array>\n")

	// WorkingDirectory
	if p.WorkingDir != "" {
		fmt.Fprintf(&b, "\t<key>WorkingDirectory</key>\n\t<string>%s</string>\n", xmlEscape(p.WorkingDir))
	}

	// EnvironmentVariables
	if len(p.Environment) > 0 {
		b.WriteString("\t<key>EnvironmentVariables</key>\n\t<dict>\n")
		for k, v := range p.Environment {
			fmt.Fprintf(&b, "\t\t<key>%s</key>\n\t\t<string>%s</string>\n", xmlEscape(k), xmlEscape(v))
		}
		b.WriteString("\t</dict>\n")
	}

	// Log paths
	if p.StdoutPath != "" {
		fmt.Fprintf(&b, "\t<key>StandardOutPath</key>\n\t<string>%s</string>\n", xmlEscape(p.StdoutPath))
	}
	if p.StderrPath != "" {
		fmt.Fprintf(&b, "\t<key>StandardErrorPath</key>\n\t<string>%s</string>\n", xmlEscape(p.StderrPath))
	}

	b.WriteString("</dict>\n</plist>\n")
	return b.String()
}
