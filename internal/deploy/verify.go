package deploy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Verifier is a provider that can check its credentials without deploying.
type Verifier interface {
	Verify(ctx context.Context) (string, error)
}

func (d *Cloudflare) Verify(ctx context.Context) (string, error) {
	if d.Client == nil {
		return "", ErrNoCreds
	}
	return d.Client.Verify(ctx)
}

// SupabaseAPI is the Management API base; a var for tests.
var SupabaseAPI = "https://api.supabase.com"

// Verify checks the credentials of a service on the Connections page: a
// provider's (cloudflare, fly) or one stored ahead of its provider (supabase).
func Verify(ctx context.Context, service string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if service == "supabase" {
		return verifySupabase(ctx)
	}
	p, err := For(service)
	if err != nil {
		return "", err
	}
	v, ok := p.(Verifier)
	if !ok {
		return "", fmt.Errorf("%s cannot be checked without deploying", p.Title())
	}
	return v.Verify(ctx)
}

func verifySupabase(ctx context.Context) (string, error) {
	tok := Lookup("SUPABASE_ACCESS_TOKEN")
	if tok == "" {
		return "", fmt.Errorf("no Supabase access token stored")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, SupabaseAPI+"/v1/projects", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("the token was refused: %s", resp.Status)
	}
	var projects []struct {
		Name string `json:"name"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&projects)
	return fmt.Sprintf("token active · %d project(s) · not used by deploys yet", len(projects)), nil
}
