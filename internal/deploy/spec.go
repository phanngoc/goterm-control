// Package deploy is a project's PROD environment: the same project a channel
// previews locally (DEV, bomclaw.json "preview"), published to Cloudflare.
//
// The shape is Lovable's: you build and look at it in one place, and Publish
// pushes a snapshot to a public URL — manually, so a half-finished edit never
// reaches anyone. Here DEV is the dashboard's Preview (local, behind the
// login) and PROD is a Cloudflare Worker serving the built static files, on a
// workers.dev address and optionally a domain on one of your Cloudflare zones,
// with an optional D1 database uploaded from the project's local SQLite file.
//
// Declared in the project's bomclaw.json, next to "preview":
//
//	{
//	  "preview": {"command": "npm run dev -- --port $PORT --host $HOST"},
//	  "deploy": {
//	    "target": "cloudflare",
//	    "build": "npm run build",
//	    "static": "dist",
//	    "domain": "phim.bomclaw.org",
//	    "database": {"engine": "sqlite", "file": "data/app.db"}
//	  }
//	}
//
// Supabase is the next database engine; "engine" exists so that adding it
// does not change the shape.
package deploy

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Manifest is the project file both environments are declared in.
const Manifest = "bomclaw.json"

// Spec is bomclaw.json's "deploy" object.
type Spec struct {
	// Target is the provider PROD lives on (see Provider): "cloudflare"
	// today, "fly" planned.
	Target string `json:"target"`
	// Name is the Worker's name, which is also its workers.dev subdomain.
	// Default: the channel's id without "ch_", made DNS-safe.
	Name string `json:"name,omitempty"`
	// Build runs in the project before anything is uploaded.
	Build string `json:"build,omitempty"`
	// Cwd is where Build runs and Static is found, relative to the project.
	Cwd string `json:"cwd,omitempty"`
	// Static is the folder of built files to serve.
	Static string `json:"static"`
	// SPA serves index.html for unknown paths, for client-side routers.
	SPA bool `json:"spa,omitempty"`
	// Main is an optional Worker script (an API in front of the files).
	// Without it the Worker serves files only.
	Main string `json:"main,omitempty"`
	// Domain is a hostname on one of your Cloudflare zones. Optional: every
	// Worker also answers on <name>.<account>.workers.dev.
	Domain string `json:"domain,omitempty"`
	// Database is uploaded only when asked (deploy --with-db): it REPLACES
	// the production tables.
	Database *Database `json:"database,omitempty"`
}

// Database is the project's data, local in DEV and hosted in PROD.
type Database struct {
	// Engine: "sqlite" — a local SQLite file, hosted as Cloudflare D1.
	// "supabase" is reserved for the Postgres path.
	Engine string `json:"engine"`
	// File is the DEV database, relative to the project.
	File string `json:"file"`
	// Name is the D1 database. Default: <worker name>-db.
	Name string `json:"name,omitempty"`
	// Binding is what a Main script calls it (env.DB). Default "DB".
	Binding string `json:"binding,omitempty"`
}

var dnsUnsafe = regexp.MustCompile(`[^a-z0-9-]+`)

// DefaultName is a channel id as a Worker name: lowercase, DNS-safe, ≤ 63.
func DefaultName(channelID string) string {
	n := strings.ToLower(strings.TrimPrefix(channelID, "ch_"))
	n = strings.Trim(dnsUnsafe.ReplaceAllString(n, "-"), "-")
	if len(n) > 63 {
		n = strings.Trim(n[:63], "-")
	}
	if n == "" {
		n = "project"
	}
	return n
}

// Read returns the project's deploy spec, or nil when it declares none.
func Read(projectDir string) (*Spec, error) {
	raw, err := os.ReadFile(filepath.Join(projectDir, Manifest))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return Parse(raw)
}

// Parse reads bomclaw.json's bytes. nil, nil: no deploy declared.
func Parse(raw []byte) (*Spec, error) {
	var mf struct {
		Deploy *Spec `json:"deploy"`
	}
	if err := json.Unmarshal(raw, &mf); err != nil {
		return nil, fmt.Errorf("%s: %w", Manifest, err)
	}
	s := mf.Deploy
	if s == nil {
		return nil, nil
	}
	if s.Target == "" {
		s.Target = DefaultTarget
	}
	prov, err := For(s.Target)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", Manifest, err)
	}
	if s.Static == "" && s.Main == "" {
		return nil, fmt.Errorf("%s: deploy needs \"static\" (the built folder) or \"main\" (a Worker script)", Manifest)
	}
	if s.Name != "" && DefaultName(s.Name) != s.Name {
		return nil, fmt.Errorf("%s: deploy.name %q must be lowercase letters, digits and dashes", Manifest, s.Name)
	}
	if err := prov.Validate(s); err != nil {
		return nil, fmt.Errorf("%s: %w", Manifest, err)
	}
	return s, nil
}

// Write sets bomclaw.json's "deploy" and keeps every other key.
func Write(projectDir string, s *Spec) error {
	path := filepath.Join(projectDir, Manifest)
	doc := map[string]json.RawMessage{}
	if raw, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(raw, &doc); err != nil {
			return fmt.Errorf("%s is not a JSON object, not touching it: %w", Manifest, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	doc["deploy"] = b
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(out, '\n'), 0o644)
}

// Resolved is a spec with its defaults filled in and paths made absolute.
type Resolved struct {
	Spec
	ProjectDir string
	WorkDir    string // ProjectDir/Cwd
	StaticDir  string // absolute; "" without Static
	MainPath   string // absolute; "" without Main
	DBFile     string // absolute; "" without a database
	DBName     string
	DBBinding  string
}

// Resolve fills defaults for the channel whose project this is.
func (s *Spec) Resolve(projectDir, channelID string) Resolved {
	r := Resolved{Spec: *s, ProjectDir: projectDir}
	if r.Name == "" {
		r.Name = DefaultName(channelID)
	}
	r.WorkDir = filepath.Join(projectDir, s.Cwd)
	if s.Static != "" {
		r.StaticDir = filepath.Join(r.WorkDir, s.Static)
	}
	if s.Main != "" {
		r.MainPath = filepath.Join(r.WorkDir, s.Main)
	}
	if d := s.Database; d != nil {
		r.DBFile = filepath.Join(projectDir, d.File)
		r.DBName = d.Name
		if r.DBName == "" {
			r.DBName = r.Name + "-db"
		}
		r.DBBinding = d.Binding
		if r.DBBinding == "" {
			r.DBBinding = "DB"
		}
	}
	return r
}
