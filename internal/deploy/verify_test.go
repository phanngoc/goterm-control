package deploy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// "Kiểm tra" on the Connections page: a live token that can see the account
// says so, with what it found; a refused one says it was refused.
func TestCheckingCredentialsSaysWhatTheTokenCanSee(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ok := func(v any) { b, _ := json.Marshal(map[string]any{"success": true, "result": v}); w.Write(b) }
		if r.Header.Get("Authorization") != "Bearer good" {
			w.WriteHeader(401)
			io.WriteString(w, `{"success":false,"errors":[{"code":1000,"message":"Invalid API Token"}]}`)
			return
		}
		switch r.URL.Path {
		case "/accounts/acct/tokens/verify":
			ok(map[string]string{"status": "active"})
		case "/accounts/acct":
			ok(map[string]string{"name": "Ngoc's account"})
		case "/accounts/acct/workers/subdomain":
			ok(map[string]string{"subdomain": "ngocp"})
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	c := NewClient(Creds{Token: "good", AccountID: "acct"})
	c.Base = srv.URL
	msg, err := c.Verify(context.Background())
	if err != nil || !strings.Contains(msg, "Ngoc's account") || !strings.Contains(msg, "ngocp") {
		t.Errorf("good token: %q %v", msg, err)
	}
	bad := NewClient(Creds{Token: "bad", AccountID: "acct"})
	bad.Base = srv.URL
	if _, err := bad.Verify(context.Background()); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Errorf("bad token: %v", err)
	}

	f, _ := fakeFly(t, true)
	if msg, err := f.Verify(context.Background()); err != nil || !strings.Contains(msg, "org personal") {
		t.Errorf("fly: %q %v", msg, err)
	}
}

// Credentials come through Lookup, so the secrets store can stand in front
// of the environment.
func TestProvidersReadCredentialsThroughLookup(t *testing.T) {
	old := Lookup
	defer func() { Lookup = old }()
	Lookup = func(env string) string {
		return map[string]string{"CLOUDFLARE_API_TOKEN": "t", "CLOUDFLARE_ACCOUNT_ID": "a", "FLY_API_TOKEN": "f", "FLY_REGION": "nrt"}[env]
	}
	if c := CredsFromEnv(); c.Token != "t" || c.AccountID != "a" {
		t.Errorf("cloudflare creds = %+v", c)
	}
	if f := NewFly(); f.Token != "f" || f.Region != "nrt" || f.Org != "personal" {
		t.Errorf("fly = %+v", f)
	}
	if _, err := Verify(context.Background(), "supabase"); err == nil {
		t.Error("supabase verified with no token")
	}
}
