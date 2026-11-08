package secrets

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTheStoreKeepsSecretsPrivateAndNeverShowsThem(t *testing.T) {
	path := filepath.Join(t.TempDir(), File)
	s := New(path)
	if err := s.Set("cloudflare", map[string]string{
		"CLOUDFLARE_API_TOKEN": "  cf-token-abcdefghijklmnop ", "CLOUDFLARE_ACCOUNT_ID": "0123456789abcdef0123456789abcdef",
	}, "dashboard"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, %v; want 0600", info.Mode().Perm(), err)
	}
	if got := s.Lookup("CLOUDFLARE_API_TOKEN"); got != "cf-token-abcdefghijklmnop" {
		t.Errorf("lookup = %q", got)
	}

	// An empty value from the form keeps what is stored.
	if err := s.Set("cloudflare", map[string]string{"CLOUDFLARE_API_TOKEN": ""}, "dashboard"); err != nil {
		t.Fatal(err)
	}
	if s.Get("CLOUDFLARE_API_TOKEN") == "" {
		t.Error("an empty field wiped the stored token")
	}

	views, err := s.View()
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range views {
		for _, f := range v.Fields {
			if strings.Contains(f.Masked, "abcdefghijklmnop") {
				t.Errorf("%s is shown unmasked: %q", f.Env, f.Masked)
			}
		}
		if v.ID == "cloudflare" && (!v.Connected || v.Fields[1].Masked != "0123456789abcdef0123456789abcdef") {
			t.Errorf("cloudflare view = %+v", v)
		}
		if v.ID == "fly" && v.Connected {
			t.Error("fly connected with no token")
		}
		if v.ID == "supabase" && v.Connected {
			t.Error("supabase connected with nothing stored")
		}
	}

	if err := s.Set("cloudflare", map[string]string{"NOPE": "x"}, "dashboard"); err == nil {
		t.Error("accepted a field the service does not have")
	}
	if err := s.Set("heroku", map[string]string{"X": "y"}, "dashboard"); err == nil {
		t.Error("accepted an unknown service")
	}
	if err := s.Unset("cloudflare"); err != nil || s.Get("CLOUDFLARE_API_TOKEN") != "" {
		t.Errorf("unset: %v", err)
	}
}

// .env still works: a stored value wins, the environment is the fallback.
func TestTheEnvironmentIsTheFallback(t *testing.T) {
	t.Setenv("FLY_API_TOKEN", "from-env-1234567890")
	s := New(filepath.Join(t.TempDir(), File))
	if s.Lookup("FLY_API_TOKEN") != "from-env-1234567890" {
		t.Error("env fallback missing")
	}
	views, _ := s.View()
	for _, v := range views {
		if v.ID == "fly" && (!v.Connected || v.Fields[0].Source != "env") {
			t.Errorf("fly from env = %+v", v)
		}
	}
	_ = s.Set("fly", map[string]string{"FLY_API_TOKEN": "from-store-0987654321"}, "bomclaw2")
	if s.Lookup("FLY_API_TOKEN") != "from-store-0987654321" {
		t.Error("the store did not win over the environment")
	}
}
