package gateway

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ngocp/goterm-control/internal/deploy"
	"github.com/ngocp/goterm-control/internal/secrets"
)

// The Connections page: the credentials for the infrastructure agents deploy
// to. Answered by the dashboard process. A value never leaves the server —
// the page gets a mask and where the value came from (the store or .env), and
// a save with an empty field keeps what is stored.

func secretsStore(deps Deps) (*secrets.Store, error) {
	if deps.Coord == nil {
		return nil, errNoCoord()
	}
	return secrets.New(secrets.PathBeside(deps.Coord.Path())), nil
}

type secretsParams struct {
	Service string            `json:"service"`
	Values  map[string]string `json:"values,omitempty"`
	Envs    []string          `json:"envs,omitempty"` // unset: these, or all when empty
}

// VerifyResult is the answer to "Kiểm tra".
type VerifyResult struct {
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

func handleSecrets(ctx context.Context, deps Deps, method string, params json.RawMessage) (json.RawMessage, error) {
	store, err := secretsStore(deps)
	if err != nil {
		return nil, err
	}
	var p secretsParams
	if len(params) > 0 {
		if err := decodeParams(params, &p); err != nil {
			return nil, err
		}
	}
	switch method {
	case "secrets.list":
		v, err := store.View()
		if err != nil {
			return nil, err
		}
		return json.Marshal(v)
	case "secrets.set":
		if err := store.Set(p.Service, p.Values, "dashboard"); err != nil {
			return nil, err
		}
		return json.Marshal(map[string]bool{"ok": true})
	case "secrets.unset":
		if p.Service == "" {
			return nil, fmt.Errorf("service is required")
		}
		if err := store.Unset(p.Service, p.Envs...); err != nil {
			return nil, err
		}
		return json.Marshal(map[string]bool{"ok": true})
	case "secrets.verify":
		// Read through the store this request just looked at, whatever the
		// process-wide Lookup is pointed at.
		old := deploy.Lookup
		deploy.Lookup = store.Lookup
		defer func() { deploy.Lookup = old }()
		detail, err := deploy.Verify(ctx, p.Service)
		if err != nil {
			return json.Marshal(VerifyResult{OK: false, Detail: err.Error()})
		}
		return json.Marshal(VerifyResult{OK: true, Detail: detail})
	}
	return nil, fmt.Errorf("unknown method: %s", method)
}
