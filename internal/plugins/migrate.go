package plugins

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/ngocp/goterm-control/internal/skills"
)

// Skills become plugins.
//
// A bare skill folder already counted as a plugin with one contribution; this
// makes it one on disk, so there is one place an agent's toolkit lives and
// one shape it has. Nothing is rewritten: the folder — SKILL.md, whatever
// ships beside it, and every edit the agent made — is moved whole, and a
// plugin.yaml is added next to it.

func init() {
	// Everything that looks a skill up by name — the hub, the peer roster,
	// skills.Read/Install/Remove — must find it inside a plugin now. This is
	// the provider for that lookup; see skills.SetFinder.
	skills.SetFinder(func(workspace string) ([]skills.Skill, error) {
		if strings.TrimSpace(workspace) == "" {
			return nil, nil
		}
		k := Load(workspace, "")
		var all []skills.Skill
		for _, p := range k.Plugins {
			if p.Status != Broken {
				all = skills.Merge(all, p.SkillList)
			}
		}
		return all, nil
	})
}

// Migrated is one skill folder MigrateSkills moved.
type Migrated struct {
	Name string `json:"name"`
	From string `json:"from"`
	To   string `json:"to"`
	// Bundled says the plugin.yaml written is the bundled one (with what it
	// provides and injects), not one generated from the SKILL.md header.
	Bundled bool `json:"bundled"`
}

// MigrateSkills moves every skill folder in the agent's skills/ into
// plugins/<name>/ and gives it a manifest. It is idempotent and safe to run
// on every start: a skill already moved has left a symlink behind, which no
// loader treats as a skill.
//
// Only the agent's own skills/ is touched. A repository's .agents/skills
// belongs to the repository — moving files in somebody's source tree because
// an agent happened to work there is not this function's business.
//
// A skill whose name is already taken by a plugin stays where it is and is
// reported in skipped: two things of one name is a conflict for a person to
// settle, and guessing which one wins would discard the other.
func MigrateSkills(workspace string) (moved []Migrated, skipped []string, err error) {
	if strings.TrimSpace(workspace) == "" {
		return nil, nil, nil
	}
	root := filepath.Join(workspace, skills.Dir)
	list, err := skills.LoadDir(root)
	if err != nil {
		return nil, nil, err
	}
	for _, s := range list {
		if s.Dir == root {
			continue // a SKILL.md sitting in skills/ itself is not a folder to move
		}
		name := s.Name
		if !ValidName(name) {
			name = filepath.Base(s.Dir) // a header name that cannot be a folder; the folder's can
		}
		if !ValidName(name) {
			skipped = append(skipped, fmt.Sprintf("%s: %q cannot be a plugin name", s.Dir, s.Name))
			continue
		}
		dest := filepath.Join(workspace, Dir, name)
		if _, err := os.Lstat(dest); err == nil {
			skipped = append(skipped, fmt.Sprintf("%s: a plugin named %q already exists at %s", s.Dir, name, dest))
			continue
		}
		m, err := migrateOne(s, name, dest)
		if err != nil {
			return moved, skipped, err
		}
		moved = append(moved, m)
	}
	return moved, skipped, nil
}

func migrateOne(s skills.Skill, name, dest string) (Migrated, error) {
	m := Migrated{Name: name, From: s.Dir, To: dest}
	manifest, err := skills.DefaultManifest(name)
	if err == nil {
		m.Bundled = true
	} else {
		manifest, err = generatedManifest(name, s)
		if err != nil {
			return m, err
		}
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return m, err
	}
	// A plugin.yaml the skill already carried (somebody got there first) is
	// respected, not overwritten.
	has := fileExists(filepath.Join(s.Dir, File))
	if err := os.Rename(s.Dir, dest); err != nil {
		return m, fmt.Errorf("plugins: move %s: %w", s.Dir, err)
	}
	if !has {
		if err := os.WriteFile(filepath.Join(dest, File), manifest, 0o644); err != nil {
			return m, err
		}
	}
	// The old path keeps working. A codex or opencode thread was given the
	// skill index once, at its start, and still holds the old path — a turn
	// told to read it must not find nothing. Best effort: where symlinks are
	// not allowed (Windows without the privilege), the index already points
	// at the new place for every new thread.
	if rel, err := filepath.Rel(filepath.Dir(s.Dir), dest); err == nil {
		_ = os.Symlink(rel, s.Dir)
	}
	return m, nil
}

// generatedManifest describes a skill as a plugin: its name, and its
// description, which is the routing line both listings show.
func generatedManifest(name string, s skills.Skill) ([]byte, error) {
	body, err := yaml.Marshal(map[string]string{
		"name":        name,
		"version":     "0.1.0",
		"description": s.Description,
	})
	if err != nil {
		return nil, err
	}
	header := "# Migrated from skills/ — the SKILL.md beside this is the plugin's skill.\n"
	if s.Category != "" {
		header += "# Was filed under category: " + s.Category + "\n"
	}
	return append([]byte(header), body...), nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
