package skills

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// The toolkit every agent starts with.
//
// These teach this machine's own tools — the three `bomclaw` commands the
// system prompt never mentions, each of which has a failure in this repo's
// history behind it: an agent reporting a site unreachable because it fetched
// a logged-out page, two agents settling a design in a DM nobody else could
// read, and a finished 3D viewer handed over as a URL in a paragraph while its
// task's output panel stayed empty.
//
// They are seeded as COPIES into each agent's workspace, not read from here.
// The point of per-agent copies is that an agent revises its own from
// experience; a default read straight out of the binary could never be
// revised, and one shared across agents would let one agent's lesson rewrite
// how its peers behave in situations they have never been in.

//go:embed defaults/*/SKILL.md
var defaultFS embed.FS

// EnsureDefaults copies any missing default skill into an agent's workspace and
// returns the names it wrote.
//
// It never overwrites. Once an agent has a copy, that copy is the agent's —
// re-seeding on every start would destroy exactly the revisions this design
// exists to allow, and would do it silently, on a schedule nobody chose.
func EnsureDefaults(workspace string) ([]string, error) {
	if workspace == "" {
		return nil, nil
	}
	entries, err := fs.ReadDir(defaultFS, "defaults")
	if err != nil {
		return nil, fmt.Errorf("skills: read defaults: %w", err)
	}
	var written []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		dest := filepath.Join(workspace, Dir, name, File)
		if _, err := os.Stat(dest); err == nil {
			continue // the agent has it; it may have changed it
		} else if !os.IsNotExist(err) {
			return written, fmt.Errorf("skills: stat %s: %w", dest, err)
		}
		body, err := defaultFS.ReadFile(path(name))
		if err != nil {
			return written, fmt.Errorf("skills: read default %s: %w", name, err)
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return written, fmt.Errorf("skills: mkdir %s: %w", filepath.Dir(dest), err)
		}
		if err := os.WriteFile(dest, body, 0o644); err != nil {
			return written, fmt.Errorf("skills: write %s: %w", dest, err)
		}
		written = append(written, name)
	}
	return written, nil
}

// DefaultNames lists what EnsureDefaults would seed, for the hub's "install
// this one back" and for telling a person what they deleted.
func DefaultNames() []string {
	entries, err := fs.ReadDir(defaultFS, "defaults")
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out
}

// Default returns one bundled skill's text, for re-installing a deleted one or
// for showing what the original said beside an agent's edited copy.
func Default(name string) ([]byte, error) {
	if !validName(name) {
		return nil, fmt.Errorf("skills: %q is not a skill name", name)
	}
	body, err := defaultFS.ReadFile(path(name))
	if err != nil {
		return nil, fmt.Errorf("skills: no bundled skill %q", name)
	}
	return body, nil
}

// path is embed's own separator-independent form; filepath.Join would produce
// backslashes on Windows and find nothing.
func path(name string) string { return "defaults/" + name + "/" + File }
