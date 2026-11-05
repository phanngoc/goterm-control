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
	"log"
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

	// OAuthToken (claude only) is a long-lived token from `claude
	// setup-token`, handed to the CLI as CLAUDE_CODE_OAUTH_TOKEN.
	// OAuthTokenEnv reads it from the environment instead.
	//
	// This is the credential that makes per-TURN rotation possible. A
	// config-dir login carries its own session store, so a conversation
	// cannot leave it; a token is auth alone, and every token account that
	// shares one session store can answer any session in it. See TokenGroup.
	OAuthToken    string
	OAuthTokenEnv string

	// managed marks an account that came from the token store rather than
	// config.yaml; a store reload replaces exactly these.
	managed bool

	// NoRotate keeps this account in the pool and out of the rotation: usable
	// by name and never chosen for you, which is what a colleague's login
	// needs.
	//
	// Negative, so the zero value is the behaviour every pool had before this
	// existed. A positive Rotate bool would have silently emptied the rotation
	// of any account built by a caller that had not heard of the field — which
	// a test caught immediately, and production would have caught at 3am.
	NoRotate bool
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

// resolvedToken returns the setup-token, from the env var when one is named.
func (a Account) resolvedToken() string {
	if a.OAuthTokenEnv != "" {
		if v := strings.TrimSpace(os.Getenv(a.OAuthTokenEnv)); v != "" {
			return v
		}
	}
	return strings.TrimSpace(a.OAuthToken)
}

// IsToken reports whether this account authenticates with a token — a
// claude setup-token or a codex access token — whether or not the token is
// actually there right now.
func (a Account) IsToken() bool {
	return a.OAuthToken != "" || a.OAuthTokenEnv != ""
}

// envCredential reports whether the account's credential travels in the
// environment (a token or an API key) rather than living in a login.
func (a Account) envCredential() bool {
	return a.IsToken() || a.APIKey != "" || a.APIKeyEnv != ""
}

// usable reports whether the account has what it needs to run right now: an
// env credential must actually be set; a login is assumed to be.
func (a Account) usable() bool {
	return !a.envCredential() || a.credential() != ""
}

// rotates reports whether the account takes part in turn-by-turn rotation.
// Every account that shares a session store can answer any conversation in
// it — a token, a key, or the login that owns the default store — so the
// rotation is simply "accounts on one store", minus those opted out.
func (a Account) rotates() bool { return !a.NoRotate }

// credential is the secret this account hands the CLI, if any.
func (a Account) credential() string {
	if a.IsToken() {
		return a.resolvedToken()
	}
	return a.resolvedKey()
}

