package deploy

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// A domain for a project on AWS or Vercel is still, most of the time, a name
// on one of the Cloudflare zones: bomclaw.org is there. So when the
// Cloudflare token can reach the zone, the records the other provider asks
// for — its CNAME, its certificate's validation record — are written here,
// and the publish finishes on its own instead of handing the owner a DNS
// chore. When it cannot (another DNS host, a token without DNS Edit), the
// deploy log says exactly which records to add.

// DNSRecord is one record another provider needs.
type DNSRecord struct {
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
}

func (r DNSRecord) String() string { return fmt.Sprintf("%s %s → %s", r.Type, r.Name, r.Content) }

// zoneFor is the Cloudflare zone holding host: the longest zone name host
// ends with.
func (c *Client) zoneFor(ctx context.Context, host string) (id, name string, err error) {
	var zones []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	q := "/zones?per_page=50&account.id=" + url.QueryEscape(c.Creds.AccountID)
	if err := c.do(ctx, http.MethodGet, q, nil, &zones); err != nil {
		return "", "", err
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	for _, z := range zones {
		if (host == z.Name || strings.HasSuffix(host, "."+z.Name)) && len(z.Name) > len(name) {
			id, name = z.ID, z.Name
		}
	}
	if id == "" {
		return "", "", fmt.Errorf("%s is not on any zone of the Cloudflare account", host)
	}
	return id, name, nil
}

// UpsertDNS writes rec on the zone that holds its name, replacing a record
// of the same type and name. DNS only (never proxied): the other provider
// terminates TLS itself and must see its own validation traffic.
func (c *Client) UpsertDNS(ctx context.Context, rec DNSRecord) error {
	if c == nil || !c.Creds.OK() {
		return ErrNoCreds
	}
	name := strings.TrimSuffix(rec.Name, ".")
	zid, _, err := c.zoneFor(ctx, name)
	if err != nil {
		return err
	}
	var existing []struct {
		ID      string `json:"id"`
		Content string `json:"content"`
	}
	path := "/zones/" + url.PathEscape(zid) + "/dns_records"
	if err := c.do(ctx, http.MethodGet, path+"?type="+url.QueryEscape(rec.Type)+"&name="+url.QueryEscape(name), nil, &existing); err != nil {
		return err
	}
	body := map[string]any{"type": rec.Type, "name": name, "content": strings.TrimSuffix(rec.Content, "."), "ttl": 1, "proxied": false}
	if len(existing) > 0 {
		if strings.EqualFold(strings.TrimSuffix(existing[0].Content, "."), strings.TrimSuffix(rec.Content, ".")) {
			return nil
		}
		return c.do(ctx, http.MethodPut, path+"/"+url.PathEscape(existing[0].ID), body, nil)
	}
	return c.do(ctx, http.MethodPost, path, body, nil)
}

// pointDNS writes each record through Cloudflare when it can, and otherwise
// logs what the owner has to add. It reports whether all were written.
func pointDNS(ctx context.Context, recs []DNSRecord, step func(string, ...any)) bool {
	if len(recs) == 0 {
		return true
	}
	cf := NewClient(CredsFromEnv())
	all := true
	for _, r := range recs {
		if !cf.Creds.OK() {
			step("dns: add %s (Cloudflare is not connected, so it was not written)", r)
			all = false
			continue
		}
		if err := cf.UpsertDNS(ctx, r); err != nil {
			step("dns: add %s by hand — Cloudflare: %v", r, err)
			all = false
			continue
		}
		step("dns: %s (Cloudflare)", r)
	}
	return all
}
