package gateway

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/ngocp/goterm-control/internal/credentials"
)

func TestTokensCRUDNeverReturnsTheSecret(t *testing.T) {
	deps, _, _ := hubFixture(t)
	const secret = "sk-ant-oat01-THISISTHESECRETPART-wxyz"
	call := func(method string, params any) (json.RawMessage, error) {
		raw, _ := json.Marshal(params)
		if method == "tokens.list" {
			return handleTokensList(deps, raw)
		}
		return handleTokensMutate(deps, method, raw)
	}

	out, err := call("tokens.save", map[string]any{"provider": "claude", "name": "work", "kind": "setup_token", "value": secret, "rotate": true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "THISISTHESECRETPART") {
		t.Fatalf("the secret came back: %s", out)
	}
	var views []ProviderView
	_ = json.Unmarshal(out, &views)
	if len(views) != 2 || views[0].Provider != "claude" || len(views[0].Tokens) != 1 {
		t.Fatalf("views = %+v", views)
	}
	tok := views[0].Tokens[0]
	if tok.Masked != "sk-ant-oat01-…wxyz" || !tok.Rotate {
		t.Fatalf("token view = %+v", tok)
	}
	if len(views[0].Agents) != 1 || views[0].Agents[0] != "bomclaw" {
		t.Fatalf("claude agents = %v", views[0].Agents)
	}

	// Edit without a value keeps it; the store still has the real secret.
	if _, err := call("tokens.save", map[string]any{"provider": "claude", "old_name": "work", "name": "work", "kind": "setup_token", "rotate": false}); err != nil {
		t.Fatal(err)
	}
	store := credentials.NewTokenStore(credentials.TokenStorePath(deps.Coord.Path()))
	all, _ := store.Load()
	if all["claude"].Tokens[0].Value != secret || all["claude"].Tokens[0].Rotate {
		t.Fatalf("stored = %+v", all["claude"].Tokens[0])
	}
	if b, _ := os.ReadFile(store.Path()); !strings.Contains(string(b), secret) {
		t.Fatal("the store file does not hold the token")
	}

	if _, err := call("tokens.settings", map[string]any{"provider": "claude", "rotate_every": 5}); err != nil {
		t.Fatal(err)
	}
	if _, err := call("tokens.save", map[string]any{"provider": "codex", "name": "cx", "kind": "access_token", "value": "eyJhbGciOiJSUzI1NiJ9.abcdefgh"}); err != nil {
		t.Fatal(err)
	}
	if _, err := call("tokens.save", map[string]any{"provider": "codex", "name": "bad", "kind": "setup_token", "value": "x"}); err == nil {
		t.Fatal("codex accepted a claude setup-token")
	}
	out, err = call("tokens.delete", map[string]any{"provider": "claude", "name": "work"})
	if err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(out, &views)
	if len(views[0].Tokens) != 0 || views[0].RotateEvery != 5 || len(views[1].Tokens) != 1 {
		t.Fatalf("after delete: %+v", views)
	}
}