// store is the session store an account reads and writes: the config dir,
// or "" for the CLI's default. Sessions can move between accounts that share
// one — and only between those.
func (a Account) store() string {
	if a.ConfigDir == "" {
		return ""
	}
	if a.Provider == ProviderCodex {
		if home, err := os.UserHomeDir(); err == nil && filepath.Clean(expandHome(a.ConfigDir)) == filepath.Join(home, ".codex") {
			return ""
		}
		return filepath.Clean(a.ConfigDir)
	}
	if isDefaultClaudeDir(a.ConfigDir) {
		return ""
	}
	return filepath.Clean(a.ConfigDir)
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
		// codex reads its credential from the environment before auth.json
		// ("auth is provided by environment"), so one of these — and none of
		// the others — decides the account.
		switch {
		case a.IsToken():
			set["CODEX_ACCESS_TOKEN"] = a.resolvedToken()
			unset = append(unset, "CODEX_API_KEY", "OPENAI_API_KEY")
		case a.resolvedKey() != "":
			set["CODEX_API_KEY"] = a.resolvedKey()
			unset = append(unset, "CODEX_ACCESS_TOKEN", "OPENAI_API_KEY")
		}
	default: // claude
		switch {
		case a.ConfigDir == "":
		case isDefaultClaudeDir(a.ConfigDir):
			// The CLI keys its Keychain login by config dir, and it keys the
			// default dir differently when CLAUDE_CONFIG_DIR names it than when
			// it is left unset. So naming ~/.claude reaches a second, separate
			// login — one nothing refreshes, which answers "Not logged in"
			// once its token lapses (agent 1, 2026-09-26) while `claude` in a
			// terminal keeps working. Leaving it unset reaches the login the
			// person actually maintains.
			unset = append(unset, "CLAUDE_CONFIG_DIR")
		default:
			set["CLAUDE_CONFIG_DIR"] = a.ConfigDir
		}
		switch {
		case a.IsToken():
			// The token, and nothing that outranks it: an API key in the
			// environment would win over it and bill the wrong account.
			set["CLAUDE_CODE_OAUTH_TOKEN"] = a.resolvedToken()
			unset = append(unset, "ANTHROPIC_API_KEY", "ANTHROPIC_API_KEY_OLD", "ANTHROPIC_AUTH_TOKEN")
		case a.resolvedKey() != "":
			// An API key outranks a setup-token in the CLI, so an ambient
			// token can stay: it is never consulted.
			set["ANTHROPIC_API_KEY"] = a.resolvedKey()
		default:
			// No key for this account: the CLI must fall back to the OAuth
			// login in its config dir, so an ambient key or token must not
			// leak in and quietly answer as somebody else.
			unset = append(unset, "ANTHROPIC_API_KEY", "ANTHROPIC_API_KEY_OLD", "CLAUDE_CODE_OAUTH_TOKEN")
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
	return a.ConfigDir == "" && a.APIKey == "" && a.APIKeyEnv == "" &&
		a.OAuthToken == "" && a.OAuthTokenEnv == ""
}

// Status is one account's health, for `bomclaw accounts` and the dashboard.
type Status struct {
	Name        string `json:"name"`
	Provider    string `json:"provider"`
	Sessions    int    `json:"sessions"`                // sessions currently pinned here
	LastUsed    string `json:"last_used,omitempty"`     // RFC3339
	CoolingHint string `json:"cooling_until,omitempty"` // RFC3339 while rate-limited
	LastError   string `json:"last_error,omitempty"`
	// Kind is how it authenticates: "login" (a config dir), "api_key" or
	// "token" (a setup-token).
	Kind string `json:"kind"`
	// Current marks the token turns are going to now, and Uses how many it
	// has taken since it became current. Token rotation only.
	Current bool `json:"current,omitempty"`
	Uses    int  `json:"uses,omitempty"`
	// Turns is every turn this account has served since the gateway started.
	Turns int `json:"turns"`
	// Managed says it comes from the dashboard's token store.
	Managed bool `json:"managed,omitempty"`
}

type accountState struct {
	acct      Account
	lastUsed  time.Time
	sessions  int
	turns     int
	coolUntil time.Time
	lastErr   string
	// sharedErr is the error another agent benched this credential for,
	// shown when this agent has none of its own to show.
	sharedErr string
	// inShared marks a bench the shared state knows about. When the shared
	// row goes away — another agent's turn on it succeeded — so does this.
	inShared bool
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

	// Token rotation: every rotateEvery turns, the next token in the group
	// takes over. 0 leaves token accounts pinned per session like any other.
	rotateEvery int
	tokCursor   int // index into the token group of the current token
	tokUses     int // turns the current token has served

	// tokStarted is false until this agent's rotation has chosen its first
	// token, which it then chooses by load like any later one rather than
	// starting every restart on the same first token.
	tokStarted bool

	// shared is the state the agents on this machine keep in common:
	// cooldowns and recent load per credential. Nil runs on local state only.
	shared *Shared

	// The token store, when attached: re-read when its file changes.
	store      *TokenStore
	storeMtime time.Time
	baseRotate int // rotate_every from config; the store may override it
}

// GroupPin is what a session on the rotation is pinned to: the rotation as
// a whole, not one token in it. Deleting a token from the dashboard must not
// strand every conversation that happened to start on it.
const GroupPin = "@rotation"

// PinFor is the name a new session started on acct should be pinned to.
func (p *Pool) PinFor(acct Account) string {
	if p == nil {
		return acct.Name
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.rotateEvery > 0 && acct.rotates() && acct.store() == "" {
		return GroupPin
	}
	return acct.Name
}

// AttachShared makes this pool read and write the cooldowns and load the
// agents on this machine share. Nil detaches it.
func (p *Pool) AttachShared(s *Shared) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.shared = s
}

// Close releases the shared state, if one is attached. A pool lives as long
// as its gateway, so only tests need this — and on Windows they do, because an
// open database file cannot be deleted with the temp dir it sits in.
func (p *Pool) Close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.shared
	p.shared = nil
	return s.Close()
}

// syncSharedLocked folds the other agents' cooldowns into this pool's view.
// A shared bench only ever lengthens a local one. Caller holds p.mu.
func (p *Pool) syncSharedLocked(now time.Time) {
	if p.shared == nil {
		return
	}
	cool := p.shared.Cooldowns(now)
	for _, s := range p.accts {
		c, ok := cool[s.acct.Key()]
		if !ok {
			if s.inShared {
				s.coolUntil, s.lastErr, s.inShared = time.Time{}, "", false
			}
			s.sharedErr = ""
			continue
		}
		s.inShared = true
		if c.Until.After(s.coolUntil) {
			s.coolUntil = c.Until
		}
		s.sharedErr = c.Reason
		if c.Agent != "" {
			s.sharedErr += " (seen by " + c.Agent + ")"
		}
	}
}

