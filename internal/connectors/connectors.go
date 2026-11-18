// Package connectors is what a project can plug into — Stripe, Resend,
// Gmail, OpenAI… — the way Lovable's Connectors are: connect a service once
// for the machine, enable it on a project, and the project's code gets its
// keys by name, in Preview and in PROD, without anyone pasting a key into the
// code.
//
// Three layers, each one place:
//
//   - The keys live in the secrets store (secrets.yaml), beside the
//     infrastructure credentials: every connector registers itself there as a
//     service with a Category, so `bomclaw secrets` and the dashboard read and
//     write the same file, and a value in .env still works as a fallback.
//   - Which connectors a project uses is in its bomclaw.json, next to
//     "preview" and "deploy": {"connectors": ["stripe", "resend"]}. Written by
//     the dashboard's toggle and by an agent's connector_enable alike.
//   - Who reads them: Preview starts the dev server with them in its
//     environment, Publish hands them to the PROD Worker or Fly app as secrets
//     (public ones also to the frontend build), and `bomclaw connectors exec`
//     runs a command with them — so an agent can test code that calls Stripe
//     without the key ever being printed into its context.
//
// Everything can be done from chat: the agent has MCP tools (`bomclaw mcp
// connectors`) to list, connect with a key the person pasted, check, enable,
// and read how to use a connector in code.
package connectors

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/ngocp/goterm-control/internal/secrets"
)

// Auth kinds.
const (
	AuthKey   = "key"   // values typed in (API keys, ids)
	AuthOAuth = "oauth" // a refresh token obtained by signing in (Google)
)

// Category is a catalog section, Lovable's left rail.
type Category struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Blurb string `json:"blurb"`
}

// Categories in display order.
var Categories = []Category{
	{ID: "grow", Title: "Grow", Blurb: "Được tìm thấy và phát triển: analytics, SEO, bản đồ"},
	{ID: "sell", Title: "Sell", Blurb: "Bán hàng và nhận tiền"},
	{ID: "communicate", Title: "Communicate", Blurb: "Email, tin nhắn, thông báo"},
	{ID: "work", Title: "Work", Blurb: "Dữ liệu và công cụ làm việc"},
	{ID: "ai", Title: "AI", Blurb: "Mô hình AI, giọng nói, crawl web"},
}

// Connector is one service a project can use.
type Connector struct {
	secrets.Service
	// Short is the one-liner on the card.
	Short string `json:"short"`
	// Icon is a simpleicons.org slug; Color its brand color (hex, no #),
	// used for the fallback badge when the icon cannot load.
	Icon  string `json:"icon,omitempty"`
	Color string `json:"color,omitempty"`
	// Popular puts it in the Home page's first section.
	Popular bool   `json:"popular,omitempty"`
	Auth    string `json:"auth"`
	// Requires names other services whose values the project also gets — a
	// Google connector needs the OAuth client (google-oauth) beside its own
	// refresh token.
	Requires []string `json:"requires,omitempty"`
	// Hidden is a service that is set up but not enabled on its own (the
	// Google OAuth client): no card, shown inside the connectors needing it.
	Hidden bool `json:"hidden,omitempty"`
	// Scopes are what an OAuth connector asks Google for.
	Scopes []string `json:"scopes,omitempty"`
	// Guide is how to use it in code, for the agent (connector_guide).
	Guide string `json:"-"`

	verify func(ctx context.Context, get func(string) string) (string, error)
}

var catalog []*Connector

func register(c *Connector) {
	if c.Auth == "" {
		c.Auth = AuthKey
	}
	catalog = append(catalog, c)
	secrets.Register(c.Service)
}

func init() {
	for _, c := range builtin() {
		register(c)
	}
}

// All is every connector, hidden ones included, in catalog order.
func All() []*Connector { return catalog }

// ByID finds a connector. Accepts a title too ("Google Sheets").
func ByID(id string) (*Connector, bool) {
	id = strings.TrimSpace(id)
	for _, c := range catalog {
		if c.ID == id {
			return c, true
		}
	}
	norm := func(s string) string {
		return strings.NewReplacer(" ", "", "-", "", "_", "", ".", "").Replace(strings.ToLower(s))
	}
	for _, c := range catalog {
		if norm(c.ID) == norm(id) || norm(c.Title) == norm(id) {
			return c, true
		}
	}
	return nil, false
}

// IDs lists the connectors that can be enabled, sorted.
func IDs() []string {
	var out []string
	for _, c := range catalog {
		if !c.Hidden {
			out = append(out, c.ID)
		}
	}
	sort.Strings(out)
	return out
}

// unknown is the error for a name not in the catalog.
func unknown(id string) error {
	return fmt.Errorf("no connector %q (known: %s)", id, strings.Join(IDs(), ", "))
}

// Envs are the variable names a project gets from this connector, its
// requirements' first.
func (c *Connector) Envs() []secrets.Field {
	var out []secrets.Field
	for _, r := range c.Requires {
		if rc, ok := ByID(r); ok {
			out = append(out, rc.Fields...)
		}
	}
	return append(out, c.Fields...)
}
