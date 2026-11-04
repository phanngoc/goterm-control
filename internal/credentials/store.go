package credentials

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// The token store: credentials managed from the dashboard instead of
// config.yaml, one list per provider, shared by every agent on the machine
// that runs that provider.
//
// A file, not the coordination database, and 0600: these are live secrets,
// and the database is read by every `bomclaw` an agent runs. It sits next to
// the database because that is the one directory every gateway and the
// dashboard already agree on.
//
// Gateways re-read it when it changes (an mtime check per turn), so adding a
// token from the dashboard reaches the next turn of every agent without a
// restart.

// TokenFile is the store's file name, beside the coordination database.
const TokenFile = "tokens.yaml"

// TokenKind is what a stored credential is, which decides how the CLI gets it.
type TokenKind string

const (
	// KindSetupToken is a claude `claude setup-token` token →
	// CLAUDE_CODE_OAUTH_TOKEN.
	KindSetupToken TokenKind = "setup_token"
	// KindAccessToken is a codex ChatGPT access token → CODEX_ACCESS_TOKEN
	// (what `codex login --with-access-token` takes).
	KindAccessToken TokenKind = "access_token"
	// KindAPIKey is a plain API key → ANTHROPIC_API_KEY for claude,
	// CODEX_API_KEY for codex.
	KindAPIKey TokenKind = "api_key"
)

// StoredToken is one credential in the store.
type StoredToken struct {
	Name    string    `yaml:"name" json:"name"`
	Kind    TokenKind `yaml:"kind" json:"kind"`
	Value   string    `yaml:"value" json:"-"`
	Rotate  bool      `yaml:"rotate" json:"rotate"`
	Note    string    `yaml:"note,omitempty" json:"note,omitempty"`
	Added   time.Time `yaml:"added" json:"added"`
	Updated time.Time `yaml:"updated" json:"updated"`
}

// ProviderTokens is one provider's section of the store.
type ProviderTokens struct {
	// RotateEvery, when > 0, overrides accounts.rotate_every for this
	// provider's agents: each rotating token serves this many turns.
	RotateEvery int           `yaml:"rotate_every" json:"rotate_every"`
	Tokens      []StoredToken `yaml:"tokens" json:"tokens"`
}

// TokenStore reads and writes the store file.
type TokenStore struct {
	path string
	mu   sync.Mutex
}

// TokenStorePath is the store beside a coordination database.
func TokenStorePath(coordDB string) string {
	return filepath.Join(filepath.Dir(coordDB), TokenFile)
}

// NewTokenStore returns the store at path. A missing file is an empty store.
func NewTokenStore(path string) *TokenStore { return &TokenStore{path: path} }

// Path is where the store lives.
func (s *TokenStore) Path() string { return s.path }

// Load returns every provider's tokens.
func (s *TokenStore) Load() (map[string]*ProviderTokens, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load()
}

func (s *TokenStore) load() (map[string]*ProviderTokens, error) {
	out := map[string]*ProviderTokens{}
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return nil, fmt.Errorf("credentials: %w", err)
	}
	if err := yaml.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("credentials: %s: %w", s.path, err)
	}
	for k, v := range out {
		if v == nil {
			out[k] = &ProviderTokens{}
		}
	}
	return out, nil
}

func (s *TokenStore) save(all map[string]*ProviderTokens) error {
	data, err := yaml.Marshal(all)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	// Atomic: every gateway reads this file before every turn.
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".tokens-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	header := "# Managed by the dashboard (Admin → Accounts). Live secrets: keep 0600.\n"
	if _, err := tmp.WriteString(header + string(data)); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.path)
}

