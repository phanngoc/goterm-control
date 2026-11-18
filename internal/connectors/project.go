package connectors

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ngocp/goterm-control/internal/secrets"
)

// Manifest is the project file connectors are declared in (with "preview"
// and "deploy").
const Manifest = "bomclaw.json"

// Enabled is the connectors a project declares, in its bomclaw.json order.
// No file or no key: none.
func Enabled(projectDir string) ([]string, error) {
	raw, err := os.ReadFile(filepath.Join(projectDir, Manifest))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var mf struct {
		Connectors []string `json:"connectors"`
	}
	if err := json.Unmarshal(raw, &mf); err != nil {
		return nil, fmt.Errorf("%s: %w", Manifest, err)
	}
	return mf.Connectors, nil
}

// SetEnabled turns a connector on or off for a project, keeping every other
// key of bomclaw.json. Reports whether anything changed.
func SetEnabled(projectDir, id string, on bool) (bool, error) {
	c, ok := ByID(id)
	if !ok {
		return false, unknown(id)
	}
	if c.Hidden {
		return false, fmt.Errorf("%s is set up once for the machine, not enabled on a project", c.Title)
	}
	list, err := Enabled(projectDir)
	if err != nil {
		return false, err
	}
	has := false
	out := list[:0:0]
	for _, x := range list {
		if x == c.ID {
			has = true
			if !on {
				continue
			}
		}
		out = append(out, x)
	}
	if has == on {
		return false, nil
	}
	if on {
		out = append(out, c.ID)
	}
	return true, writeList(projectDir, out)
}

func writeList(projectDir string, list []string) error {
	path := filepath.Join(projectDir, Manifest)
	doc := map[string]json.RawMessage{}
	if raw, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(raw, &doc); err != nil {
			return fmt.Errorf("%s is not a JSON object, not touching it: %w", Manifest, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if len(list) == 0 {
		delete(doc, "connectors")
	} else {
		b, _ := json.Marshal(list)
		doc["connectors"] = b
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(out, '\n'), 0o644)
}

// Var is one variable a project's code gets.
type Var struct {
	Name, Value string
	Public      bool
	Connector   string
}

// ProjectEnv is every value a project's enabled connectors give its code,
// and the connectors that cannot give theirs yet (not connected) with why.
// get reads a value: the secrets store, then the environment.
func ProjectEnv(projectDir string, get func(string) string) ([]Var, map[string]string, error) {
	ids, err := Enabled(projectDir)
	if err != nil {
		return nil, nil, err
	}
	var vars []Var
	missing := map[string]string{}
	seen := map[string]bool{}
	for _, id := range ids {
		c, ok := ByID(id)
		if !ok {
			missing[id] = "not a known connector"
			continue
		}
		for _, f := range c.Envs() {
			v := get(f.Env)
			if v == "" {
				if f.Required {
					missing[c.ID] = f.Env + " is not set"
				}
				continue
			}
			if seen[f.Env] {
				continue
			}
			seen[f.Env] = true
			vars = append(vars, Var{Name: f.Env, Value: v, Public: f.Public, Connector: c.ID})
		}
	}
	return vars, missing, nil
}

// browserPrefixes are how frontend build tools pick which variables reach the
// browser bundle. Only public values are given these names.
var browserPrefixes = []string{"VITE_", "NEXT_PUBLIC_"}

// ServerEnv is NAME=value for a process that runs the project: every value
// under its own name, and the public ones under the frontend names too (a dev
// server builds the frontend as it serves it).
func ServerEnv(vars []Var) []string {
	var out []string
	for _, v := range vars {
		out = append(out, v.Name+"="+v.Value)
	}
	return append(out, BuildEnv(vars)...)
}

// BuildEnv is what a frontend build gets: the public values, under their own
// names and the frontend names. Never a secret — a build's output is public.
func BuildEnv(vars []Var) []string {
	var out []string
	for _, v := range vars {
		if !v.Public {
			continue
		}
		out = append(out, v.Name+"="+v.Value)
		for _, p := range browserPrefixes {
			out = append(out, p+v.Name+"="+v.Value)
		}
	}
	return out
}

// Fingerprint changes when the values a project gets change — a running
// Preview started with another set needs a restart to see them.
func Fingerprint(vars []Var) string {
	parts := make([]string, 0, len(vars))
	for _, v := range vars {
		parts = append(parts, v.Name+"\x00"+v.Value)
	}
	sort.Strings(parts)
	return hash(strings.Join(parts, "\x01"))
}

// View is a connector as the page and the tools show it.
type View struct {
	*Connector
	Fields    []secrets.FieldView `json:"fields"`
	Connected bool                `json:"connected"`
	// Enabled on the project asked about; Missing says why an enabled one
	// gives the project nothing yet.
	Enabled bool `json:"enabled"`
	// Ready: connected and, for an OAuth connector, its client set up too.
	Ready bool `json:"ready"`
}

// Views is the catalog with each connector's state, for one project when
// projectDir is not "".
func Views(store *secrets.Store, projectDir string) ([]View, error) {
	sv, err := store.View()
	if err != nil {
		return nil, err
	}
	byID := map[string]secrets.ServiceView{}
	for _, s := range sv {
		byID[s.ID] = s
	}
	on := map[string]bool{}
	if projectDir != "" {
		ids, err := Enabled(projectDir)
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			on[id] = true
		}
	}
	out := make([]View, 0, len(catalog))
	for _, c := range catalog {
		s := byID[c.ID]
		v := View{Connector: c, Fields: s.Fields, Connected: s.Connected, Enabled: on[c.ID]}
		v.Ready = v.Connected
		for _, r := range c.Requires {
			if !byID[r].Connected {
				v.Ready = false
			}
		}
		out = append(out, v)
	}
	return out, nil
}
