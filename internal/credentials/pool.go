// Package credentials manages the pool of accounts an agent runs its sessions
// under, for both CLI backends.
//
// Both CLIs keep their credentials AND their session store under one
// directory — CLAUDE_CONFIG_DIR for claude, CODEX_HOME for codex — which is
// the fact the whole design turns on: switching account switches the session
// store with it. A conversation therefore cannot migrate between accounts. An
// account is chosen once, when the session starts, and pinned to it for life;
// rotation happens across sessions, never inside one.
package credentials

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Provider keys, matching internal/config.
const (
	ProviderClaude = "claude"
	ProviderCodex  = "codex"
)

// Account is one credential a session can run under.
type Account struct {
	Name     string
	Provider string

	// ConfigDir is the CLI's config/session directory: CLAUDE_CONFIG_DIR for
	// claude, CODEX_HOME for codex. Each account needs its own, logged in
	// separately, or they share one identity and rotating achieves nothing.
	ConfigDir string

	// APIKey (claude only) runs the CLI on a plain API key instead of the
	// OAuth subscription. APIKeyEnv reads it from the environment instead, so
	// the key itself need not sit in config.yaml.
	APIKey    string
	APIKeyEnv string
}

// resolvedKey returns the API key, from the env var when one is named.
func (a Account) resolvedKey() string {
	if a.APIKeyEnv != "" {
		if v := strings.TrimSpace(os.Getenv(a.APIKeyEnv)); v != "" {
			return v
		}
	}
	return strings.TrimSpace(a.APIKey)
}

// Env returns the environment overrides for running under this account:
// values to set, and names to unset.
func (a Account) Env() (set map[string]string, unset []string) {
	set = map[string]string{}
	// The zero account means "leave the process environment exactly as it is".
	// Anything else here would change behaviour for every install that has no
	// pool configured, which is all of them until someone opts in.
	if a.Zero() {
		return set, nil
	}
	switch a.Provider {
	case ProviderCodex:
		if a.ConfigDir != "" {
			set["CODEX_HOME"] = a.ConfigDir
		}
	default: // claude
		if a.ConfigDir != "" {
			set["CLAUDE_CONFIG_DIR"] = a.ConfigDir
		}
		if key := a.resolvedKey(); key != "" {
			set["ANTHROPIC_API_KEY"] = key
		} else {
			// No key for this account: the CLI must fall back to the OAuth
			// login in its config dir, so an ambient key must not leak in.
			unset = append(unset, "ANTHROPIC_API_KEY", "ANTHROPIC_API_KEY_OLD")
		}
	}
	return set, unset
}

// Zero reports whether this is the implicit "whatever the process already has"
// account, used when no pool is configured. It asks whether the account
// carries any credential information at all, not merely whether it is named:
// an account that selects a config dir still selects an identity, named or
// not, and treating it as ambient would quietly ignore it.
func (a Account) Zero() bool {
	return a.ConfigDir == "" && a.APIKey == "" && a.APIKeyEnv == ""
}

// Status is one account's health, for `bomclaw accounts` and the dashboard.
type Status struct {
	Name        string `json:"name"`
	Provider    string `json:"provider"`
	Sessions    int    `json:"sessions"`                // sessions currently pinned here
	LastUsed    string `json:"last_used,omitempty"`     // RFC3339
	CoolingHint string `json:"cooling_until,omitempty"` // RFC3339 while rate-limited
	LastError   string `json:"last_error,omitempty"`
}

type accountState struct {
	acct      Account
	lastUsed  time.Time
	sessions  int
	coolUntil time.Time
	lastErr   string
}

// Pool holds the accounts for ONE provider — an agent runs one backend, and
// mixing claude and codex credentials in one rotation would hand a session a
// login its CLI cannot read.
type Pool struct {
	provider string
	cooldown time.Duration

	mu    sync.Mutex
	accts []*accountState
	next  int // round-robin cursor, used to break ties between equal accounts
}

// NewPool builds the pool for provider from accounts. Entries for other
// providers are ignored. An empty result is not an error: it means "run with
// the ambient credentials", which is what every existing install does.
func NewPool(provider string, accounts []Account, cooldown time.Duration) (*Pool, error) {
	if cooldown <= 0 {
		cooldown = 15 * time.Minute
	}
	p := &Pool{provider: provider, cooldown: cooldown}
	seen := map[string]bool{}
	for _, a := range accounts {
		if a.Provider == "" {
			a.Provider = provider
		}
		if a.Provider != provider {
			continue
		}
		if a.Name == "" {
			return nil, fmt.Errorf("credentials: every account needs a name")
		}
		if seen[a.Name] {
			return nil, fmt.Errorf("credentials: duplicate account name %q", a.Name)
		}
		seen[a.Name] = true
		a.ConfigDir = expandHome(a.ConfigDir)
		p.accts = append(p.accts, &accountState{acct: a})
	}
	return p, nil
}

// Empty reports whether no pool is configured for this provider.
func (p *Pool) Empty() bool {
	if p == nil {
		return true
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.accts) == 0
}

