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
// These teach this machine's own tools — the `bomclaw` commands the system
// prompt never mentions, each of which has a failure in this repo's history
// behind it: an agent reporting a site unreachable because it fetched a
// logged-out page, two agents settling a design in a DM nobody else could
// read, and a finished 3D viewer handed over as a URL in a paragraph while its
// task's output panel stayed empty.
//
// Each ships as a PLUGIN — a SKILL.md plus a plugin.yaml naming what it
// provides — because everything in an agent's toolkit is one now (see
// internal/plugins). They are seeded as COPIES into each agent's workspace,
// not read from here: the point of per-agent copies is that an agent revises
// its own from experience, and a default read straight out of the binary
// could never be revised.

//go:embed defaults/*/SKILL.md defaults/*/plugin.yaml
var defaultFS embed.FS

// PluginsDir is where an agent's plugins live, inside its workspace. It is
// plugins.Dir; spelled out here because this package is a leaf the plugins
// package builds on, and a test there pins the two together.
const PluginsDir = "plugins"

// ManifestFile is plugins.File, for the same reason.
const ManifestFile = "plugin.yaml"

// EnsureDefaults seeds any missing bundled plugin into an agent's workspace
// and returns the names it wrote.
//
// It never overwrites. Once an agent has a copy, that copy is the agent's —
// re-seeding on every start would destroy exactly the revisions this design
// exists to allow, and would do it silently, on a schedule nobody chose.
func EnsureDefaults(workspace string) ([]string, error) {
	if workspace == "" {
		return nil, nil
	}
	var written []string
	for _, name := range DefaultNames() {
		dest := filepath.Join(workspace, PluginsDir, name, File)
		if _, err := os.Stat(dest); err == nil {
			continue // the agent has it; it may have changed it
		} else if !os.IsNotExist(err) {
			return written, fmt.Errorf("skills: stat %s: %w", dest, err)
		}
		if err := InstallDefault(workspace, name); err != nil {
			return written, err
		}
		written = append(written, name)
	}
	return written, nil
}

// InstallDefault writes one bundled plugin into a workspace, replacing the
// SKILL.md and manifest of the same name if they are there — how the hub puts
// back a default someone removed, or resets an edited one on request.
func InstallDefault(workspace, name string) error {
	body, err := Default(name)
	if err != nil {
		return err
	}
	manifest, err := DefaultManifest(name)
	if err != nil {
		return err
	}
	dir := filepath.Join(workspace, PluginsDir, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("skills: mkdir %s: %w", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, ManifestFile), manifest, 0o644); err != nil {
		return fmt.Errorf("skills: write %s: %w", name, err)
	}
	return os.WriteFile(filepath.Join(dir, File), body, 0o644)
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
	body, err := defaultFS.ReadFile(path(name, File))
	if err != nil {
		return nil, fmt.Errorf("skills: no bundled skill %q", name)
	}
	return body, nil
}

// DefaultManifest returns one bundled plugin's plugin.yaml, which migration
// uses to give an agent's existing copy of a default its real manifest.
func DefaultManifest(name string) ([]byte, error) {
	if !validName(name) {
		return nil, fmt.Errorf("skills: %q is not a skill name", name)
	}
	body, err := defaultFS.ReadFile(path(name, ManifestFile))
	if err != nil {
		return nil, fmt.Errorf("skills: no bundled plugin %q", name)
	}
	return body, nil
}

// path is embed's own separator-independent form; filepath.Join would produce
// backslashes on Windows and find nothing.
func path(name, file string) string { return "defaults/" + name + "/" + file }
