// Package fsguard keeps the agents' CLIs out of the folders macOS guards with
// TCC, so a stray search never pops a permission dialog.
//
// The gateway runs under launchd as com.bomclaw.gateway, and every claude,
// codex and opencode process it starts — and everything those go on to run —
// is attributed to that identity. The first time any of them reads ~/Music,
// ~/Pictures, ~/Downloads, an app's container or iCloud Drive, macOS stops
// and asks the person at the machine. One `find ~ -name x` touches all of them
// in the same second; that is exactly what happened on 2026-11-03 at 20:07,
// from an agent looking for a script it had lost track of.
//
// Asking the agents not to (a prompt section, Claude's permissions.deny
// strings) was already in place and did not hold: the deny rules only load in
// the agent's own workspace, match command strings rather than paths, and say
// nothing to codex or opencode. So the guard is enforced by the OS instead:
// each CLI is started under sandbox-exec with a profile that denies those
// paths. A denied access fails with EPERM before tccd is ever consulted —
// verified with a fresh, never-granted identity launched by launchd — so there
// is no dialog, only "Operation not permitted", which the prompt note explains.
package fsguard

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
)

// DefaultDeny is what the guard blocks unless config takes a path off it,
// relative to $HOME.
//
// Each entry is a TCC folder class (it would prompt, or it is Full Disk Access
// data that is denied without asking) that no agent here has opened a file in:
// across every agent transcript on this machine, these appear only as lines of
// `ls ~` output. ~/Documents is deliberately absent — the source repo lives
// there and the agents edit it.
var DefaultDeny = []string{
	"Desktop",
	"Downloads",
	"Movies",
	"Music",
	"OrbStack", // a network mount: a walk into it stalls as well as prompts
	"Pictures",
	"Library/Application Support/AddressBook",
	"Library/Calendars",
	"Library/Containers",
	"Library/Group Containers",
	"Library/Mail",
	"Library/Messages",
	"Library/Mobile Documents", // iCloud Drive
	"Library/Safari",
}

// Options is the guard's configuration as the gateway hands it over. A plain
// struct rather than config.SecurityConfig keeps this package a leaf.
type Options struct {
	Enabled bool
	Home    string
	Deny    []string // added to DefaultDeny
	Allow   []string // taken off the result, e.g. "~/Downloads"
}

type state struct {
	paths   []string // absolute, sorted, deduplicated
	profile string
}

var current atomic.Pointer[state]

// Configure sets the guard for every command started after it returns. Called
// once at startup; a disabled or empty guard turns Command into exec.Command.
func Configure(o Options) {
	if !o.Enabled || !supported() {
		current.Store(nil)
		return
	}
	paths := Resolve(o)
	if len(paths) == 0 {
		current.Store(nil)
		return
	}
	current.Store(&state{paths: paths, profile: Profile(paths)})
}

// Paths lists what the guard blocks right now; nil when it is off.
func Paths() []string {
	if s := current.Load(); s != nil {
		return append([]string(nil), s.paths...)
	}
	return nil
}

// Resolve turns options into the absolute paths to deny: defaults plus extras,
// minus anything allowed. An allowed path also lifts everything beneath it, so
// allowing "~/Library" frees the containers too.
func Resolve(o Options) []string {
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		if p = expand(p, o.Home); p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, p := range DefaultDeny {
		add(filepath.Join(o.Home, p))
	}
	for _, p := range o.Deny {
		add(p)
	}
	var allowed []string
	for _, p := range o.Allow {
		if p = expand(p, o.Home); p != "" {
			allowed = append(allowed, p)
		}
	}
	kept := out[:0]
	for _, p := range out {
		if !under(p, allowed) {
			kept = append(kept, p)
		}
	}
	sort.Strings(kept)
	return kept
}

func under(p string, roots []string) bool {
	for _, r := range roots {
		if p == r || strings.HasPrefix(p, r+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// expand makes a configured path absolute: "~" and "~/x" become home-relative,
// a relative path is taken as relative to home, and the result is cleaned.
// "/" and the home directory itself are refused — denying either would deny
// the CLI its own config and stop every turn.
func expand(p, home string) string {
	p = strings.TrimSpace(p)
	switch {
	case p == "":
		return ""
	case p == "~":
		p = home
	case strings.HasPrefix(p, "~/"):
		p = filepath.Join(home, p[2:])
	case !filepath.IsAbs(p):
		p = filepath.Join(home, p)
	}
	p = filepath.Clean(p)
	if p == "/" || p == filepath.Clean(home) {
		return ""
	}
	return p
}

// Profile is the sandbox profile denying paths. Everything else stays allowed:
// the guard is a fence around a few folders, not a sandbox for the agent.
//
// The folder itself stays visible — its metadata can be read, so `ls ~` and
// `ls -ld ~/Music` work — and only listing it, or anything inside it, is
// refused. That is the line TCC draws as well.
func Profile(paths []string) string {
	var b strings.Builder
	b.WriteString("(version 1)\n(allow default)\n")
	for _, p := range paths {
		q := quote(p)
		b.WriteString("(deny file-read-data (literal " + q + "))\n")
		b.WriteString("(deny file-read* file-write* (prefix " + quote(p+"/") + "))\n")
	}
	return b.String()
}

// quote renders s as an SBPL string literal.
func quote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + r.Replace(s) + `"`
}

// PromptNote tells the agent what the guard is, so an EPERM reads as a fence
// rather than a fault to work around. Empty when the guard is off.
func PromptNote() string {
	s := current.Load()
	if s == nil {
		return ""
	}
	home, _ := os.UserHomeDir()
	names := make([]string, 0, len(s.paths))
	for _, p := range s.paths {
		if home != "" && strings.HasPrefix(p, home+"/") {
			p = "~/" + strings.TrimPrefix(p, home+"/")
		}
		names = append(names, p)
	}
	return "\n\n## Blocked folders (enforced)\n" +
		"Your commands run in a macOS sandbox that blocks these folders: " + strings.Join(names, ", ") + ".\n" +
		"- Reading or listing them fails with \"Operation not permitted\". That is the guard, not a bug. " +
		"Do not retry with sudo, another tool or a copy. If the person needs a file there, tell them; " +
		"they can add the folder to `security.fs_guard.allow` in the agent's config.\n" +
		"- Look for files only under the workspace, the project folder, ~/goterm-projects and ~/goterm-shared. " +
		"Never run `find ~`, `find /` or `grep -r ~`: they walk into every blocked folder.\n" +
		"- Nested sandboxes are refused: start headless Chrome/Chromium with `--no-sandbox` " +
		"(Playwright already does), and use `swift build --disable-sandbox`.\n"
}
