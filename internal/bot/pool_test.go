package bot

import (
	"path/filepath"
	"testing"

	"github.com/ngocp/goterm-control/internal/config"
	"github.com/ngocp/goterm-control/internal/credentials"
)

// TestTheBotRunsOnTheGatewaysPool: the gateway builds the agent's pool and the
// bot must run on that same one. Until this was fixed, the bot built its own:
// two rotations per agent, and `bomclaw accounts` blind to every chat turn.
func TestTheBotRunsOnTheGatewaysPool(t *testing.T) {
	c := cfgFor(config.ProviderClaude, "claude-opus-5")
	c.Coord.Path = filepath.Join(t.TempDir(), "coord.db") // keep the real token store out of it
	shared, err := PoolFromConfig(c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { shared.Close() })
	got, err := botPool(c, shared)
	if err != nil {
		t.Fatal(err)
	}
	if got != shared {
		t.Fatal("the bot built a second pool instead of using the gateway's")
	}

	// With nothing to share, it still builds one — cmd/goterm relies on that.
	own, err := botPool(c, nil)
	if err != nil || own == nil {
		t.Fatalf("botPool(nil) = %v, %v; want a pool from config", own, err)
	}
	own.Close()
}

// TestOneRotationAcrossChatAndTasks is what sharing buys: chat turns and task
// turns advance one count, so rotate_every means what it says for the agent.
func TestOneRotationAcrossChatAndTasks(t *testing.T) {
	pool, err := credentials.NewPool(config.ProviderClaude, []credentials.Account{
		{Name: "a", Provider: config.ProviderClaude, OAuthToken: "sk-ant-oat01-a"},
		{Name: "b", Provider: config.ProviderClaude, OAuthToken: "sk-ant-oat01-b"},
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	pool.SetRotateEvery(2)

	// chat, task, chat, task — two callers, one pool.
	var seq []string
	for i := 0; i < 4; i++ {
		acct, err := pool.Pick(credentials.GroupPin)
		if err != nil {
			t.Fatal(err)
		}
		seq = append(seq, acct.Name)
	}
	want := []string{"a", "a", "b", "b"}
	for i := range want {
		if seq[i] != want[i] {
			t.Fatalf("rotation across callers = %v, want %v", seq, want)
		}
	}
}
