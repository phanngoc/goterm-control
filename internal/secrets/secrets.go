// Package secrets holds the credentials for the infrastructure the agents
// deploy to — Cloudflare, Fly.io, Supabase — entered on the dashboard's
// Connections page or by an agent the owner handed a token to in chat.
//
// Separate from the token store (credentials.TokenStore) on purpose: that one
// is the LLM accounts agents rotate through, a list per provider with turn
// counts. These are a fixed set of named values per service, read by whatever
// deploys — the dashboard, `bomclaw env`, an agent's shell.
//
// Keyed by the environment variable each tool already reads
// (CLOUDFLARE_API_TOKEN, FLY_API_TOKEN, …), so a value here and the same line
// in .env are interchangeable, and Lookup can stand in for os.Getenv. A value
// stored here wins; .env is the fallback, so an existing setup keeps working.
//
// A file beside the coordination database, 0600, written atomically — the
// same reasons as tokens.yaml: live secrets, read by every process on the
// machine, and never in the database every `bomclaw` command opens.
package secrets

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// File is the store's name, beside the coordination database.
const File = "secrets.yaml"

// PathBeside is the store next to a coordination database file.
func PathBeside(coordDB string) string { return filepath.Join(filepath.Dir(coordDB), File) }

// Field is one value a service needs.
type Field struct {
	Env         string `json:"env"`   // the variable name, which is also the key
	Label       string `json:"label"` // what the form calls it
	Secret      bool   `json:"secret"`
	Required    bool   `json:"required"`
	Placeholder string `json:"placeholder,omitempty"`
	Help        string `json:"help,omitempty"`
}

// Service is one thing the agents can connect to.
type Service struct {
	ID      string  `json:"id"`
	Title   string  `json:"title"`
	Purpose string  `json:"purpose"`
	Docs    string  `json:"docs,omitempty"` // where to create the credential
	Fields  []Field `json:"fields"`
	Planned bool    `json:"planned,omitempty"` // storable now, not used by a provider yet
}

// Services is every service the page offers, in display order.
var Services = []Service{
	{
		ID: "cloudflare", Title: "Cloudflare",
		Purpose: "PROD for sites: Workers + static assets, custom domains on your zones, D1 databases.",
		Docs:    "https://dash.cloudflare.com/profile/api-tokens",
		Fields: []Field{
			{Env: "CLOUDFLARE_API_TOKEN", Label: "API token", Secret: true, Required: true,
				Help: "Custom token: Account · Workers Scripts Edit, D1 Edit, Account Settings Read; Zone · Workers Routes Edit, DNS Edit, Zone Read."},
			{Env: "CLOUDFLARE_ACCOUNT_ID", Label: "Account ID", Required: true,
				Placeholder: "32 hex characters", Help: "Dashboard → any zone → right column, or Workers & Pages → Account details."},
		},
	},
	{
		ID: "fly", Title: "Fly.io",
		Purpose: "PROD for apps with a server: Fly Machines from a Docker image, <app>.fly.dev.",
		Docs:    "https://fly.io/docs/security/tokens/",
		Fields: []Field{
			{Env: "FLY_API_TOKEN", Label: "API token", Secret: true, Required: true,
				Help: "fly tokens create org (an org deploy token), or fly auth token."},
			{Env: "FLY_ORG", Label: "Organization", Placeholder: "personal"},
			{Env: "FLY_REGION", Label: "Region", Placeholder: "sin"},
		},
	},
	{
		ID: "supabase", Title: "Supabase", Planned: true,
		Purpose: "The next database engine (Postgres, auth, storage). Stored now; deploys do not use it yet.",
		Docs:    "https://supabase.com/dashboard/account/tokens",
		Fields: []Field{
			{Env: "SUPABASE_ACCESS_TOKEN", Label: "Access token", Secret: true,
				Help: "Account → Access Tokens (for the Management API / supabase CLI)."},
			{Env: "SUPABASE_URL", Label: "Project URL", Placeholder: "https://<ref>.supabase.co"},
			{Env: "SUPABASE_SERVICE_ROLE_KEY", Label: "Service role key", Secret: true},
		},
	},
}

// ServiceByID returns a service definition.
func ServiceByID(id string) (Service, bool) {
	for _, s := range Services {
		if s.ID == id {
			return s, true
		}
	}
	return Service{}, false
}

func fieldOf(svc Service, env string) (Field, bool) {
	for _, f := range svc.Fields {
		if f.Env == env {
			return f, true
		}
	}
	return Field{}, false
}

// Value is one stored value.
type Value struct {
	Value   string    `yaml:"value"`
	Updated time.Time `yaml:"updated"`
	By      string    `yaml:"by,omitempty"` // "dashboard", or the agent that set it
}

// Store reads and writes the file.
type Store struct {
	path string
	mu   sync.Mutex
}

func New(path string) *Store { return &Store{path: path} }

