package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Changing an agent's toolkit while it runs.
//
// The index is re-read every turn, so everything here takes effect on the
// agent's next turn with no restart. That is the whole point: a toolkit you can
// only change by restarting the gateway is not a toolkit anybody edits.

// MaxSkillBytes bounds one SKILL.md. The body is read on demand rather than
// injected, so this is generous — but a skill is instructions, and something
// past this is a document that wants to be a file the instructions point at.
const MaxSkillBytes = 256 << 10

// nameOK is what a skill may be called. A skill name becomes a directory name,
// so anything that could climb out of the skills folder or collide with a
// path separator is refused rather than sanitised: silently renaming what
// someone asked for produces a skill they cannot find again.
var nameOK = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

func validName(name string) bool { return nameOK.MatchString(name) }

// ValidateName reports why a name is unusable, for a message a person can act
// on rather than a silent refusal.
func ValidateName(name string) error {
	if name == "" {
		return fmt.Errorf("skills: a skill needs a name")
	}
	if !validName(name) {
		return fmt.Errorf("skills: %q is not usable as a folder name — "+
			"lowercase letters, digits, - and _, starting with a letter or digit", name)
	}
	return nil
}

// dirFor is the one place a skill name becomes a path, and the only thing
// standing between a name and the rest of the disk.
func dirFor(workspace, name string) (string, error) {
	if err := ValidateName(name); err != nil {
		return "", err
	}
	if strings.TrimSpace(workspace) == "" {
		return "", fmt.Errorf("skills: this agent has no workspace")
	}
	return filepath.Join(workspace, Dir, name), nil
}

// Install writes a skill into an agent's workspace, replacing what is there.
//
// Replacing rather than refusing, because the hub's two real uses are "give
// this agent the skill" and "give this agent the version that peer improved" —
// and both are the same write. What protects an edited copy is that nothing
// does this on its own: EnsureDefaults never overwrites, and this only runs
// when a person or an agent asked for it by name.
func Install(workspace, name string, body []byte) error {
	if len(body) == 0 {
		return fmt.Errorf("skills: %q has no content", name)
	}
	if len(body) > MaxSkillBytes {
		return fmt.Errorf("skills: %q is %d bytes (max %d) — instructions this long "+
			"want to be a file the skill points at", name, len(body), MaxSkillBytes)
	}
	dir, err := dirFor(workspace, name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("skills: mkdir %s: %w", dir, err)
	}
	// Temp + rename: the agent may be reading this folder right now, and a
	// half-written SKILL.md is instructions it would act on.
	dest := filepath.Join(dir, File)
	tmp, err := os.CreateTemp(dir, ".install-*")
	if err != nil {
		return fmt.Errorf("skills: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return fmt.Errorf("skills: write %s: %w", dest, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("skills: %w", err)
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return fmt.Errorf("skills: %w", err)
	}
	if err := os.Rename(tmp.Name(), dest); err != nil {
		return fmt.Errorf("skills: install %s: %w", dest, err)
	}
	return nil
}

// Remove deletes one agent's copy of a skill. Other agents keep theirs.
//
// A bundled default can be removed like any other: it comes back with
// `Default(name)` and one Install, so this loses nothing that cannot be
// restored. An agent's OWN revisions to it are lost, which is why the hub
// should say so before doing it.
func Remove(workspace, name string) error {
	dir, err := dirFor(workspace, name)
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(dir, File)); os.IsNotExist(err) {
		return fmt.Errorf("skills: this agent does not have %q", name)
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("skills: remove %s: %w", dir, err)
	}
	return nil
}

// Read returns one agent's copy of a skill, for showing it and for copying it
// somewhere else.
func Read(workspace, name string) ([]byte, error) {
	dir, err := dirFor(workspace, name)
	if err != nil {
		return nil, err
	}
	body, err := os.ReadFile(filepath.Join(dir, File))
	if err != nil {
		return nil, fmt.Errorf("skills: %q: %w", name, err)
	}
	return body, nil
}

// Copy takes one agent's version of a skill and gives it to another.
//
// This is the operation that makes per-agent copies worth having. Without it
// each agent improves alone and a lesson learned by one dies with it — three
// loops running in parallel instead of one that accumulates. With it, an
// improvement spreads because somebody looked at two versions and decided.
func Copy(fromWorkspace, toWorkspace, name string) error {
	if fromWorkspace == toWorkspace {
		return fmt.Errorf("skills: that is the same agent")
	}
	body, err := Read(fromWorkspace, name)
	if err != nil {
		return err
	}
	return Install(toWorkspace, name, body)
}
