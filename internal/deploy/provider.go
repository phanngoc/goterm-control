package deploy

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
)

// Provider is where a PROD environment lives. bomclaw.json names one in
// deploy.target; everything above this package — the dashboard's panel, `bomclaw
// env`, the agents' skill — talks to the interface, so adding a host is adding
// an implementation, not a feature.
//
// Cloudflare is for sites (a Worker serving built files, D1 for data); Fly.io
// is for apps that run a server process (Machines from a Docker image), and
// for a static site where the account is on Fly. Managed Postgres on Fly and
// Supabase are the database engines to come.
type Provider interface {
	// Name is the deploy.target value that selects it.
	Name() string
	// Title is how a screen names it ("Cloudflare", "Fly.io").
	Title() string
	// Validate checks the parts of a spec only this provider understands
	// (which database engines it can host, which fields it needs).
	Validate(s *Spec) error
	// Connected reports whether credentials are in place, and if not, what
	// to set — in words the owner can act on.
	Connected() (bool, string)
	// Status asks the provider what is live. Never changes anything.
	Status(ctx context.Context, r Resolved) ProdStatus
	// Deploy publishes, logging each step.
	Deploy(ctx context.Context, r Resolved, opt Options, log io.Writer) (*Result, error)
}

// Factory builds a provider with its credentials read now, so a token added to
// .env is seen after a restart without anything else changing.
type Factory func() Provider

var (
	registry = map[string]Factory{}
	// planned are targets that are known and not built yet. Naming them gives
	// an agent that tries one a useful answer instead of "unknown target".
	planned = map[string]string{
		"vercel": "Vercel",
	}
)

// Register adds a provider. Called from each implementation's init.
func Register(name string, f Factory) { registry[name] = f }

// For returns the provider a target names.
func For(target string) (Provider, error) {
	if target == "" {
		target = DefaultTarget
	}
	if f, ok := registry[target]; ok {
		return f(), nil
	}
	if what, ok := planned[target]; ok {
		return nil, fmt.Errorf("deploy.target %q is planned, not built yet: %s. Available: %s", target, what, strings.Join(Available(), ", "))
	}
	return nil, fmt.Errorf("deploy.target %q is not a provider. Available: %s", target, strings.Join(Available(), ", "))
}

// Available lists the built providers.
func Available() []string {
	out := make([]string, 0, len(registry))
	for n := range registry {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Planned lists the providers that are known and not built yet.
func Planned() map[string]string {
	out := make(map[string]string, len(planned))
	for k, v := range planned {
		out[k] = v
	}
	return out
}

// DefaultTarget is the provider a spec without "target" uses.
const DefaultTarget = "cloudflare"
