package browserbridge

import (
	"fmt"
	"net/url"
	"strings"
)

// CheckNavigateURL decides whether the agent may open rawURL in the user's
// browser. Only http(s) pages (and about:blank) qualify: file://, chrome://,
// javascript: and data: URLs are how a prompt-injected agent would reach the
// user's disk or the browser's own settings, and none of them is a web page
// the user asked to have worked on. blockedHosts adds sites the operator has
// ruled out entirely; "*.example.com" matches every subdomain.
func CheckNavigateURL(rawURL string, blockedHosts []string) error {
	raw := strings.TrimSpace(rawURL)
	if raw == "" {
		return fmt.Errorf("url is required")
	}
	if strings.EqualFold(raw, "about:blank") {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid url %q: %v", raw, err)
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
	case "":
		return fmt.Errorf("url %q has no scheme — write it as https://%s", raw, raw)
	default:
		return fmt.Errorf("only http(s) pages can be opened in the user's browser (got %s:)", u.Scheme)
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return fmt.Errorf("url %q has no host", raw)
	}
	for _, pattern := range blockedHosts {
		if hostMatches(host, pattern) {
			return fmt.Errorf("%s is blocked by config (browser.extension.blocked_hosts)", host)
		}
	}
	return nil
}

// hostMatches compares a lowercase hostname with a config pattern: an exact
// host, or "*.suffix" for the suffix and everything under it.
func hostMatches(host, pattern string) bool {
	p := strings.ToLower(strings.TrimSpace(pattern))
	if p == "" {
		return false
	}
	if strings.HasPrefix(p, "*.") {
		suffix := p[1:] // ".example.com"
		return host == p[2:] || strings.HasSuffix(host, suffix)
	}
	return host == p
}
