package credentials

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func mustPool(t *testing.T, provider string, accts []Account) *Pool {
	t.Helper()
	p, err := NewPool(provider, accts, time.Minute)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	return p
}

// No pool configured must behave exactly as before this feature existed: the
// CLI runs on whatever credentials the process already has.
func TestEmptyPoolYieldsAmbientAccount(t *testing.T) {
	p := mustPool(t, ProviderClaude, nil)
	if !p.Empty() {
		t.Fatal("a pool with no accounts must report empty")
	}
	a, err := p.Pick("")
	if err != nil || !a.Zero() {
		t.Fatalf("Pick on an empty pool = (%+v, %v), want the zero account", a, err)
	}
	// A nil pool is the same thing, so callers need no nil checks.
	var nilPool *Pool
	if !nilPool.Empty() {
		t.Error("a nil pool must report empty")
	}
	if a, err := nilPool.Pick(""); err != nil || !a.Zero() {
		t.Errorf("nil pool Pick = (%+v, %v)", a, err)
	}
}

// Accounts belonging to the other backend must never enter the rotation: a
// codex login is unreadable by the claude CLI.
func TestPoolFiltersByProvider(t *testing.T) {
	p := mustPool(t, ProviderClaude, []Account{
		{Name: "c1", Provider: ProviderClaude, ConfigDir: "/tmp/c1"},
		{Name: "x1", Provider: ProviderCodex, ConfigDir: "/tmp/x1"},
		{Name: "c2"}, // no provider: inherits the pool's
	})
	got := p.Names()
	if len(got) != 2 || got[0] != "c1" || got[1] != "c2" {
		t.Fatalf("names = %v, want [c1 c2]", got)
	}
}

func TestPoolRejectsBadConfig(t *testing.T) {
	if _, err := NewPool(ProviderClaude, []Account{{ConfigDir: "/tmp/x"}}, 0); err == nil {
		t.Error("an unnamed account must be rejected — the name is what a session pins to")
	}
	if _, err := NewPool(ProviderClaude, []Account{{Name: "a"}, {Name: "a"}}, 0); err == nil {
		t.Error("duplicate names must be rejected — a pin would be ambiguous")
	}
}

// Unpinned sessions spread across the pool instead of piling onto the first
// entry.
func TestPickSpreadsAcrossAccounts(t *testing.T) {
	p := mustPool(t, ProviderClaude, []Account{
		{Name: "a"}, {Name: "b"}, {Name: "c"},
	})
	seen := map[string]int{}
	for i := 0; i < 6; i++ {
		a, err := p.Pick("")
		if err != nil {
			t.Fatal(err)
		}
		seen[a.Name]++
	}
	for _, n := range []string{"a", "b", "c"} {
		if seen[n] != 2 {
			t.Errorf("account %s picked %d times, want 2 (even spread): %v", n, seen[n], seen)
		}
	}
}

// The pin is the whole point: a session's history lives in its account's
// config dir, so every later turn must return to the same account.
func TestPickHonoursThePin(t *testing.T) {
	p := mustPool(t, ProviderClaude, []Account{{Name: "a"}, {Name: "b"}})
	for i := 0; i < 5; i++ {
		got, err := p.Pick("b")
		if err != nil || got.Name != "b" {
			t.Fatalf("Pick(\"b\") = (%q, %v), want b", got.Name, err)
		}
	}
}

// A pinned account that is rate-limited is still returned: the conversation
// cannot be read anywhere else, so the turn must fail loudly rather than be
// silently restarted under another identity.
func TestPinnedAccountIsUsedEvenWhileCoolingDown(t *testing.T) {
	p := mustPool(t, ProviderClaude, []Account{{Name: "a"}, {Name: "b"}})
	p.MarkFailure("a", errors.New("429 rate limit exceeded"))

	got, err := p.Pick("a")
	if err != nil || got.Name != "a" {
		t.Fatalf("a pinned cooling account must still be returned, got (%q, %v)", got.Name, err)
	}
	// …while new sessions avoid it.
	fresh, err := p.Pick("")
	if err != nil || fresh.Name != "b" {
		t.Fatalf("a new session should avoid the cooling account, got (%q, %v)", fresh.Name, err)
	}
}

// A pin naming an account that is gone must fail rather than rehome the
// session, which would hand the user a blank conversation under another login.
func TestUnknownPinIsAnError(t *testing.T) {
	p := mustPool(t, ProviderClaude, []Account{{Name: "a"}})
	_, err := p.Pick("removed")
	if err == nil || !strings.Contains(err.Error(), "removed") {
		t.Fatalf("expected an error naming the missing account, got %v", err)
	}
}

// When every account is cooling down the agent still tries, rather than
// idling on a guess about someone else's rate limiter.
func TestAllCoolingStillReturnsSomething(t *testing.T) {
	p := mustPool(t, ProviderClaude, []Account{{Name: "a"}, {Name: "b"}})
	p.MarkFailure("a", errors.New("429 rate limit"))
	p.MarkFailure("b", errors.New("429 rate limit"))
	got, err := p.Pick("")
	if err != nil || got.Name == "" {
		t.Fatalf("expected an account anyway, got (%+v, %v)", got, err)
	}
}

func TestMarkSuccessLiftsCooldown(t *testing.T) {
	p := mustPool(t, ProviderClaude, []Account{{Name: "a"}, {Name: "b"}})
	p.MarkFailure("a", errors.New("429 rate limit"))
	p.MarkSuccess("a")
	if st := p.Status()[0]; st.CoolingHint != "" || st.LastError != "" {
		t.Errorf("success must clear the cooldown and the error, got %+v", st)
	}
}