// AttachStore makes the dashboard's token store part of this pool. Its
// tokens join the config's accounts and are re-read whenever the file
// changes, so an edit reaches the agent's next turn without a restart.
func (p *Pool) AttachStore(s *TokenStore) {
	if p == nil || s == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.store = s
	p.storeMtime = time.Time{}
	p.syncLocked()
}

// syncLocked reloads the store if its file changed. Caller holds p.mu.
// Accounts keep their counters and cooldowns across a reload, by name.
func (p *Pool) syncLocked() {
	if p.store == nil {
		return
	}
	var mtime time.Time
	if info, err := os.Stat(p.store.Path()); err == nil {
		mtime = info.ModTime()
	}
	if mtime.Equal(p.storeMtime) && !mtime.IsZero() {
		return
	}
	all, err := p.store.Load()
	if err != nil {
		log.Printf("credentials: token store: %v (keeping the previous tokens)", err)
		return
	}
	p.storeMtime = mtime
	pt := all[p.provider]

	prev := map[string]*accountState{}
	var kept []*accountState
	taken := map[string]bool{}
	for _, s := range p.accts {
		if s.acct.managed {
			prev[s.acct.Name] = s
			continue
		}
		kept = append(kept, s)
		taken[s.acct.Name] = true
	}
	for _, a := range pt.accounts(p.provider) {
		if taken[a.Name] {
			log.Printf("credentials: stored %s token %q is shadowed by a config account of that name", p.provider, a.Name)
			continue
		}
		taken[a.Name] = true
		if s, ok := prev[a.Name]; ok {
			s.acct = a
			kept = append(kept, s)
		} else {
			kept = append(kept, &accountState{acct: a})
		}
	}
	p.accts = kept
	p.rotateEvery = p.baseRotate
	if pt != nil && pt.RotateEvery > 0 {
		p.rotateEvery = pt.RotateEvery
	}
}

// AuthCooldown is how long a token that failed to authenticate is left out.
// Long, because a revoked or expired token does not recover by itself — but
// not forever, so a token someone renews comes back without a restart.
const AuthCooldown = 6 * time.Hour

// SetRotateEvery makes token accounts take turns: each serves n turns, then
// the next one does. n <= 0 turns rotation off.
func (p *Pool) SetRotateEvery(n int) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if n < 0 {
		n = 0
	}
	p.baseRotate, p.rotateEvery = n, n
	p.storeMtime = time.Time{} // let an attached store's override apply again
	p.syncLocked()
}

// tokenGroup is the token accounts a session anchored on `anchor` may move
// between: rotating, and sharing its session store. Caller holds p.mu.
func (p *Pool) tokenGroup(anchor Account) []*accountState {
	var out []*accountState
	for _, s := range p.accts {
		if s.acct.rotates() && s.acct.store() == anchor.store() {
			out = append(out, s)
		}
	}
	return out
}