// Names lists the configured account names, in config order.
func (p *Pool) Names() []string {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, 0, len(p.accts))
	for _, s := range p.accts {
		out = append(out, s.acct.Name)
	}
	return out
}

// Pick returns the account a session should run under.
//
// A pinned name always wins, even while that account is cooling down: the
// session's history lives in its config directory and cannot be read from
// anywhere else, so moving it would silently start a new conversation. The
// caller surfaces the rate-limit error instead.
//
// With nothing pinned, the least recently used healthy account is chosen, so
// load spreads instead of piling onto whichever entry happens to be first.
func (p *Pool) Pick(pinned string) (Account, error) {
	if p == nil {
		return Account{}, nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	if len(p.accts) == 0 {
		return Account{}, nil // ambient credentials
	}

	if pinned != "" {
		for _, s := range p.accts {
			if s.acct.Name == pinned {
				s.lastUsed = time.Now()
				return s.acct, nil
			}
		}
		// The pin names an account this agent no longer has. Refusing is
		// safer than silently rehoming: the CLI would not find the session
		// either, and a fresh conversation under another identity is a
		// surprising thing to hand someone mid-thread.
		return Account{}, fmt.Errorf("credentials: session is pinned to account %q, which is not configured for provider %s", pinned, p.provider)
	}

	now := time.Now()
	var best *accountState
	for i := range p.accts {
		s := p.accts[(p.next+i)%len(p.accts)]
		if now.Before(s.coolUntil) {
			continue
		}
		if best == nil || s.lastUsed.Before(best.lastUsed) {
			best = s
		}
	}
	if best == nil {
		// Everything is cooling down. Hand back whichever recovers soonest and
		// let the turn try: a cooldown is a guess about someone else's rate
		// limiter, not a fact, and refusing outright would idle the agent.
		for _, s := range p.accts {
			if best == nil || s.coolUntil.Before(best.coolUntil) {
				best = s
			}
		}
	}
	best.lastUsed = now
	p.next = (p.next + 1) % len(p.accts)
	return best.acct, nil
}

// MarkStarted records that a session is now pinned to an account.
func (p *Pool) MarkStarted(name string) {
	p.each(name, func(s *accountState) { s.sessions++ })
}

// MarkFailure notes a turn failure. A rate-limit style error puts the account
// on cooldown so NEW sessions go elsewhere; sessions already pinned to it stay
// where their history is.
func (p *Pool) MarkFailure(name string, err error) {
	if err == nil {
		return
	}
	p.each(name, func(s *accountState) {
		s.lastErr = truncateErr(err.Error())
		if IsRateLimit(err) {
			s.coolUntil = time.Now().Add(p.cooldown)
		}
	})
}

// MarkSuccess clears a recorded error and lifts any cooldown: the account
// evidently works again.
func (p *Pool) MarkSuccess(name string) {
	p.each(name, func(s *accountState) {
		s.lastErr = ""
		s.coolUntil = time.Time{}
	})
}

func (p *Pool) each(name string, fn func(*accountState)) {
	if p == nil || name == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, s := range p.accts {
		if s.acct.Name == name {
			fn(s)
			return
		}
	}
}

// Status reports every account's health.
func (p *Pool) Status() []Status {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	out := make([]Status, 0, len(p.accts))
	for _, s := range p.accts {
		st := Status{
			Name:      s.acct.Name,
			Provider:  s.acct.Provider,
			Sessions:  s.sessions,
			LastError: s.lastErr,
		}
		if !s.lastUsed.IsZero() {
			st.LastUsed = s.lastUsed.Format(time.RFC3339)
		}
		if now.Before(s.coolUntil) {
			st.CoolingHint = s.coolUntil.Format(time.RFC3339)
		}
		out = append(out, st)
	}
	return out
}

// IsRateLimit reports whether err looks like the provider throttling this
// account, as opposed to a bug or a transient server fault. Only signals that
// are specific to an account count: "overloaded" is the provider's own
// capacity problem and would cool down a perfectly good account.
func IsRateLimit(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, sig := range []string{
		"rate limit",
		"rate_limit",
		"429",
		"quota",
		"usage limit",
		"too many requests",
		"insufficient_quota",
	} {
		if strings.Contains(msg, sig) {
			return true
		}
	}
	return false
}

func truncateErr(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if r := []rune(s); len(r) > 160 {
		return string(r[:159]) + "…"
	}
	return s
}

func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

// ApplyEnv returns environ with the account's overrides applied: values set or
// replaced, named variables removed.
func ApplyEnv(environ []string, a Account) []string {
	set, unset := a.Env()
	drop := map[string]bool{}
	for _, k := range unset {
		drop[k] = true
	}
	for k := range set {
		drop[k] = true // replaced below
	}
	out := make([]string, 0, len(environ)+len(set))
	for _, kv := range environ {
		key := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			key = kv[:i]
		}
		if !drop[key] {
			out = append(out, kv)
		}
	}
	for k, v := range set {
		out = append(out, k+"="+v)
	}
	return out
}
