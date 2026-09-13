package chat

import (
	"fmt"
	"sort"
	"sync"

	"github.com/ngocp/goterm-control/internal/agent"

	"github.com/ngocp/goterm-control/internal/credentials"
	"github.com/ngocp/goterm-control/internal/models"
	"github.com/ngocp/goterm-control/internal/tools"
)

// Which CLI backs an agent.
//
// This used to be an if/else on one agent-wide config key, with an else branch
// that turned anything unrecognised into claude without saying so. That shape
// has two costs. A third backend means editing internal/bot, which is the
// package that should know least about any particular CLI. And the silent else
// means loosening the config check without also editing that branch gives you
// an agent that reports one provider and runs another.
//
// The key is the model's API rather than a provider name, because the model
// catalogue already records which protocol each model speaks and config
// already cross-checks the two (config.Validate). Choosing a model is
// something people do every day; choosing a backend is not. Keying on the API
// makes them one decision instead of two that have to agree.
type Factory func(Deps) Client

// Deps is what every backend needs to build one. Plain values rather than
// *config.Config on purpose: config imports this package to validate against
// the registry, and the reverse import would close the loop.
type Deps struct {
	SystemPrompt string
	Workspace    string
	Executor     *tools.Executor   // claude runs tools in-process; others may ignore it
	Pool         *credentials.Pool // nil means the ambient credentials
}

var (
	registryMu sync.RWMutex
	registry   = map[models.ModelAPI]Factory{}
)

// Register adds a backend. Called from a provider package's init, so adding one
// is a new file and one line rather than a new branch in internal/bot.
// Registering the same API twice is a programming error, not a runtime one.
func Register(api models.ModelAPI, f Factory) {
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, taken := registry[api]; taken {
		panic(fmt.Sprintf("chat: two backends registered for %q", api))
	}
	registry[api] = f
}

// Resolve builds the backend for an API, or says which one it could not find.
// It never falls back: an agent quietly running a different CLI than the one
// its config names is worse than an agent that does not start.
func Resolve(api models.ModelAPI, deps Deps) (Client, error) {
	registryMu.RLock()
	f, ok := registry[api]
	registryMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("chat: no backend for model api %q (have: %v) — "+
			"the provider package may not be imported", api, Registered())
	}
	return f(deps), nil
}

// Supports reports whether a backend exists for an API. config.Validate uses it
// instead of a hardcoded list of two.
func Supports(api models.ModelAPI) bool {
	registryMu.RLock()
	defer registryMu.RUnlock()
	_, ok := registry[api]
	return ok
}

// Registered lists the APIs with a backend, sorted so error messages and the
// settings screen do not reorder themselves between calls.
func Registered() []models.ModelAPI {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make([]models.ModelAPI, 0, len(registry))
	for api := range registry {
		out = append(out, api)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// The second seam: one-shot text.
//
// Naming a session is not a conversation — no stream to a sink, no tool loop,
// no session to resume — so it needs a different interface, and giving it one
// registry of its own is cheaper than bending chat.Client around a single
// string. What it must NOT have is the shape this package just removed: a
// switch that ends in "and anything else is claude". An agent running a
// backend that has no titler then shells out to a CLI it may not have
// installed, and on a machine where it does, spends that provider's quota on
// titles for an agent deliberately moved off it.
//
// Absent is a legitimate answer here, in a way it is not for chat: a session
// with no title is a small loss, and titler.Title already no-ops on a nil
// provider. Silence beats a failing call every turn.
type TitlerFactory func(Deps) agent.ModelProvider

var titlers = map[models.ModelAPI]TitlerFactory{}

// RegisterTitler adds the one-shot text backend for an API. Optional: a
// provider with no titler simply has none.
func RegisterTitler(api models.ModelAPI, f TitlerFactory) {
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, taken := titlers[api]; taken {
		panic(fmt.Sprintf("chat: two titlers registered for %q", api))
	}
	titlers[api] = f
}

// ResolveTitler returns the one-shot backend for an API, or nil when that
// backend has none. nil is a supported answer: the caller skips titling.
func ResolveTitler(api models.ModelAPI, deps Deps) agent.ModelProvider {
	registryMu.RLock()
	f, ok := titlers[api]
	registryMu.RUnlock()
	if !ok {
		return nil
	}
	return f(deps)
}
