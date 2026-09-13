package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/ngocp/goterm-control/internal/chat"
	"github.com/ngocp/goterm-control/internal/models"
)

// Changing an agent's backend from the dashboard.
//
// The write is line-surgery rather than a YAML round-trip, and that is the
// whole design. These files are hand-maintained and have diverged from the
// template — agent names, auto_claim, edited system prompts, comments that
// explain why — and a marshal-and-rewrite would silently reformat all of it to
// change two scalars. Editing the two lines leaves every other byte identical,
// which is checkable: the caller can diff.
//
// What is chosen is the MODEL, not the provider. The model's API is what
// selects the backend (chat.Resolve), so picking a model and deriving the
// provider makes the two agree by construction instead of asking a person to
// keep them in step.
var (
	providerLine = regexp.MustCompile(`(?m)^provider:[ \t]*.*$`)
	defaultLine  = regexp.MustCompile(`(?m)^([ \t]+)default:[ \t]*.*$`)
)

// SetModel points a config file at modelID and at whichever provider that
// model's API implies, then proves the result loads and validates before it
// replaces the original.
//
// Writing a config the binary will refuse at startup is how you lose an agent
// until somebody reads a log, so the check happens here rather than at the
// next restart.
func SetModel(path, modelID string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	current, err := Load(path)
	if err != nil {
		return fmt.Errorf("load %s: %w", path, err)
	}

	m := current.Resolver().Lookup(modelID)
	if m == nil {
		return fmt.Errorf("unknown model %q — add it under models.custom first", modelID)
	}
	provider := providerFor(m.API)
	if provider == "" {
		return fmt.Errorf("model %q speaks %q, which has no provider key", modelID, m.API)
	}

	out := string(raw)
	if n := len(providerLine.FindAllString(out, -1)); n != 1 {
		return fmt.Errorf("expected exactly one top-level provider: line in %s, found %d", path, n)
	}
	out = providerLine.ReplaceAllString(out, fmt.Sprintf("provider: %q", provider))

	// models.default is the only indented `default:` in these files; anything
	// else is a shape this cannot safely edit and must not guess at.
	if n := len(defaultLine.FindAllString(out, -1)); n != 1 {
		return fmt.Errorf("expected exactly one models.default line in %s, found %d", path, n)
	}
	out = defaultLine.ReplaceAllString(out, fmt.Sprintf("${1}default: %q", modelID))

	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.yaml")
	if err != nil {
		return fmt.Errorf("temp file: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(out); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp: %w", err)
	}

	// The candidate has to survive exactly what startup does to it.
	candidate, err := Load(tmp.Name())
	if err != nil {
		return fmt.Errorf("the edited config does not load: %w", err)
	}
	if err := candidate.Validate(); err != nil {
		return fmt.Errorf("the edited config would be refused at startup: %w", err)
	}
	if candidate.Provider != provider || candidate.Models.Default != modelID {
		return fmt.Errorf("the edit did not take: provider=%q model=%q", candidate.Provider, candidate.Models.Default)
	}

	// Keep the original's mode; CreateTemp makes 0600 and these files are read
	// by the agent's own shell too.
	if info, err := os.Stat(path); err == nil {
		_ = os.Chmod(tmp.Name(), info.Mode())
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}

// Resolver builds this config's model resolver — builtins plus whatever the
// agent added under models.custom.
func (c *Config) Resolver() *models.Resolver {
	want := c.Models.Default
	if want == "" {
		want = c.Claude.Model
	}
	return models.NewResolver(want, c.Models.Custom)
}

// KnownModels lists what this agent can be switched to: every model it can
// resolve whose API has a backend registered.
func (c *Config) KnownModels() []ModelChoice {
	var out []ModelChoice
	for _, m := range c.Resolver().List() {
		if !chat.Supports(m.API) {
			continue
		}
		out = append(out, ModelChoice{
			ID:       m.ID,
			Name:     m.Name,
			API:      string(m.API),
			Provider: providerFor(m.API),
			Current:  m.ID == c.Models.Default,
		})
	}
	return out
}

// ModelChoice is one option on the settings screen.
type ModelChoice struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	API      string `json:"api"`
	Provider string `json:"provider"`
	Current  bool   `json:"current"`
}
