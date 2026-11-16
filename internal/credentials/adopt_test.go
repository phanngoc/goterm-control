package credentials

import (
	"path/filepath"
	"testing"
	"time"
)

// config.yaml's accounts move into the one store: a login becomes a stored
// login kept for the agent it came from, a literal token a stored token, and
// one that reads its secret from the environment stays in config. A second
// start moves nothing and shadows nothing.
func TestConfigAccountsMoveIntoTheStore(t *testing.T) {
	store := NewTokenStore(filepath.Join(t.TempDir(), TokenFile))
	accts := []Account{
		{Name: "default", Provider: ProviderClaude, ConfigDir: "~/.claude"},
		{Name: "tam", Provider: ProviderClaude, ConfigDir: "~/.claude-tam", NoRotate: true},
		{Name: "lit", Provider: ProviderClaude, OAuthToken: "sk-ant-oat01-abcdefghijkl"},
		{Name: "env", Provider: ProviderClaude, OAuthTokenEnv: "SOME_TOKEN"},
	}
	rest, moved, err := store.Adopt(ProviderClaude, "bomclaw", accts)
	if err != nil {
		t.Fatal(err)
	}
	if len(moved) != 3 || len(rest) != 1 || rest[0].Name != "env" {
		t.Fatalf("moved %v, rest %+v", moved, rest)
	}
	all, _ := store.Load()
	byName := map[string]StoredToken{}
	for _, tk := range all[ProviderClaude].Tokens {
		byName[tk.Name] = tk
	}
	if d := byName["default"]; d.Kind != KindLogin || d.ConfigDir != "~/.claude" || !d.Rotate || len(d.Agents) != 1 || d.Agents[0] != "bomclaw" {
		t.Errorf("default = %+v", d)
	}
	if tam := byName["tam"]; tam.Rotate {
		t.Errorf("tam was opted out of the rotation and must stay out: %+v", tam)
	}
	if l := byName["lit"]; l.Kind != KindSetupToken || l.Value == "" {
		t.Errorf("lit = %+v", l)
	}

	// Again: nothing new, nothing returned to shadow the stored entries.
	rest, moved, err = store.Adopt(ProviderClaude, "bomclaw", accts)
	if err != nil || len(moved) != 0 || len(rest) != 1 {
		t.Errorf("second adopt moved %v, rest %v, err %v", moved, rest, err)
	}

	// The pool of the agent they came from has them; another agent's does not.
	for agent, want := range map[string]bool{"bomclaw": true, "bomclaw2": false} {
		p, _ := NewPool(ProviderClaude, rest, time.Minute)
		p.SetAgent(agent)
		p.AttachStore(store)
		has := false
		for _, n := range p.Names() {
			if n == "default" {
				has = true
			}
		}
		if has != want {
			t.Errorf("%s pool has default = %v, want %v (names %v)", agent, has, want, p.Names())
		}
	}
}

// Turns are kept for the chart and counted per credential, agent and bucket.
func TestHistoryCountsTurnsPerBucket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	a, err := OpenShared(path, "bomclaw")
	if err != nil {
		t.Fatal(err)
	}
	defer a.db.Close()
	b, _ := OpenShared(path, "bomclaw2")
	defer b.db.Close()
	base := time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)
	a.RecordTurn("k1", base.Add(5*time.Minute))
	a.RecordTurn("k1", base.Add(50*time.Minute))
	b.RecordTurn("k1", base.Add(70*time.Minute))
	a.RecordTurn("k2", base.Add(-30*24*time.Hour)) // past the query

	pts, err := a.History(base.Add(-time.Hour), time.Hour, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, p := range pts {
		got[p.Key+"/"+p.Agent+"/"+time.Unix(p.Bucket, 0).UTC().Format("15")] += p.Turns
	}
	want := map[string]int{"k1/bomclaw/09": 2, "k1/bomclaw2/10": 1}
	if len(got) != len(want) || got["k1/bomclaw/09"] != 2 || got["k1/bomclaw2/10"] != 1 {
		t.Errorf("history = %v, want %v", got, want)
	}
	if last := a.LastUsed()["k1"]; !last.Equal(base.Add(70 * time.Minute)) {
		t.Errorf("last used = %v", last)
	}
}
