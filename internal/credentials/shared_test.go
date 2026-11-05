package credentials

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResetAtReadsTheLimitMessage(t *testing.T) {
	saigon, err := time.LoadLocation("Asia/Saigon")
	if err != nil {
		t.Skip("no tzdata")
	}
	now := time.Date(2026, 11, 5, 14, 42, 0, 0, saigon)
	cases := []struct {
		msg  string
		want time.Time
	}{
		{"You've hit your 5-hour limit · resets 7:20pm (Asia/Saigon)", time.Date(2026, 11, 5, 19, 20, 0, 0, saigon)},
		{"You've hit your session limit · resets 3pm (Asia/Saigon)", time.Date(2026, 11, 5, 15, 0, 0, 0, saigon)},
		// already past today: tomorrow
		{"limit reached · resets 9am (Asia/Saigon)", time.Date(2026, 11, 6, 9, 0, 0, 0, saigon)},
		{"You've hit your weekly limit · resets Nov 7, 3pm (Asia/Saigon)", time.Date(2026, 11, 7, 15, 0, 0, 0, saigon)},
		{"weekly limit · resets 12am (Asia/Saigon)", time.Date(2026, 11, 6, 0, 0, 0, 0, saigon)},
	}
	for _, c := range cases {
		got, ok := ResetAt(c.msg, now)
		if !ok || !got.Equal(c.want) {
			t.Errorf("%q → %v %v, want %v", c.msg, got, ok, c.want)
		}
	}
	// The zone in the message wins over now's zone.
	got, ok := ResetAt("resets 7:20pm (Asia/Saigon)", now.UTC())
	if !ok || !got.Equal(time.Date(2026, 11, 5, 19, 20, 0, 0, saigon)) {
		t.Errorf("UTC now: %v %v", got, ok)
	}
	for _, msg := range []string{"429 rate limit", "resets soon", "resets 13pm", "resets Foo 3, 3pm"} {
		if _, ok := ResetAt(msg, now); ok {
			t.Errorf("%q should not parse", msg)
		}
	}
}

// A capped token is benched until the reset its error names, not for the
// configured cooldown: the 15-minute bench is what put a capped token back in
// front of the person every quarter hour on 2026-11-05.
func TestACapIsBenchedUntilItsReset(t *testing.T) {
	p := tokenPool(t, 5, tok("a", "ta"), tok("b", "tb"))
	reset := time.Now().Add(3 * time.Hour).Truncate(time.Minute)
	p.MarkFailure("a", fmt.Errorf("claude error: You've hit your 5-hour limit · resets %s", reset.Format("3:04pm")))
	var until time.Time
	for _, st := range p.Status() {
		if st.Name == "a" {
			until, _ = time.Parse(time.RFC3339, st.CoolingHint)
		}
	}
	if want := reset.Add(time.Minute); !until.Equal(want) {
		t.Fatalf("benched until %v, want the reset plus a minute (%v)", until, want)
	}
}

func sharedPools(t *testing.T) (*Pool, *Pool) {
	t.Helper()
	path := filepath.Join(t.TempDir(), SharedFile)
	mk := func(agent string) *Pool {
		s, err := OpenShared(path, agent)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		p := tokenPool(t, 5, tok("tien", "tok-1"), tok("hue", "tok-2"))
		p.AttachShared(s)
		return p
	}
	return mk("bomclaw"), mk("bomclaw2")
}

// One agent finding a cap benches the token for all of them.
func TestACapSeenByOneAgentBenchesItForAll(t *testing.T) {
	one, two := sharedPools(t)
	one.MarkFailure("tien", errors.New("You've hit your 5-hour limit · resets 11:59pm"))
	for i := 0; i < 6; i++ {
		a, err := two.Pick(GroupPin)
		if err != nil {
			t.Fatal(err)
		}
		if a.Name == "tien" {
			t.Fatalf("turn %d: agent two used the token agent one found capped", i)
		}
	}
	for _, st := range two.Status() {
		if st.Name == "tien" && (st.CoolingHint == "" || st.LastError == "") {
			t.Fatalf("agent two does not report the shared bench: %+v", st)
		}
	}
	// A success anywhere lifts it everywhere.
	two.MarkSuccess("tien")
	for _, st := range one.Status() {
		if st.Name == "tien" && st.CoolingHint != "" {
			t.Fatal("the lifted bench still holds on agent one")
		}
	}
}

// When an agent starts a run it takes the token the others have used least,
// instead of every agent piling onto the first token after a restart.
func TestARunStartsOnTheLeastUsedToken(t *testing.T) {
	one, two := sharedPools(t)
	for i := 0; i < 4; i++ {
		if a, _ := one.Pick(GroupPin); a.Name != "tien" {
			t.Fatalf("agent one turn %d on %s", i, a.Name)
		}
	}
	if a, _ := two.Pick(GroupPin); a.Name != "hue" {
		t.Fatalf("agent two started on %s; tien already carries agent one's load", a.Name)
	}
}

// Keys are shared by secret, not by name, and never contain the secret.
func TestKeysIdentifyTheCredential(t *testing.T) {
	a := Account{Name: "x", Provider: ProviderClaude, OAuthToken: "sk-ant-oat01-secret"}
	b := Account{Name: "y", Provider: ProviderClaude, OAuthToken: "sk-ant-oat01-secret"}
	c := Account{Name: "x", Provider: ProviderClaude, OAuthToken: "sk-ant-oat01-other"}
	if a.Key() != b.Key() || a.Key() == c.Key() {
		t.Fatalf("keys: %s %s %s", a.Key(), b.Key(), c.Key())
	}
	if strings.Contains(a.Key(), "secret") {
		t.Fatalf("key leaks the secret: %s", a.Key())
	}
}
