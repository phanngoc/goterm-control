package gateway

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ngocp/goterm-control/internal/coord"
)

// The Connections page sets values, sees masks, and never gets a secret back.
func TestTheConnectionsPageNeverSeesASecret(t *testing.T) {
	cdb, err := coord.Open(filepath.Join(t.TempDir(), "coord.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer cdb.Close()
	deps := Deps{Coord: cdb}
	ctx := context.Background()

	if _, err := handleSecrets(ctx, deps, "secrets.set", json.RawMessage(
		`{"service":"fly","values":{"FLY_API_TOKEN":"fo1_supersecretvalue_123456","FLY_REGION":"nrt"}}`)); err != nil {
		t.Fatal(err)
	}
	raw, err := handleSecrets(ctx, deps, "secrets.list", nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "supersecretvalue") {
		t.Fatalf("the listing carries the secret:\n%s", raw)
	}
	if !strings.Contains(string(raw), `"masked":"fo1_…3456"`) || !strings.Contains(string(raw), `"masked":"nrt"`) {
		t.Errorf("listing = %s", raw)
	}

	// The check answers in the response, failure included; it never errors
	// the request, so the page can show why.
	raw, err = handleSecrets(ctx, deps, "secrets.verify", json.RawMessage(`{"service":"supabase"}`))
	if err != nil || !strings.Contains(string(raw), `"ok":false`) {
		t.Errorf("verify = %s, %v", raw, err)
	}

	if _, err := handleSecrets(ctx, deps, "secrets.unset", json.RawMessage(`{"service":"fly"}`)); err != nil {
		t.Fatal(err)
	}
	raw, _ = handleSecrets(ctx, deps, "secrets.list", nil)
	if strings.Contains(string(raw), "fo1_") {
		t.Error("unset left the token")
	}
}
