package connectors

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Google sign-in for the Google connectors (Gmail, Drive, Sheets, Calendar,
// Search Console). The person's own OAuth client (google-oauth) is used, so
// nothing is brokered through a third party: the dashboard sends the browser
// to Google, Google sends it back to /api/connectors/oauth/callback with a
// code, and the refresh token it is traded for is stored like any key.

// Google endpoints; vars for tests.
var (
	GoogleAuthURL  = "https://accounts.google.com/o/oauth2/v2/auth"
	GoogleTokenURL = "https://oauth2.googleapis.com/token"
)

// CallbackPath is where Google returns the browser.
const CallbackPath = "/api/connectors/oauth/callback"

// StartPath begins a sign-in: ?id=<connector>&project=<channel>.
const StartPath = "/api/connectors/oauth/start"

// AuthURL is where to send the browser to connect c.
func AuthURL(c *Connector, clientID, redirect, state string) string {
	q := url.Values{
		"client_id":              {clientID},
		"redirect_uri":           {redirect},
		"response_type":          {"code"},
		"scope":                  {strings.Join(c.Scopes, " ")},
		"access_type":            {"offline"},
		"prompt":                 {"consent"}, // a refresh token every time, not only the first
		"include_granted_scopes": {"true"},
		"state":                  {state},
	}
	return GoogleAuthURL + "?" + q.Encode()
}

type tokenAnswer struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
}

func tokenCall(ctx context.Context, form url.Values) (*tokenAnswer, error) {
	var v tokenAnswer
	err := getJSON(ctx, rq{method: http.MethodPost, url: GoogleTokenURL, body: form.Encode(),
		header: map[string]string{"Content-Type": "application/x-www-form-urlencoded"}}, &v)
	if err != nil {
		return nil, fmt.Errorf("google: %w", err)
	}
	return &v, nil
}

// Exchange trades the code Google returned for a refresh token.
func Exchange(ctx context.Context, clientID, secret, code, redirect string) (string, error) {
	v, err := tokenCall(ctx, url.Values{"client_id": {clientID}, "client_secret": {secret}, "code": {code},
		"redirect_uri": {redirect}, "grant_type": {"authorization_code"}})
	if err != nil {
		return "", err
	}
	if v.RefreshToken == "" {
		return "", fmt.Errorf("google gave no refresh token — remove the app's access at myaccount.google.com/permissions and connect again")
	}
	return v.RefreshToken, nil
}

// GoogleAccessToken is a fresh access token for a stored refresh token.
func GoogleAccessToken(ctx context.Context, clientID, secret, refresh string) (string, error) {
	if clientID == "" || secret == "" {
		return "", fmt.Errorf("the Google OAuth client is not set up (GOOGLE_CLIENT_ID / GOOGLE_CLIENT_SECRET)")
	}
	if refresh == "" {
		return "", fmt.Errorf("not signed in with Google yet")
	}
	v, err := tokenCall(ctx, url.Values{"client_id": {clientID}, "client_secret": {secret},
		"refresh_token": {refresh}, "grant_type": {"refresh_token"}})
	if err != nil {
		return "", err
	}
	return v.AccessToken, nil
}

// Pending is a sign-in between leaving for Google and coming back.
type Pending struct {
	Connector string
	Project   string // channel to return to, optional
	Redirect  string // the redirect_uri sent, which the exchange must repeat
	By        string
	at        time.Time
}

var (
	pendMu  sync.Mutex
	pending = map[string]Pending{}
)

// pendingTTL is how long a sign-in may take.
const pendingTTL = 15 * time.Minute

// NewState remembers a sign-in and returns its state parameter.
func NewState(p Pending) string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	st := hex.EncodeToString(b)
	pendMu.Lock()
	defer pendMu.Unlock()
	now := time.Now()
	for k, v := range pending {
		if now.Sub(v.at) > pendingTTL {
			delete(pending, k)
		}
	}
	p.at = now
	pending[st] = p
	return st
}

// TakeState returns and forgets a sign-in; false for an unknown or expired one.
func TakeState(st string) (Pending, bool) {
	pendMu.Lock()
	defer pendMu.Unlock()
	p, ok := pending[st]
	delete(pending, st)
	if !ok || time.Since(p.at) > pendingTTL {
		return Pending{}, false
	}
	return p, true
}

// TokenField is the managed field an OAuth connector stores its token in.
func (c *Connector) TokenField() string {
	for _, f := range c.Fields {
		if f.Managed {
			return f.Env
		}
	}
	return ""
}

// originFile holds the dashboard's public address, beside secrets.yaml.
const originFile = "dashboard-origin"

// SaveOrigin records the dashboard's public address (https://host). The
// dashboard writes it at start; the agents' MCP tools read it to give a
// person a sign-in link they can open from a chat.
func SaveOrigin(secretsPath, origin string) error {
	p := filepath.Join(filepath.Dir(secretsPath), originFile)
	if old, err := os.ReadFile(p); err == nil && strings.TrimSpace(string(old)) == origin {
		return nil
	}
	return os.WriteFile(p, []byte(origin+"\n"), 0o644)
}

// Origin is the dashboard's public address, "" when it never said.
func Origin(secretsPath string) string {
	b, err := os.ReadFile(filepath.Join(filepath.Dir(secretsPath), originFile))
	if err != nil {
		return ""
	}
	return strings.TrimRight(strings.TrimSpace(string(b)), "/")
}
