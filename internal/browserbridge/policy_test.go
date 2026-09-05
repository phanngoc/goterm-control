package browserbridge

import (
	"strings"
	"testing"
)

func TestCheckNavigateURL(t *testing.T) {
	blocked := []string{"*.bank.example", "intranet.corp"}
	cases := []struct {
		url  string
		want string // "" = allowed, else substring of the error
	}{
		{"https://example.com/page", ""},
		{"http://localhost:3000", ""},
		{"HTTPS://EXAMPLE.COM", ""},
		{"about:blank", ""},
		{"", "required"},
		{"example.com", "no scheme"},
		{"file:///etc/passwd", "only http(s)"},
		{"chrome://settings", "only http(s)"},
		{"javascript:alert(1)", "only http(s)"},
		{"data:text/html,hi", "only http(s)"},
		{"https://bank.example", "blocked"},
		{"https://online.bank.example/login", "blocked"},
		{"https://notbank.example", ""},
		{"https://intranet.corp/", "blocked"},
		{"https://intranet.corp.example/", ""},
	}
	for _, c := range cases {
		err := CheckNavigateURL(c.url, blocked)
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%q: unexpected error %v", c.url, err)
		case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%q: got %v, want error containing %q", c.url, err, c.want)
		}
	}
}