// rotateToken returns the token a turn should use, advancing the rotation:
// the current token keeps serving until it has done rotateEvery turns, is
// cooling down, or has no token to give — then the next usable one takes
// over. Caller holds p.mu.
func (p *Pool) rotateToken(anchor *accountState) *accountState {
	group := p.tokenGroup(anchor.acct)
	if len(group) == 0 {
		return anchor
	}
	now := time.Now()
	usable := func(s *accountState) bool {
		return s.acct.usable() && !now.Before(s.coolUntil)
	}
	cur := p.tokCursor % len(group)
	if !p.tokStarted || p.tokUses >= p.rotateEvery || !usable(group[cur]) {
		// Look from the next one round to the current one itself, so a
		// group with one usable token keeps using it. With shared load, the
		// least-used usable token across every agent wins; ties keep the
		// round-robin order. The current token only competes on the first
		// pick, when there is no run to end.
		load := p.shared.Load(now)
		found := -1
		// Order of preference on a tie: after the current token when a run
		// ends, the current token itself first when there is no run yet.
		start, end := 1, len(group)
		if !p.tokStarted {
			start, end = 0, len(group)-1
		}
		for i := start; i <= end; i++ {
			j := (cur + i) % len(group)
			if !usable(group[j]) {
				continue
			}
			if found < 0 || load[group[j].acct.Key()] < load[group[found].acct.Key()] {
				found = j
			}
		}
		if found < 0 {
			// Nothing usable. The one recovering soonest gets the turn — a
			// cooldown is a guess, and an idle agent is worse than a try.
			found = cur
			for j, s := range group {
				if s.acct.usable() && s.coolUntil.Before(group[found].coolUntil) {
					found = j
				}
			}
		}
		if found != cur {
			p.tokCursor, p.tokUses = found, 0
		} else if p.tokUses >= p.rotateEvery {
			p.tokUses = 0 // the only usable token starts a fresh run
		}
		p.tokStarted = true
	}
	s := group[p.tokCursor%len(group)]
	p.tokUses++
	s.lastUsed = now
	s.turns++
	p.shared.RecordTurn(s.acct.Key(), now)
	return s
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
		if a.OAuthToken != "" || a.OAuthTokenEnv != "" {
			if a.APIKey != "" || a.APIKeyEnv != "" {
				return nil, fmt.Errorf("credentials: %q has both an API key and a setup-token — one account, one credential", a.Name)
			}
		}
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
	p.syncLocked()
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
	p.syncLocked()
	p.syncSharedLocked(time.Now())

	if len(p.accts) == 0 {
		return Account{}, nil // ambient credentials
	}

	if pinned == GroupPin {
		return p.pickGroup(), nil
	}

	if pinned != "" {
		for _, s := range p.accts {
			if s.acct.Name == pinned {
				// A session on a rotating token is not tied to that token:
				// its history is in the shared store, so whichever token is
				// current answers it.
				if p.rotateEvery > 0 && s.acct.rotates() {
					return p.rotateToken(s).acct, nil
				}
				s.lastUsed = time.Now()
				s.turns++
				p.shared.RecordTurn(s.acct.Key(), s.lastUsed)
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
		// An account can be in the pool without being in the rotation. A
		// colleague's login is the case that needs it: usable on purpose, and
		// never the answer to "start a chat" — otherwise every other new
		// conversation quietly lands on somebody else's quota.
		if s.acct.NoRotate {
			continue
		}
		if now.Before(s.coolUntil) {
			continue
		}
		if best == nil || s.lastUsed.Before(best.lastUsed) {
			best = s
		}
	}
	if best == nil {
		// Everything in the rotation is cooling down. Hand back whichever
		// recovers soonest and let the turn try: a cooldown is a guess about
		// someone else's rate limiter, not a fact, and refusing outright would
		// idle the agent. Opted-out accounts stay out — being unreachable by
		// exhaustion is the whole point of opting out.
		for _, s := range p.accts {
			if s.acct.NoRotate {
				continue
			}
			if best == nil || s.coolUntil.Before(best.coolUntil) {
				best = s
			}
		}
	}
	if best == nil {
		// Everything is opted out. The ambient credentials are what is left,
		// and they are what an install with no pool uses anyway.
		return Account{}, nil
	}
	p.next = (p.next + 1) % len(p.accts)
	if p.rotateEvery > 0 && best.acct.rotates() {
		return p.rotateToken(best).acct, nil
	}
	best.lastUsed = now
	best.turns++
	p.shared.RecordTurn(best.acct.Key(), now)
	return best.acct, nil
}

// pickGroup serves a session pinned to the rotation: the current token of
// the default store's rotation, or — when that rotation has been emptied —
// any account on the default store, since that is where the session lives.
// Caller holds p.mu.
func (p *Pool) pickGroup() Account {
	for _, s := range p.accts {
		if s.acct.rotates() && s.acct.store() == "" {
			if p.rotateEvery > 0 {
				return p.rotateToken(s).acct
			}
			s.lastUsed, s.turns = time.Now(), s.turns+1
			return s.acct
		}
	}
	return Account{} // the ambient login, which is the default store
}

// Failover returns another account this session can continue on after `from`
// failed with err, or false when there is none. Only a token in a rotation
// has anywhere to go: its session store is shared, so the conversation moves
// with the turn. Anything else stays where its history is.
//
// It marks the failure first, so the account it leaves is cooling down and
// the rotation steps past it.
func (p *Pool) Failover(from Account, err error) (Account, bool) {
	if p == nil || !from.rotates() || !(IsRateLimit(err) || IsAuthFailure(err)) {
		return Account{}, false
	}
	p.MarkFailure(from.Name, err)
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	p.syncSharedLocked(now)
	for _, s := range p.tokenGroup(from) {
		if s.acct.Name != from.Name && s.acct.usable() && !now.Before(s.coolUntil) {
			// Make it current, so the turns after this one stay on it too.
			for i, g := range p.tokenGroup(from) {
				if g == s {
					p.tokCursor, p.tokUses = i, 1
				}
			}
			p.tokStarted = true
			s.lastUsed = now
			s.turns++
			p.shared.RecordTurn(s.acct.Key(), now)
			return s.acct, true
		}
	}
	return Account{}, false
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
	var key, reason string
	var until time.Time
	p.each(name, func(s *accountState) {
		s.lastErr = truncateErr(err.Error())
		// Only a token is benched for an auth failure: a config-dir login
		// that says "not logged in" is the one login there is, and benching
		// it would change nothing but the message.
		if IsAuthFailure(err) && !IsRateLimit(err) && !s.acct.envCredential() {
			return
		}
		if u, ok := p.coolFor(err, time.Now()); ok {
			if u.After(s.coolUntil) {
				s.coolUntil = u
			}
			key, until, reason = s.acct.Key(), s.coolUntil, s.lastErr
			s.inShared = p.shared != nil
		}
	})
	if key != "" {
		p.shared.Bench(key, until, reason)
		log.Printf("credentials: %s benched until %s: %s", name, until.Format("15:04 Jan 2"), reason)
	}
}

// MarkSuccess clears a recorded error and lifts any cooldown: the account
// evidently works again.
func (p *Pool) MarkSuccess(name string) {
	var lift string
	p.each(name, func(s *accountState) {
		if !s.coolUntil.IsZero() || s.sharedErr != "" {
			lift = s.acct.Key()
		}
		s.lastErr = ""
		s.sharedErr = ""
		s.inShared = false
		s.coolUntil = time.Time{}
	})
	p.shared.Lift(lift)
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
	p.syncLocked()
	now := time.Now()
	p.syncSharedLocked(now)
	out := make([]Status, 0, len(p.accts))
	for _, s := range p.accts {
		st := Status{
			Name:      s.acct.Name,
			Provider:  s.acct.Provider,
			Sessions:  s.sessions,
			LastError: s.lastErr,
			Turns:     s.turns,
			Kind:      "login",
		}
		switch {
		case s.acct.IsToken():
			st.Kind = "token"
		case s.acct.APIKey != "" || s.acct.APIKeyEnv != "":
			st.Kind = "api_key"
		}
		st.Managed = s.acct.managed
		{
			if p.rotateEvery > 0 && s.acct.rotates() {
				if g := p.tokenGroup(s.acct); len(g) > 0 && g[p.tokCursor%len(g)] == s {
					st.Current, st.Uses = true, p.tokUses
				}
			}
			if !s.acct.usable() && st.LastError == "" {
				env := s.acct.OAuthTokenEnv
				if env == "" {
					env = s.acct.APIKeyEnv
				}
				st.LastError = "no credential: $" + env + " is empty"
			}
		}
		if !s.lastUsed.IsZero() {
			st.LastUsed = s.lastUsed.Format(time.RFC3339)
		}
		if now.Before(s.coolUntil) {
			st.CoolingHint = s.coolUntil.Format(time.RFC3339)
			if st.LastError == "" {
				st.LastError = s.sharedErr
			}
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
	// The provider throttling everyone says so — "Server is temporarily
	// limiting requests (not your usage limit)" — and contains "usage limit".
	// Benching an account for it would shrink the pool for nothing.
	if strings.Contains(msg, "not your usage limit") || strings.Contains(msg, "temporarily limiting requests") {
		return false
	}
	// Subscription caps, as the CLI words them: "You've hit your session
	// limit", "… weekly limit", "… Opus limit", "… monthly spend limit".
	if strings.Contains(msg, "hit your") && strings.Contains(msg, "limit") {
		return true
	}
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

// IsAuthFailure reports whether err says the credential itself was refused —
// an expired, revoked or malformed token — rather than throttled. A token
// that fails this way is left out for AuthCooldown, and the turn moves on.
func IsAuthFailure(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	// The CLI's own wording (code.claude.com/docs/en/errors): "Failed to
	// authenticate: OAuth token revoked", "API Error: 401 OAuth token has
	// expired", code authentication_failed, "Not logged in · Please run
	// /login", "Login expired".
	for _, sig := range []string{
		"401",
		"authentication_error",
		"authentication_failed",
		"failed to authenticate",
		"oauth token revoked",
		"oauth token has expired",
		"invalid bearer token",
		"invalid api key",
		"invalid x-api-key",
		"not logged in",
		"login expired",
		"please run /login",
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

// isDefaultClaudeDir is whether dir is the claude CLI's own default,
// ~/.claude.
func isDefaultClaudeDir(dir string) bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	return filepath.Clean(expandHome(dir)) == filepath.Join(home, ".claude")
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
