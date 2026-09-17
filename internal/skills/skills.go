// Package skills is an agent's own toolkit: folders of instructions it can
// read when a job calls for them.
//
// The shape is AgentSkills/openclaw's — one directory per skill, a SKILL.md
// with YAML frontmatter naming it and saying when to use it, and the body left
// for the instructions themselves.
//
// Two decisions are worth stating up front, because both are constraints from
// this codebase rather than taste.
//
// It is NOT the CLI's own skill mechanism. internal/claude passes
// --disable-slash-commands on purpose: the CLI otherwise loads whatever is in
// ~/.claude/skills, which is the operator's interactive toolkit, and some of it
// competes for the agent's work — a headless-browser skill advertising "open in
// browser" wins those requests over `bomclaw browser`, so the agent quietly
// drives a logged-out browser and reports the site as unreachable. That flag
// stays. It is also claude's alone, and this machine runs claude, codex and
// opencode; a mechanism that works on one backend is a mechanism two thirds of
// the agents do not have.
//
// So skills reach the model as a short INDEX in the system prompt — name,
// description, path — and the agent opens the SKILL.md itself when a job looks
// relevant. That is openclaw's actual mechanism too: the description is the
// routing signal, the body is read on demand. It costs a few hundred tokens for
// a toolkit of any size, and it works identically on all three backends.
//
// Each agent keeps its OWN copy under its own workspace. Divergence is the
// point, not the price: the plan is for an agent to revise its own skills from
// experience, and a shared library would mean one agent's lesson silently
// rewriting how its peers behave in situations they have never been in.
package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Dir is the folder inside an agent's workspace that holds its skills.
const Dir = "skills"

// File is the one file that makes a directory a skill.
const File = "SKILL.md"

// MaxDescriptionRunes bounds what one skill contributes to every prompt. The
// description is a routing signal — "use this when…" — not the instructions,
// and a paragraph here is paid on every single turn.
const MaxDescriptionRunes = 400

// MaxIndexRunes bounds the whole block. Past this the agent is told to list the
// folder itself, which is better than silently dropping the tail: a skill that
// is invisible for reasons nobody can see is worse than one that takes a
// second step to find.
const MaxIndexRunes = 4000

// Skill is one folder's worth of instructions.
type Skill struct {
	// Name is how the agent refers to it. It comes from the frontmatter, and
	// falls back to the directory name — a skill with a malformed header is
	// still better offered than hidden.
	Name string `json:"name"`
	// Description says WHEN to use it. This is the whole routing decision: it
	// is what the model sees, and the body is what it reads afterwards.
	Description string `json:"description"`
	// Path is the SKILL.md the agent opens. Absolute, because the working
	// directory of a turn is not always the workspace — a task filed under a
	// project runs in the project's folder.
	Path string `json:"path"`
	// Dir is the skill's folder, for anything it ships alongside SKILL.md.
	Dir string `json:"dir"`
}

// frontmatter is the header of a SKILL.md. Only two fields are read: a skill
// index exists to answer "what is this and when do I want it", and everything
// else a skill wants to say belongs in the body, where it costs nothing until
// the agent decides to look.
type frontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

// Load reads every skill in an agent's workspace, sorted by name.
//
// A missing folder is not an error: most agents will have no skills, and a
// gateway that refused to start over it would be broken by the normal case.
func Load(workspace string) ([]Skill, error) {
	if strings.TrimSpace(workspace) == "" {
		return nil, nil
	}
	root := filepath.Join(workspace, Dir)
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("skills: read %s: %w", root, err)
	}

	var out []Skill
	for _, e := range entries {
		// A file sitting in skills/ is not a skill, and neither is a dotfile.
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		dir := filepath.Join(root, e.Name())
		path := filepath.Join(dir, File)
		data, err := os.ReadFile(path)
		if err != nil {
			continue // a folder with no SKILL.md is just a folder
		}
		s := parse(string(data))
		if s.Name == "" {
			s.Name = e.Name()
		}
		s.Path, s.Dir = path, dir
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// parse pulls the frontmatter out of a SKILL.md.
//
// A file whose header is missing or malformed still yields a skill — with no
// description, which reads as "nobody said when to use this" rather than
// vanishing. A skill that is invisible for reasons nobody can see is the worst
// outcome here: the agent cannot use it and cannot be told why.
func parse(body string) Skill {
	body = strings.TrimPrefix(body, "\ufeff") // a BOM from an editor, not part of the header
	if !strings.HasPrefix(body, "---") {
		return Skill{}
	}
	rest := strings.TrimPrefix(body, "---")
	rest = strings.TrimLeft(rest, "\r\n")
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return Skill{}
	}
	var fm frontmatter
	if err := yaml.Unmarshal([]byte(rest[:end]), &fm); err != nil {
		return Skill{}
	}
	return Skill{
		Name:        strings.TrimSpace(fm.Name),
		Description: cut(strings.TrimSpace(fm.Description), MaxDescriptionRunes),
	}
}

// Index is the block that goes into a system prompt: what this agent can do,
// and where to read how. Empty when the agent has no skills, so a prompt never
// carries an empty heading.
func Index(list []Skill) string {
	if len(list) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n## Your skills\n\n")
	b.WriteString("Folders of instructions you have. The line below each name says when it applies — " +
		"when one does, READ its file before starting, because it holds the details this summary does not.\n\n")
	for _, s := range list {
		fmt.Fprintf(&b, "- **%s** — %s\n  `%s`\n", s.Name, orNone(s.Description), s.Path)
	}
	out := b.String()
	if r := []rune(out); len(r) > MaxIndexRunes {
		out = string(r[:MaxIndexRunes]) + fmt.Sprintf(
			"\n\n[skill list truncated — the rest are folders under %s]\n",
			filepath.Dir(list[0].Dir))
	}
	return out
}

func orNone(d string) string {
	if d == "" {
		return "(no description — open it to find out what it does)"
	}
	return d
}

func cut(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return strings.TrimSpace(string(r[:max])) + "…"
}
