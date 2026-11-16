package deploy

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// What PROD runs on, in the words a person checks a bill in: whose account,
// which plan, which of the provider's services the project uses.
//
// The room header shows this beside the project name. "Cloudflare" alone does
// not answer the questions people actually have about a live site — is it on
// the free plan, is the database D1 or a file, is that domain ours — and each
// of those took a trip to the provider's dashboard.

// Account is the provider account a PROD environment lives in.
type Account struct {
	Name string `json:"name,omitempty"`
	// Plan is the plan the provider bills the project's compute under, as the
	// provider names it ("Workers Paid"). Empty when the token may not read
	// billing — an unknown plan is said to be unknown, never guessed.
	Plan string `json:"plan,omitempty"`
	// PlanHint says why Plan is empty, in words the owner can act on.
	PlanHint string `json:"plan_hint,omitempty"`
	// Zones are the account's domains and the plan each is on ("Free
	// Website"). Readable with the token the setup asks for, where the
	// Workers plan is not — so the plan question always has an answer.
	Zones []ZonePlan `json:"zones,omitempty"`
}

// ZonePlan is one domain of the account and its Cloudflare plan.
type ZonePlan struct {
	Zone      string  `json:"zone"`
	Plan      string  `json:"plan"`
	Price     float64 `json:"price"`
	Currency  string  `json:"currency,omitempty"`
	Frequency string  `json:"frequency,omitempty"`
}

// ZonePlans lists the account's zones with their plans.
func (c *Client) ZonePlans(ctx context.Context) ([]ZonePlan, error) {
	var zones []struct {
		Name string `json:"name"`
		Plan struct {
			Name      string  `json:"name"`
			Price     float64 `json:"price"`
			Currency  string  `json:"currency"`
			Frequency string  `json:"frequency"`
		} `json:"plan"`
	}
	q := "/zones?per_page=50&account.id=" + url.QueryEscape(c.Creds.AccountID)
	if err := c.do(ctx, http.MethodGet, q, nil, &zones); err != nil {
		return nil, err
	}
	out := make([]ZonePlan, 0, len(zones))
	for _, z := range zones {
		out = append(out, ZonePlan{Zone: z.Name, Plan: z.Plan.Name, Price: z.Plan.Price,
			Currency: z.Plan.Currency, Frequency: z.Plan.Frequency})
	}
	return out, nil
}

// AccountReader is a Provider that can name the account its credentials
// reach — asked of a room before it has declared PROD, so the header can say
// which account the project would deploy into.
type AccountReader interface {
	Account(ctx context.Context) *Account
}

// Account is the Cloudflare account the stored token reaches.
func (d *Cloudflare) Account(ctx context.Context) *Account {
	if ok, _ := d.Connected(); !ok {
		return nil
	}
	if acc := d.Client.AccountInfo(ctx); acc.Name != "" || acc.Plan != "" || len(acc.Zones) > 0 {
		return &acc
	}
	return nil
}

// Account is the Fly organization deploys go to. Its plan is not read: the
// Machines API does not expose it, and a guessed one would be worse than none.
func (f *Fly) Account(ctx context.Context) *Account {
	if ok, _ := f.Connected(); !ok {
		return nil
	}
	return &Account{Name: firstNonEmpty(f.Org, "personal"), PlanHint: "Fly không trả gói qua API"}
}

// accountTTL is how long an account lookup is reused. A plan changes on the
// scale of months; the header asks every minute, for every project room.
const accountTTL = 10 * time.Minute

type cachedAccount struct {
	at  time.Time
	acc Account
}

var (
	accountMu    sync.Mutex
	accountCache = map[string]cachedAccount{}
)

// AccountInfo names the account and its Workers plan, cached per account.
//
// The plan comes from the account's subscriptions. A Workers subscription
// names a paid plan; an account whose subscriptions can be read and hold none
// is on Workers Free. When they cannot be read (a token scoped to Workers and
// D1 only, which is what the setup asks for), the plan stays empty.
func (c *Client) AccountInfo(ctx context.Context) Account {
	if c == nil || !c.Creds.OK() {
		return Account{}
	}
	key := c.Base + "|" + c.Creds.AccountID
	accountMu.Lock()
	if hit, ok := accountCache[key]; ok && time.Since(hit.at) < accountTTL {
		accountMu.Unlock()
		return hit.acc
	}
	accountMu.Unlock()

	var acc Account
	var a struct {
		Name string `json:"name"`
		Type string `json:"type"`
	}
	if err := c.do(ctx, http.MethodGet, c.acct(), nil, &a); err == nil {
		acc.Name = a.Name
		if strings.EqualFold(a.Type, "enterprise") {
			acc.Plan = "Enterprise"
		}
	}
	if acc.Plan == "" {
		var subs []struct {
			RatePlan struct {
				ID         string `json:"id"`
				PublicName string `json:"public_name"`
			} `json:"rate_plan"`
			State string `json:"state"`
		}
		if err := c.do(ctx, http.MethodGet, c.acct()+"/subscriptions", nil, &subs); err != nil {
			acc.PlanHint = "token chưa có quyền Account › Billing › Read"
		} else {
			acc.Plan = "Workers Free"
			for _, s := range subs {
				rp := s.RatePlan
				if s.State != "" && !strings.EqualFold(s.State, "paid") && !strings.EqualFold(s.State, "active") {
					continue
				}
				if strings.Contains(strings.ToLower(rp.ID+" "+rp.PublicName), "workers") {
					acc.Plan = firstNonEmpty(rp.PublicName, "Workers Paid")
					break
				}
			}
		}
	}

	zones, zerr := c.ZonePlans(ctx)
	if zerr == nil {
		acc.Zones = zones
	}

	// Only a complete lookup is kept: a blip on any one of these calls must
	// not pin a half answer — the account without its plans — for ten
	// minutes. It did, the first time the dashboard opened every room at once.
	if acc.Name != "" && zerr == nil {
		accountMu.Lock()
		accountCache[key] = cachedAccount{at: time.Now(), acc: acc}
		accountMu.Unlock()
	}
	return acc
}

// cloudflareServices names the Cloudflare products a project uses, from its
// spec: what it is built on, not what happens to be deployed right now.
func cloudflareServices(r Resolved) []string {
	out := []string{"Workers"}
	if r.Static != "" {
		out = append(out, "Static Assets")
	}
	if r.Database != nil {
		out = append(out, "D1")
	}
	if r.Domain != "" {
		out = append(out, "Custom Domain")
	}
	return out
}

// flyServices is the same for Fly.io.
func flyServices(r Resolved) []string {
	out := []string{"Machines"}
	if r.Database != nil {
		names := map[string]string{"postgres": "Postgres", "sqlite": "SQLite"}
		out = append(out, firstNonEmpty(names[r.Database.Engine], r.Database.Engine))
	}
	if r.Domain != "" {
		out = append(out, "Certificates")
	}
	return out
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}