var tokenNameOK = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,47}$`)

// TokenInput is a create-or-update. Value empty on an update keeps the old
// one: the dashboard never has it to send back.
type TokenInput struct {
	Provider string    `json:"provider"`
	Name     string    `json:"name"`
	OldName  string    `json:"old_name,omitempty"` // set to rename
	Kind     TokenKind `json:"kind"`
	Value    string    `json:"value,omitempty"`
	Rotate   bool      `json:"rotate"`
	Note     string    `json:"note,omitempty"`
}

// Save creates or updates one token.
func (s *TokenStore) Save(in TokenInput) error {
	if err := validTokenInput(in); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.load()
	if err != nil {
		return err
	}
	pt := all[in.Provider]
	if pt == nil {
		pt = &ProviderTokens{}
		all[in.Provider] = pt
	}
	// An edit names the token it edits (OldName, equal to Name when it is
	// not being renamed); without one this is a create, and a create never
	// overwrites — a second "a" typed by mistake must not replace the first.
	key := in.OldName
	if key == "" {
		key = in.Name
	}
	now := time.Now().UTC().Truncate(time.Second)
	idx := -1
	for i, t := range pt.Tokens {
		if t.Name == key {
			idx = i
		}
		if t.Name == in.Name && in.Name != key {
			return fmt.Errorf("credentials: %s already has a token named %q", in.Provider, in.Name)
		}
	}
	value := strings.TrimSpace(in.Value)
	if idx >= 0 && in.OldName == "" {
		return fmt.Errorf("credentials: %s already has a token named %q", in.Provider, in.Name)
	}
	if idx < 0 {
		if in.OldName != "" {
			return fmt.Errorf("credentials: no %s token named %q", in.Provider, in.OldName)
		}
		for _, t := range pt.Tokens {
			if t.Name == in.Name {
				return fmt.Errorf("credentials: %s already has a token named %q", in.Provider, in.Name)
			}
		}
		if value == "" {
			return fmt.Errorf("credentials: a new token needs its value")
		}
		pt.Tokens = append(pt.Tokens, StoredToken{Name: in.Name, Kind: in.Kind, Value: value,
			Rotate: in.Rotate, Note: in.Note, Added: now, Updated: now})
	} else {
		t := &pt.Tokens[idx]
		t.Name, t.Kind, t.Rotate, t.Note, t.Updated = in.Name, in.Kind, in.Rotate, in.Note, now
		if value != "" {
			t.Value = value
		}
	}
	return s.save(all)
}

// Delete removes one token.
func (s *TokenStore) Delete(provider, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.load()
	if err != nil {
		return err
	}
	pt := all[provider]
	if pt == nil {
		return fmt.Errorf("credentials: no %s token named %q", provider, name)
	}
	kept := pt.Tokens[:0]
	found := false
	for _, t := range pt.Tokens {
		if t.Name == name {
			found = true
			continue
		}
		kept = append(kept, t)
	}
	if !found {
		return fmt.Errorf("credentials: no %s token named %q", provider, name)
	}
	pt.Tokens = kept
	return s.save(all)
}

// SetRotateEvery sets a provider's rotation length; 0 defers to config.
func (s *TokenStore) SetRotateEvery(provider string, n int) error {
	if !knownProvider(provider) {
		return fmt.Errorf("credentials: unknown provider %q", provider)
	}
	if n < 0 || n > 1000 {
		return fmt.Errorf("credentials: rotate_every must be between 0 and 1000")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.load()
	if err != nil {
		return err
	}
	if all[provider] == nil {
		all[provider] = &ProviderTokens{}
	}
	all[provider].RotateEvery = n
	return s.save(all)
}

func knownProvider(p string) bool { return p == ProviderClaude || p == ProviderCodex }

func validTokenInput(in TokenInput) error {
	if !knownProvider(in.Provider) {
		return fmt.Errorf("credentials: unknown provider %q (claude or codex)", in.Provider)
	}
	if !tokenNameOK.MatchString(in.Name) {
		return fmt.Errorf("credentials: %q — lowercase letters, digits, '.', '-' and '_'", in.Name)
	}
	if strings.HasPrefix(in.Name, "@") {
		return fmt.Errorf("credentials: names starting with @ are reserved")
	}
	switch {
	case in.Provider == ProviderClaude && (in.Kind == KindSetupToken || in.Kind == KindAPIKey):
	case in.Provider == ProviderCodex && (in.Kind == KindAccessToken || in.Kind == KindAPIKey):
	default:
		return fmt.Errorf("credentials: %s does not take a %q", in.Provider, in.Kind)
	}
	if strings.ContainsAny(in.Value, "\r\n") {
		return fmt.Errorf("credentials: a token is one line")
	}
	return nil
}

// Mask shows enough of a secret to tell two apart, and nothing more.
func Mask(v string) string {
	v = strings.TrimSpace(v)
	if len(v) <= 8 {
		return "••••"
	}
	prefix := ""
	if i := strings.Index(v, "-"); i > 0 && i <= 12 {
		// Keep a recognisable prefix such as "sk-ant-oat01" — it says what
		// kind of credential this is, not which one.
		parts := strings.SplitN(v, "-", 4)
		if len(parts) >= 3 {
			prefix = strings.Join(parts[:3], "-") + "-"
		}
	}
	return prefix + "…" + v[len(v)-4:]
}

// accounts turns one provider's stored tokens into pool accounts.
func (pt *ProviderTokens) accounts(provider string) []Account {
	if pt == nil {
		return nil
	}
	out := make([]Account, 0, len(pt.Tokens))
	for _, t := range pt.Tokens {
		a := Account{Name: t.Name, Provider: provider, NoRotate: !t.Rotate, managed: true}
		switch t.Kind {
		case KindAPIKey:
			a.APIKey = t.Value
		default:
			a.OAuthToken = t.Value
		}
		out = append(out, a)
	}
	return out
}
