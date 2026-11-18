package connectors

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ngocp/goterm-control/internal/secrets"
)

func TestEveryConnectorIsCompleteAndInTheStore(t *testing.T) {
	seen := map[string]bool{}
	cats := map[string]bool{"setup": true}
	for _, c := range Categories {
		cats[c.ID] = true
	}
	for _, c := range All() {
		if seen[c.ID] {
			t.Errorf("%s twice", c.ID)
		}
		seen[c.ID] = true
		if !cats[c.Category] {
			t.Errorf("%s: category %q", c.ID, c.Category)
		}
		if len(c.Fields) == 0 || c.Title == "" || c.Purpose == "" {
			t.Errorf("%s is incomplete", c.ID)
		}
		if !c.Hidden && (c.Guide == "" || c.verify == nil) {
			t.Errorf("%s has no guide or no check", c.ID)
		}
		if _, ok := secrets.ServiceByID(c.ID); !ok {
			t.Errorf("%s not registered in the secrets store", c.ID)
		}
		if c.Auth == AuthOAuth && (c.TokenField() == "" || len(c.Scopes) < 3) {
			t.Errorf("%s: oauth without a token field or scopes", c.ID)
		}
	}
	if c, ok := ByID("Google Sheets"); !ok || c.ID != "google-sheets" {
		t.Errorf("ByID by title = %v", c)
	}
}

func TestEnablingKeepsTheRestOfTheManifest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, Manifest)
	os.WriteFile(path, []byte(`{"preview":{"static":"."},"deploy":{"target":"cloudflare","static":"dist"}}`), 0o644)

	if ch, err := SetEnabled(dir, "stripe", true); err != nil || !ch {
		t.Fatalf("enable: %v %v", ch, err)
	}
	if ch, _ := SetEnabled(dir, "Resend", true); !ch {
		t.Fatal("enable by title did nothing")
	}
	if ch, _ := SetEnabled(dir, "stripe", true); ch {
		t.Error("enabling twice changed something")
	}
	got, _ := Enabled(dir)
	if strings.Join(got, ",") != "stripe,resend" {
		t.Errorf("enabled = %v", got)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), `"deploy"`) || !strings.Contains(string(raw), `"preview"`) {
		t.Errorf("other keys lost: %s", raw)
	}
	SetEnabled(dir, "stripe", false)
	SetEnabled(dir, "resend", false)
	raw, _ = os.ReadFile(path)
	if strings.Contains(string(raw), "connectors") {
		t.Errorf("empty list left behind: %s", raw)
	}
	if _, err := SetEnabled(dir, "google-oauth", true); err == nil {
		t.Error("the hidden OAuth client was enabled on a project")
	}
	if _, err := SetEnabled(dir, "nope", true); err == nil {
		t.Error("unknown connector enabled")
	}
}

func TestSecretsNeverReachTheBrowserBuild(t *testing.T) {
	dir := t.TempDir()
	SetEnabled(dir, "stripe", true)
	SetEnabled(dir, "gmail", true)
	vals := map[string]string{
		"STRIPE_SECRET_KEY": "sk_test_x", "STRIPE_PUBLISHABLE_KEY": "pk_test_y",
		"GOOGLE_CLIENT_ID": "id.apps.googleusercontent.com", "GOOGLE_CLIENT_SECRET": "cs",
	}
	vars, missing, err := ProjectEnv(dir, func(k string) string { return vals[k] })
	if err != nil {
		t.Fatal(err)
	}
	if missing["gmail"] == "" || missing["stripe"] != "" {
		t.Errorf("missing = %v", missing)
	}
	build := strings.Join(BuildEnv(vars), "\n")
	for _, secret := range []string{"sk_test_x", "GOOGLE_CLIENT_SECRET"} {
		if strings.Contains(build, secret) {
			t.Errorf("build env leaks %s:\n%s", secret, build)
		}
	}
	for _, want := range []string{"VITE_STRIPE_PUBLISHABLE_KEY=pk_test_y", "NEXT_PUBLIC_STRIPE_PUBLISHABLE_KEY=pk_test_y"} {
		if !strings.Contains(build, want) {
			t.Errorf("build env lacks %s", want)
		}
	}
	server := strings.Join(ServerEnv(vars), "\n")
	if !strings.Contains(server, "STRIPE_SECRET_KEY=sk_test_x") || !strings.Contains(server, "GOOGLE_CLIENT_SECRET=cs") {
		t.Errorf("server env = %s", server)
	}
	before := Fingerprint(vars)
	vals["STRIPE_SECRET_KEY"] = "sk_test_z"
	vars2, _, _ := ProjectEnv(dir, func(k string) string { return vals[k] })
	if Fingerprint(vars2) == before {
		t.Error("a replaced key did not change the fingerprint")
	}
}

