package deploy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Creds are a Cloudflare API token and the account it acts in. Read from the
// environment under the names wrangler itself uses, so the same two lines in
// the gateway's .env serve both this package and wrangler.
type Creds struct {
	Token     string
	AccountID string
}

// Lookup reads one credential by its environment-variable name. Processes
// that have the secrets store (the dashboard, gateways, `bomclaw env`) point
// it at secrets.Store.Lookup, so a value entered on the Connections page or by
// an agent wins over .env; the default is the environment alone.
var Lookup = func(env string) string { return strings.TrimSpace(os.Getenv(env)) }

// CredsFromEnv reads CLOUDFLARE_API_TOKEN and CLOUDFLARE_ACCOUNT_ID (through
// Lookup: the secrets store first, then the environment).
func CredsFromEnv() Creds {
	return Creds{
		Token:     Lookup("CLOUDFLARE_API_TOKEN"),
		AccountID: Lookup("CLOUDFLARE_ACCOUNT_ID"),
	}
}

// ErrNoCreds says what to set, because the person reading it has to set it.
var ErrNoCreds = errors.New("Cloudflare is not connected: add the API token and account ID on the dashboard (Admin → Connections), " +
	"or `bomclaw secrets set cloudflare CLOUDFLARE_API_TOKEN=… CLOUDFLARE_ACCOUNT_ID=…`")

// OK reports whether both are set.
func (c Creds) OK() bool { return c.Token != "" && c.AccountID != "" }

// Client is the slice of the Cloudflare API that PROD status needs. Deploys
// go through wrangler, which owns the asset-upload protocol.
type Client struct {
	Creds Creds
	Base  string // default https://api.cloudflare.com/client/v4
	HTTP  *http.Client
}

func NewClient(c Creds) *Client {
	return &Client{Creds: c, Base: "https://api.cloudflare.com/client/v4", HTTP: &http.Client{Timeout: 20 * time.Second}}
}

type apiResp struct {
	Success bool            `json:"success"`
	Errors  []apiErr        `json:"errors"`
	Result  json.RawMessage `json:"result"`
}

type apiErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// errNotFound is a 404 from the API: the thing has not been created yet.
var errNotFound = errors.New("not found")

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Base+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Creds.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("cloudflare: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return errNotFound
	}
	var r apiResp
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return fmt.Errorf("cloudflare %s %s: %s", method, path, resp.Status)
	}
	if !r.Success {
		msgs := make([]string, 0, len(r.Errors))
		for _, e := range r.Errors {
			msgs = append(msgs, fmt.Sprintf("%d %s", e.Code, e.Message))
		}
		return fmt.Errorf("cloudflare %s %s: %s", method, path, strings.Join(msgs, "; "))
	}
	if out != nil && len(r.Result) > 0 {
		return json.Unmarshal(r.Result, out)
	}
	return nil
}

func (c *Client) acct() string { return "/accounts/" + url.PathEscape(c.Creds.AccountID) }

// Deployment is the live version of a Worker.
type Deployment struct {
	ID        string    `json:"id"`
	CreatedOn time.Time `json:"created_on"`
	Source    string    `json:"source,omitempty"`
}

// LatestDeployment is the Worker's newest deployment, or nil when the Worker
// does not exist yet.
func (c *Client) LatestDeployment(ctx context.Context, name string) (*Deployment, error) {
	var out struct {
		Deployments []Deployment `json:"deployments"`
	}
	err := c.do(ctx, http.MethodGet, c.acct()+"/workers/scripts/"+url.PathEscape(name)+"/deployments", nil, &out)
	if errors.Is(err, errNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(out.Deployments) == 0 {
		return nil, nil
	}
	latest := out.Deployments[0]
	for _, d := range out.Deployments[1:] {
		if d.CreatedOn.After(latest.CreatedOn) {
			latest = d
		}
	}
	return &latest, nil
}

// Subdomain is the account's workers.dev subdomain ("" when none is set up).
func (c *Client) Subdomain(ctx context.Context) (string, error) {
	var out struct {
		Subdomain string `json:"subdomain"`
	}
	err := c.do(ctx, http.MethodGet, c.acct()+"/workers/subdomain", nil, &out)
	if errors.Is(err, errNotFound) {
		return "", nil
	}
	return out.Subdomain, err
}

// Domains are the custom hostnames attached to a Worker.
func (c *Client) Domains(ctx context.Context, name string) ([]string, error) {
	var out []struct {
		Hostname string `json:"hostname"`
		Service  string `json:"service"`
	}
	if err := c.do(ctx, http.MethodGet, c.acct()+"/workers/domains?service="+url.QueryEscape(name), nil, &out); err != nil {
		if errors.Is(err, errNotFound) {
			return nil, nil
		}
		return nil, err
	}
	hosts := []string{}
	for _, d := range out {
		if d.Service == "" || d.Service == name {
			hosts = append(hosts, d.Hostname)
		}
	}
	return hosts, nil
}

// D1 is one hosted database.
type D1 struct {
	UUID      string `json:"uuid"`
	Name      string `json:"name"`
	FileSize  int64  `json:"file_size"`
	NumTables int    `json:"num_tables"`
	CreatedAt string `json:"created_at,omitempty"`
}

// FindD1 is the database with this name, or nil.
func (c *Client) FindD1(ctx context.Context, name string) (*D1, error) {
	var out []D1
	if err := c.do(ctx, http.MethodGet, c.acct()+"/d1/database?name="+url.QueryEscape(name), nil, &out); err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].Name == name {
			// The listing's size and table count lag behind by minutes — right
			// after an import it said "0 tables" for a database holding data.
			// The database's own endpoint is current.
			var d D1
			if err := c.do(ctx, http.MethodGet, c.acct()+"/d1/database/"+url.PathEscape(out[i].UUID), nil, &d); err == nil && d.UUID != "" {
				return &d, nil
			}
			return &out[i], nil
		}
	}
	return nil, nil
}

// EnsureD1 returns the database, creating it the first time.
func (c *Client) EnsureD1(ctx context.Context, name string) (*D1, error) {
	if d, err := c.FindD1(ctx, name); err != nil || d != nil {
		return d, err
	}
	var d D1
	if err := c.do(ctx, http.MethodPost, c.acct()+"/d1/database", map[string]string{"name": name}, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// Verify checks that the token is live and can see the account.
func (c *Client) Verify(ctx context.Context) (string, error) {
	if !c.Creds.OK() {
		return "", ErrNoCreds
	}
	// Account-owned tokens verify under the account; user tokens under /user.
	var tok struct {
		Status string `json:"status"`
	}
	err := c.do(ctx, http.MethodGet, c.acct()+"/tokens/verify", nil, &tok)
	if err != nil {
		if err2 := c.do(ctx, http.MethodGet, "/user/tokens/verify", nil, &tok); err2 != nil {
			return "", fmt.Errorf("the token was refused: %v", err2)
		}
	}
	if tok.Status != "" && tok.Status != "active" {
		return "", fmt.Errorf("the token is %s", tok.Status)
	}
	var acct struct {
		Name string `json:"name"`
	}
	if err := c.do(ctx, http.MethodGet, c.acct(), nil, &acct); err != nil {
		return "", fmt.Errorf("the token is valid but cannot read account %s: %v", c.Creds.AccountID, err)
	}
	sub, _ := c.Subdomain(ctx)
	msg := "token active · account " + acct.Name
	if sub != "" {
		msg += " · workers.dev subdomain " + sub
	} else {
		msg += " · no workers.dev subdomain yet (the first deploy sets one up)"
	}
	return msg, nil
}