func (s *Store) Path() string { return s.path }

type doc map[string]map[string]Value // service → env → value

func (s *Store) load() (doc, error) {
	raw, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return doc{}, nil
	}
	if err != nil {
		return nil, err
	}
	d := doc{}
	if err := yaml.Unmarshal(raw, &d); err != nil {
		return nil, fmt.Errorf("%s: %w", s.path, err)
	}
	return d, nil
}

func (s *Store) save(d doc) error {
	data, err := yaml.Marshal(d)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".secrets-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	header := "# Infrastructure credentials (dashboard → Admin → Connections, or `bomclaw secrets`). Live secrets: keep 0600.\n"
	if _, err := tmp.WriteString(header + string(data)); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.path)
}

// Set stores values for one service. An empty value leaves the stored one as
// it is — the page never has a secret to send back — and Unset removes one.
func (s *Store) Set(service string, values map[string]string, by string) error {
	svc, ok := ServiceByID(service)
	if !ok {
		return fmt.Errorf("no service %q (known: %s)", service, strings.Join(IDs(), ", "))
	}
	for env := range values {
		if _, ok := fieldOf(svc, env); !ok {
			return fmt.Errorf("%s has no field %s (fields: %s)", svc.Title, env, strings.Join(envs(svc), ", "))
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.load()
	if err != nil {
		return err
	}
	if d[service] == nil {
		d[service] = map[string]Value{}
	}
	now := time.Now().UTC().Truncate(time.Second)
	for env, v := range values {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		d[service][env] = Value{Value: v, Updated: now, By: by}
	}
	return s.save(d)
}

// Unset removes values; no envs removes the whole service.
func (s *Store) Unset(service string, envs ...string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.load()
	if err != nil {
		return err
	}
	if len(envs) == 0 {
		delete(d, service)
	} else {
		for _, e := range envs {
			delete(d[service], e)
		}
		if len(d[service]) == 0 {
			delete(d, service)
		}
	}
	return s.save(d)
}

// Get is a stored value, "" when none.
func (s *Store) Get(env string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.load()
	if err != nil {
		return ""
	}
	for _, vals := range d {
		if v, ok := vals[env]; ok {
			return v.Value
		}
	}
	return ""
}

// Lookup is os.Getenv with the store in front of it.
func (s *Store) Lookup(env string) string {
	if v := s.Get(env); v != "" {
		return v
	}
	return strings.TrimSpace(os.Getenv(env))
}

// FieldView is one field as the page sees it: never the value, only a mask
// and where it comes from.
type FieldView struct {
	Field
	Set     bool   `json:"set"`
	Source  string `json:"source,omitempty"` // "store" | "env"
	Masked  string `json:"masked,omitempty"`
	Updated string `json:"updated,omitempty"`
	By      string `json:"by,omitempty"`
}

// ServiceView is one service with its fields' state.
type ServiceView struct {
	Service
	Fields    []FieldView `json:"fields"`
	Connected bool        `json:"connected"` // every required field has a value
}

// View is every service, for the page and `bomclaw secrets list`.
func (s *Store) View() ([]ServiceView, error) {
	s.mu.Lock()
	d, err := s.load()
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	out := make([]ServiceView, 0, len(Services))
	for _, svc := range Services {
		v := ServiceView{Service: svc, Connected: true}
		any := false
		for _, f := range svc.Fields {
			fv := FieldView{Field: f}
			if val, ok := d[svc.ID][f.Env]; ok && val.Value != "" {
				fv.Set, fv.Source, fv.By = true, "store", val.By
				fv.Updated = val.Updated.Format(time.RFC3339)
				fv.Masked = mask(val.Value, f.Secret)
			} else if env := strings.TrimSpace(os.Getenv(f.Env)); env != "" {
				fv.Set, fv.Source = true, "env"
				fv.Masked = mask(env, f.Secret)
			}
			if f.Required && !fv.Set {
				v.Connected = false
			}
			any = any || fv.Set
			v.Fields = append(v.Fields, fv)
		}
		// A service with no required field is connected once something is in it.
		v.Connected = v.Connected && any
		out = append(out, v)
	}
	return out, nil
}

// mask shows enough of a secret to tell two apart, never enough to use one.
// Non-secret fields (an account id, a region) are shown whole.
func mask(v string, secret bool) string {
	if !secret {
		return v
	}
	r := []rune(v)
	if len(r) <= 8 {
		return strings.Repeat("•", len(r))
	}
	return string(r[:4]) + "…" + string(r[len(r)-4:])
}

// IDs lists the services.
func IDs() []string {
	out := make([]string, 0, len(Services))
	for _, s := range Services {
		out = append(out, s.ID)
	}
	sort.Strings(out)
	return out
}

func envs(svc Service) []string {
	out := make([]string, 0, len(svc.Fields))
	for _, f := range svc.Fields {
		out = append(out, f.Env)
	}
	return out
}