// route sends every check to a test server, keeping the path.
func route(t *testing.T, h http.HandlerFunc) {
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	target, _ := url.Parse(srv.URL)
	old := HTTPClient
	HTTPClient = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		r.Header.Set("X-Original-Host", r.URL.Host)
		r.URL.Scheme, r.URL.Host = target.Scheme, target.Host
		return http.DefaultTransport.RoundTrip(r)
	})}
	t.Cleanup(func() { HTTPClient = old })
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestVerifyAsksTheServiceAndSaysWhatTheKeyReaches(t *testing.T) {
	route(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/balance" && r.Header.Get("Authorization") == "Bearer sk_test_good":
			w.Write([]byte(`{"livemode":false,"available":[{"currency":"usd"}]}`))
		case r.URL.Path == "/v1/balance":
			w.WriteHeader(401)
			w.Write([]byte(`{"error":{"message":"Invalid API Key provided: sk_test_****bad"}}`))
		default:
			w.WriteHeader(404)
		}
	})
	get := func(k string) string { return map[string]string{"STRIPE_SECRET_KEY": "sk_test_good"}[k] }
	d, err := Verify(context.Background(), "stripe", get)
	if err != nil || !strings.Contains(d, "TEST mode") {
		t.Fatalf("good key: %q %v", d, err)
	}
	bad := func(k string) string { return map[string]string{"STRIPE_SECRET_KEY": "sk_test_bad"}[k] }
	if _, err := Verify(context.Background(), "stripe", bad); err == nil || !strings.Contains(err.Error(), "Invalid API Key") {
		t.Errorf("bad key: %v", err)
	}
	if _, err := Verify(context.Background(), "stripe", func(string) string { return "" }); err == nil ||
		!strings.Contains(err.Error(), "STRIPE_SECRET_KEY") {
		t.Errorf("no key: %v", err)
	}
	if _, err := Verify(context.Background(), "gmail", func(string) string { return "" }); err == nil ||
		!strings.Contains(err.Error(), "Google OAuth client") {
		t.Errorf("gmail without client: %v", err)
	}
}

func TestGoogleSignInRoundTrip(t *testing.T) {
	route(t, func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		switch {
		case r.URL.Path == "/token" && r.Form.Get("grant_type") == "authorization_code" && r.Form.Get("code") == "c0de":
			w.Write([]byte(`{"access_token":"at","refresh_token":"rt"}`))
		case r.URL.Path == "/token" && r.Form.Get("refresh_token") == "rt":
			w.Write([]byte(`{"access_token":"at2"}`))
		case r.URL.Path == "/tokeninfo" && r.Form.Get("access_token") == "at2":
			w.Write([]byte(`{"email":"me@x.com","scope":"openid https://www.googleapis.com/auth/spreadsheets"}`))
		default:
			w.WriteHeader(400)
			w.Write([]byte(`{"error":"invalid_grant","error_description":"Bad Request"}`))
		}
	})
	c, _ := ByID("google-sheets")
	st := NewState(Pending{Connector: c.ID, Redirect: "https://d/cb"})
	u, _ := url.Parse(AuthURL(c, "cid", "https://d/cb", st))
	if q := u.Query(); q.Get("access_type") != "offline" || !strings.Contains(q.Get("scope"), "spreadsheets") || q.Get("state") != st {
		t.Errorf("auth url = %s", u)
	}
	p, ok := TakeState(st)
	if !ok || p.Connector != "google-sheets" {
		t.Fatalf("state lost: %v", p)
	}
	if _, ok := TakeState(st); ok {
		t.Error("a state was usable twice")
	}
	rt, err := Exchange(context.Background(), "cid", "cs", "c0de", p.Redirect)
	if err != nil || rt != "rt" {
		t.Fatalf("exchange: %q %v", rt, err)
	}
	vals := map[string]string{"GOOGLE_CLIENT_ID": "cid", "GOOGLE_CLIENT_SECRET": "cs", c.TokenField(): rt}
	d, err := Verify(context.Background(), c.ID, func(k string) string { return vals[k] })
	if err != nil || !strings.Contains(d, "me@x.com") {
		t.Errorf("verify: %q %v", d, err)
	}
	// Signed in, but for another scope than this connector needs.
	g, _ := ByID("gmail")
	vals[g.TokenField()] = rt
	if _, err := Verify(context.Background(), g.ID, func(k string) string { return vals[k] }); err == nil {
		t.Error("gmail accepted a token without gmail scope")
	}
}

func TestErrorsDoNotCarryKeysInURLs(t *testing.T) {
	old := HTTPClient
	HTTPClient = &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) {
		return nil, context.DeadlineExceeded
	})}
	defer func() { HTTPClient = old }()
	_, err := Verify(context.Background(), "telegram", func(k string) string { return "123:SECRETTOKEN" })
	if err == nil || strings.Contains(err.Error(), "SECRETTOKEN") {
		t.Errorf("err = %v", err)
	}
}
