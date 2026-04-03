package models

import (
	"fmt"
	"strings"
	"sync"
)

// Resolver manages the model catalog and per-session model overrides.
type Resolver struct {
	mu       sync.RWMutex
	models   []Model
	byID     map[string]*Model // canonical ID → model
	byAlias  map[string]*Model // alias → model
	defaults string            // default model ID

	// Per-session model overrides (chatID → model ID).
	overrides   map[int64]string
	overridesMu sync.RWMutex
}

// NewResolver creates a resolver with builtin models + optional custom models.
// defaultModel is the model ID to use when no override is set.
func NewResolver(defaultModel string, custom []Model) *Resolver {
	r := &Resolver{
		models:    make([]Model, 0),
		byID:      make(map[string]*Model),
		byAlias:   make(map[string]*Model),
		defaults:  defaultModel,
		overrides: make(map[int64]string),
	}

	// Load builtins first, then custom (custom can override builtins).
	for _, m := range BuiltinModels() {
		r.addModel(m)
	}
	for _, m := range custom {
		r.addModel(m)
	}

	// If the default model doesn't exist in catalog, use the first model.
	if _, ok := r.byID[defaultModel]; !ok && len(r.models) > 0 {
		r.defaults = r.models[0].ID
	}

	return r
}

func (r *Resolver) addModel(m Model) {
	// Replace existing model with same ID
	existing := false
	for i, em := range r.models {
		if em.ID == m.ID {
			r.models[i] = m
			existing = true
			break
		}
	}
	if !existing {
		r.models = append(r.models, m)
	}

	stored := &r.models[len(r.models)-1]
	if existing {
		for i := range r.models {
			if r.models[i].ID == m.ID {
				stored = &r.models[i]
				break
			}
		}
	}

	r.byID[m.ID] = stored
	for _, alias := range m.Aliases {
		r.byAlias[strings.ToLower(alias)] = stored
	}
}

// Resolve returns the model for a given chatID.
// Checks: per-session override → default.
func (r *Resolver) Resolve(chatID int64) *Model {
	r.overridesMu.RLock()
	override, ok := r.overrides[chatID]
	r.overridesMu.RUnlock()

	if ok {
		if m := r.Lookup(override); m != nil {
			return m
		}
	}

	r.mu.RLock()
	defer r.mu.RUnlock()
	if m, ok := r.byID[r.defaults]; ok {
		return m
	}
	if len(r.models) > 0 {
		return &r.models[0]
	}
	return nil
}

// Lookup finds a model by ID or alias. Returns nil if not found.
func (r *Resolver) Lookup(idOrAlias string) *Model {
	r.mu.RLock()
	defer r.mu.RUnlock()

	key := strings.ToLower(idOrAlias)
	if m, ok := r.byID[key]; ok {
		return m
	}
	if m, ok := r.byAlias[key]; ok {
		return m
	}
	return nil
}

// SetOverride sets a per-session model override. Returns the resolved model or error.
func (r *Resolver) SetOverride(chatID int64, idOrAlias string) (*Model, error) {
	m := r.Lookup(idOrAlias)
	if m == nil {
		return nil, fmt.Errorf("unknown model: %q — use /models to list available models", idOrAlias)
	}

	r.overridesMu.Lock()
	r.overrides[chatID] = m.ID
	r.overridesMu.Unlock()

	return m, nil
}

// ClearOverride removes the per-session model override.
func (r *Resolver) ClearOverride(chatID int64) {
	r.overridesMu.Lock()
	delete(r.overrides, chatID)
	r.overridesMu.Unlock()
}

// List returns all available models.
func (r *Resolver) List() []Model {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Model, len(r.models))
	copy(out, r.models)
	return out
}

// Default returns the default model ID.
func (r *Resolver) Default() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.defaults
}

// FormatModelInfo returns a human-readable string for a model.
func FormatModelInfo(m *Model, isActive bool) string {
	marker := "  "
	if isActive {
		marker = "▸ "
	}

	aliases := ""
	if len(m.Aliases) > 0 {
		aliases = fmt.Sprintf(" (%s)", strings.Join(m.Aliases, ", "))
	}

	reasoning := ""
	if m.Reasoning {
		reasoning = " 🧠"
	}

	return fmt.Sprintf("%s`%s`%s%s\n   %s · %dk ctx · $%.1f/$%.1f per 1M tokens",
		marker, m.ID, aliases, reasoning,
		m.Name, m.ContextWindow/1000,
		m.Cost.Input, m.Cost.Output,
	)
}
