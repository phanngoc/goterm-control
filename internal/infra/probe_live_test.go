package infra

import (
	"context"
	"encoding/json"
	"os"
	"testing"
)

// INFRA_LIVE=1 go test ./internal/infra -run Live -v — reads this machine.
func TestLiveSnapshot(t *testing.T) {
	if os.Getenv("INFRA_LIVE") == "" {
		t.Skip("INFRA_LIVE not set")
	}
	s := New().Snapshot(context.Background())
	b, _ := json.MarshalIndent(s, "", " ")
	t.Log(string(b))
}
