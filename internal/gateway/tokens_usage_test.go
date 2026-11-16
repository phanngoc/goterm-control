package gateway

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/ngocp/goterm-control/internal/coord"
	"github.com/ngocp/goterm-control/internal/credentials"
)

// The usage chart reads the shared turns per stored credential and agent,
// in hourly buckets for a day and daily buckets for a week.
func TestTokensUsageChartsEachCredentialPerAgent(t *testing.T) {
	cdb, err := coord.Open(filepath.Join(t.TempDir(), "coord.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer cdb.Close()
	store := credentials.NewTokenStore(credentials.TokenStorePath(cdb.Path()))
	if err := store.Save(credentials.TokenInput{Provider: "claude", Name: "hue", Kind: credentials.KindSetupToken, Value: "sk-ant-oat01-hue-aaaaaaaa", Rotate: true}); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(credentials.TokenInput{Provider: "claude", Name: "tam", Kind: credentials.KindLogin, ConfigDir: "~/.claude-tam", Agents: []string{"bomclaw"}}); err != nil {
		t.Fatal(err)
	}
	all, _ := store.Load()
	key := map[string]string{}
	for _, tk := range all["claude"].Tokens {
		key[tk.Name] = tk.Account("claude").Key()
	}
	a, _ := credentials.OpenShared(credentials.SharedStatePath(cdb.Path()), "bomclaw")
	b, _ := credentials.OpenShared(credentials.SharedStatePath(cdb.Path()), "bomclaw2")
	now := time.Now()
	a.RecordTurn(key["hue"], now.Add(-10*time.Minute))
	b.RecordTurn(key["hue"], now.Add(-5*time.Minute))
	a.RecordTurn(key["tam"], now.Add(-3*24*time.Hour))
	a.Close()
	b.Close()

	raw, err := handleTokensUsage(Deps{Coord: cdb}, json.RawMessage(`{"days":1}`))
	if err != nil {
		t.Fatal(err)
	}
	var day TokenUsage
	_ = json.Unmarshal(raw, &day)
	if day.Bucket != 3600 || len(day.Buckets) != 24 {
		t.Fatalf("a day is 24 hourly buckets, got %d of %ds", len(day.Buckets), day.Bucket)
	}
	for _, l := range day.Tokens {
		sum := 0
		for _, xs := range l.ByAgent {
			for _, n := range xs[22:] { // the last two hours
				sum += n
			}
		}
		if l.Name == "hue" && (l.Total != 2 || sum != 2 || len(l.ByAgent) != 2) {
			t.Errorf("hue today = %+v", l)
		}
		if l.Name == "tam" && l.Total != 0 {
			t.Errorf("tam's turn was three days ago, not today: %+v", l)
		}
	}
	raw, _ = handleTokensUsage(Deps{Coord: cdb}, json.RawMessage(`{"days":7}`))
	var week TokenUsage
	_ = json.Unmarshal(raw, &week)
	if week.Bucket != 86400 || len(week.Buckets) != 7 {
		t.Fatalf("a week is 7 daily buckets, got %d", len(week.Buckets))
	}
	totals := map[string]int{}
	for _, l := range week.Tokens {
		totals[l.Name] = l.Total
	}
	if totals["hue"] != 2 || totals["tam"] != 1 {
		t.Errorf("week totals = %v", totals)
	}
}
