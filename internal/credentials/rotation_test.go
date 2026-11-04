package credentials

import (
	"errors"
	"testing"
	"time"
)

func tokenPool(t *testing.T, every int, accts ...Account) *Pool {
	t.Helper()
	p, err := NewPool(ProviderClaude, accts, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	p.SetRotateEvery(every)
	return p
}

func tok(name, value string) Account { return Account{Name: name, OAuthToken: value} }

// picks returns the account names of n turns of one session.
func picks(t *testing.T, p *Pool, pinned string, n int) []string {
	t.Helper()
	var out []string
	for i := 0; i < n; i++ {
		a, err := p.Pick(pinned)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, a.Name)
	}
	return out
}

func TestTokensTakeTurnsEveryN(t *testing.T) {
	p := tokenPool(t, 5, tok("a", "ta"), tok("b", "tb"), tok("c", "tc"))
	got := picks(t, p, "a", 16)
	want := []string{"a", "a", "a", "a", "a", "b", "b", "b", "b", "b", "c", "c", "c", "c", "c", "a"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("turn %d: got %v\nwant %v", i+1, got, want)
		}
	}
}

// The count is the agent's, not the session's: turns from different
// sessions all advance the same rotation.
func TestRotationCountsEveryTurnOfEverySession(t *testing.T) {
	p := tokenPool(t, 2, tok("a", "ta"), tok("b", "tb"))
	first, _ := p.Pick("")  // a new session lands on the group
	second, _ := p.Pick("") // another new session
	third, _ := p.Pick(first.Name)
	if first.Name != "a" || second.Name != "a" || third.Name != "b" {
		t.Fatalf("got %s %s %s", first.Name, second.Name, third.Name)
	}
}

func TestRotationSkipsCoolingAndEmptyTokens(t *testing.T) {
	t.Setenv("TOKEN_B_UNSET", "")
	p := tokenPool(t, 3,
		tok("a", "ta"),
		Account{Name: "b", OAuthTokenEnv: "TOKEN_B_UNSET"},
		tok("c", "tc"))
	p.MarkFailure("c", errors.New("You've hit your weekly limit"))
	// a serves its 3, b has no token, c is cooling → back to a.
	got := picks(t, p, "a", 6)
	for _, n := range got {
		if n != "a" {
			t.Fatalf("got %v", got)
		}
	}
	st := statusOf(p, "b")
	if st.Kind != "token" || st.LastError == "" {
		t.Fatalf("an empty token was not reported: %+v", st)
	}
}

func TestFailoverMovesTheTurnAndTheRotation(t *testing.T) {
	p := tokenPool(t, 5, tok("a", "ta"), tok("b", "tb"))
	a, _ := p.Pick("a")
	next, ok := p.Failover(a, errors.New("Failed to authenticate. API Error: 401 OAuth token has expired"))
	if !ok || next.Name != "b" {
		t.Fatalf("failover: %v %v", next, ok)
	}
	if st := statusOf(p, "a"); st.CoolingHint == "" {
		t.Fatal("the refused token is not benched")
	}
	// The following turns stay on b.
	if got := picks(t, p, "a", 3); got[0] != "b" || got[2] != "b" {
		t.Fatalf("after failover: %v", got)
	}
	// A failure that is not the account's does not move anything.
	if _, ok := p.Failover(next, errors.New("API Error: Server is temporarily limiting requests (not your usage limit)")); ok {
		t.Fatal("a provider-wide throttle moved the turn")
	}
	// Nowhere to go: the other one is benched.
	if _, ok := p.Failover(next, errors.New("429 Too Many Requests")); ok {
		t.Fatal("failed over onto a benched token")
	}
}