// Only account-specific throttling counts. "overloaded" is the provider's own
// capacity problem and would sideline a perfectly good account.
func TestIsRateLimit(t *testing.T) {
	yes := []string{
		"429 Too Many Requests", "rate limit exceeded", "monthly quota reached",
		"usage limit reached", "insufficient_quota",
	}
	no := []string{
		"connection refused", "overloaded_error: server is overloaded",
		"context deadline exceeded", "invalid model",
	}
	for _, m := range yes {
		if !IsRateLimit(errors.New(m)) {
			t.Errorf("IsRateLimit(%q) = false, want true", m)
		}
	}
	for _, m := range no {
		if IsRateLimit(errors.New(m)) {
			t.Errorf("IsRateLimit(%q) = true, want false", m)
		}
	}
	if IsRateLimit(nil) {
		t.Error("IsRateLimit(nil) must be false")
	}
}

func TestAccountEnvClaude(t *testing.T) {
	// OAuth account: the config dir selects the identity, and an ambient API
	// key must be removed or the CLI would silently bill the wrong thing.
	set, unset := Account{Provider: ProviderClaude, ConfigDir: "/tmp/a"}.Env()
	if set["CLAUDE_CONFIG_DIR"] != "/tmp/a" {
		t.Errorf("CLAUDE_CONFIG_DIR not set: %v", set)
	}
	if _, ok := set["ANTHROPIC_API_KEY"]; ok {
		t.Error("an OAuth account must not set an API key")
	}
	if !contains(unset, "ANTHROPIC_API_KEY") {
		t.Errorf("an ambient API key must be unset, got %v", unset)
	}

	// API-key account.
	set, unset = Account{Provider: ProviderClaude, APIKey: "sk-test"}.Env()
	if set["ANTHROPIC_API_KEY"] != "sk-test" {
		t.Errorf("API key not set: %v", set)
	}
	if len(unset) != 0 {
		t.Errorf("nothing should be unset for a key account, got %v", unset)
	}
}

// The key can live in the environment so it need not sit in config.yaml.
func TestAccountEnvFromEnvVar(t *testing.T) {
	t.Setenv("POOL_KEY_A", "sk-from-env")
	set, _ := Account{Provider: ProviderClaude, APIKeyEnv: "POOL_KEY_A"}.Env()
	if set["ANTHROPIC_API_KEY"] != "sk-from-env" {
		t.Errorf("key should come from the named env var, got %v", set)
	}

	// An unset variable falls back to OAuth rather than exporting an empty key.
	set, unset := Account{Provider: ProviderClaude, APIKeyEnv: "POOL_KEY_MISSING"}.Env()
	if _, ok := set["ANTHROPIC_API_KEY"]; ok {
		t.Errorf("a missing env var must not export an empty key: %v", set)
	}
	if !contains(unset, "ANTHROPIC_API_KEY") {
		t.Errorf("expected the ambient key to be unset, got %v", unset)
	}
}

func TestAccountEnvCodex(t *testing.T) {
	set, unset := Account{Provider: ProviderCodex, ConfigDir: "/tmp/cx"}.Env()
	if set["CODEX_HOME"] != "/tmp/cx" {
		t.Errorf("CODEX_HOME not set: %v", set)
	}
	if len(unset) != 0 {
		t.Errorf("codex needs nothing unset, got %v", unset)
	}
}

func TestApplyEnvReplacesAndRemoves(t *testing.T) {
	base := []string{"PATH=/bin", "ANTHROPIC_API_KEY=ambient", "CLAUDE_CONFIG_DIR=/old"}

	got := ApplyEnv(base, Account{Provider: ProviderClaude, ConfigDir: "/new"})
	if !contains(got, "CLAUDE_CONFIG_DIR=/new") {
		t.Errorf("config dir not replaced: %v", got)
	}
	if contains(got, "CLAUDE_CONFIG_DIR=/old") {
		t.Errorf("stale config dir survived: %v", got)
	}
	if hasPrefix(got, "ANTHROPIC_API_KEY=") {
		t.Errorf("ambient key must be removed for an OAuth account: %v", got)
	}
	if !contains(got, "PATH=/bin") {
		t.Errorf("unrelated variables must survive: %v", got)
	}

	// The zero account changes nothing, which is what an unconfigured install
	// relies on.
	if len(ApplyEnv(base, Account{})) != len(base) {
		t.Error("the zero account must leave the environment untouched")
	}
}

func TestExpandHome(t *testing.T) {
	home, _ := os.UserHomeDir()
	p := mustPool(t, ProviderClaude, []Account{{Name: "a", ConfigDir: "~/.claude-alt"}})
	a, _ := p.Pick("a")
	if a.ConfigDir != home+"/.claude-alt" {
		t.Errorf("~ not expanded: %q", a.ConfigDir)
	}
}

func TestStatusReportsHealth(t *testing.T) {
	p := mustPool(t, ProviderClaude, []Account{{Name: "a"}, {Name: "b"}})
	_, _ = p.Pick("")
	p.MarkStarted("a")
	p.MarkFailure("b", errors.New("429 rate limit exceeded"))

	st := p.Status()
	if len(st) != 2 {
		t.Fatalf("want 2 statuses, got %d", len(st))
	}
	byName := map[string]Status{st[0].Name: st[0], st[1].Name: st[1]}
	if byName["a"].Sessions != 1 {
		t.Errorf("account a should show 1 pinned session, got %+v", byName["a"])
	}
	if byName["b"].CoolingHint == "" || !strings.Contains(byName["b"].LastError, "rate limit") {
		t.Errorf("account b should show the cooldown and the reason, got %+v", byName["b"])
	}
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

func hasPrefix(ss []string, prefix string) bool {
	for _, s := range ss {
		if strings.HasPrefix(s, prefix) {
			return true
		}
	}
	return false
}