// Logins and opted-out tokens keep today's behaviour: pinned per session.
func TestOnlyRotatingTokensMove(t *testing.T) {
	p := tokenPool(t, 1,
		Account{Name: "login", ConfigDir: "/tmp/claude-x"},
		Account{Name: "colleague", OAuthToken: "tx", NoRotate: true},
		tok("a", "ta"), tok("b", "tb"))
	for _, pinned := range []string{"login", "colleague"} {
		for _, n := range picks(t, p, pinned, 3) {
			if n != pinned {
				t.Fatalf("%s moved to %s", pinned, n)
			}
		}
		if _, ok := p.Failover(Account{Name: pinned, OAuthToken: "x", NoRotate: pinned == "colleague"}, errors.New("429")); ok && pinned == "colleague" {
			t.Fatal("an opted-out token was failed over")
		}
	}
	if _, ok := p.Failover(Account{Name: "login", ConfigDir: "/tmp/claude-x"}, errors.New("429")); ok {
		t.Fatal("a login was failed over — its history cannot move")
	}
}

// Tokens move only within one session store.
func TestTokensDoNotCrossSessionStores(t *testing.T) {
	p := tokenPool(t, 1,
		tok("a", "ta"),
		Account{Name: "elsewhere", OAuthToken: "te", ConfigDir: "/tmp/other-store"})
	for _, n := range picks(t, p, "a", 4) {
		if n != "a" {
			t.Fatalf("crossed into another store: %s", n)
		}
	}
}

func TestTokenEnv(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "ambient")
	t.Setenv("MY_TOKEN", "sk-ant-oat01-xyz")
	set, unset := Account{Name: "a", Provider: ProviderClaude, OAuthTokenEnv: "MY_TOKEN"}.Env()
	if set["CLAUDE_CODE_OAUTH_TOKEN"] != "sk-ant-oat01-xyz" {
		t.Fatalf("set = %v", set)
	}
	// An API key or auth token in the environment outranks the token in the
	// CLI and would bill the wrong account.
	for _, k := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"} {
		found := false
		for _, u := range unset {
			found = found || u == k
		}
		if !found {
			t.Errorf("%s not unset: %v", k, unset)
		}
	}
	if _, has := set["CLAUDE_CONFIG_DIR"]; has {
		t.Fatal("a token account changed the session store")
	}
	// A login must not be answered by a token that leaked into the env.
	_, unset = Account{Name: "l", Provider: ProviderClaude, ConfigDir: "/tmp/x"}.Env()
	found := false
	for _, u := range unset {
		found = found || u == "CLAUDE_CODE_OAUTH_TOKEN"
	}
	if !found {
		t.Fatal("an ambient setup-token could answer for a login account")
	}
}

func TestErrorClassification(t *testing.T) {
	for msg, want := range map[string][2]bool{ // {rate limit, auth failure}
		"You've hit your session limit · resets 3pm":                                {true, false},
		"You've hit your monthly spend limit":                                       {true, false},
		"API Error: Request rejected (429)":                                         {true, false},
		"API Error: Server is temporarily limiting requests (not your usage limit)": {false, false},
		"Failed to authenticate: OAuth token revoked":                               {false, true},
		"Failed to authenticate. API Error: 401 OAuth token has expired":            {false, true},
		"Not logged in · Please run /login":                                         {false, true},
		"API Error: 529 overloaded":                                                 {false, false},
	} {
		err := errors.New(msg)
		if IsRateLimit(err) != want[0] || IsAuthFailure(err) != want[1] {
			t.Errorf("%q: rate=%v auth=%v, want %v", msg, IsRateLimit(err), IsAuthFailure(err), want)
		}
	}
}

func TestBadTokenConfigIsRefused(t *testing.T) {
	if _, err := NewPool(ProviderCodex, []Account{{Name: "x", Provider: ProviderCodex, OAuthToken: "t"}}, 0); err == nil {
		t.Error("a codex account accepted a claude setup-token")
	}
	if _, err := NewPool(ProviderClaude, []Account{{Name: "x", OAuthToken: "t", APIKey: "k"}}, 0); err == nil {
		t.Error("an account with two credentials was accepted")
	}
}

func statusOf(p *Pool, name string) Status {
	for _, s := range p.Status() {
		if s.Name == name {
			return s
		}
	}
	return Status{}
}
