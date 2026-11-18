package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"log"
	"net/http"
	"strings"

	"github.com/ngocp/goterm-control/internal/auth"
	"github.com/ngocp/goterm-control/internal/connectors"
	"github.com/ngocp/goterm-control/internal/secrets"
)

// The Connectors panel (internal/connectors): what a project can plug into,
// Lovable's way. Answered by the dashboard process, from the same secrets
// store as the Connections page; a value never comes back to the page, only
// a mask. Enabling one writes the project's bomclaw.json, which is also what
// an agent's connector_enable tool does.

type connectorsParams struct {
	ChannelID string            `json:"channel_id,omitempty"`
	ID        string            `json:"id,omitempty"`
	Values    map[string]string `json:"values,omitempty"`
	On        bool              `json:"on,omitempty"`
}

// ConnectorsList is the panel's data.
type ConnectorsList struct {
	Categories []connectors.Category `json:"categories"`
	Connectors []connectors.View     `json:"connectors"`
	// ProjectDir is set when a channel was asked about and has a folder.
	ProjectDir string `json:"project_dir,omitempty"`
	// Missing: enabled on the project but not connected, with why.
	Missing map[string]string `json:"missing,omitempty"`
}

func handleConnectors(ctx context.Context, deps Deps, method string, params json.RawMessage) (json.RawMessage, error) {
	store, err := secretsStore(deps)
	if err != nil {
		return nil, err
	}
	var p connectorsParams
	if len(params) > 0 {
		if err := decodeParams(params, &p); err != nil {
			return nil, err
		}
	}
	projectDir := func() (string, error) {
		if strings.TrimSpace(p.ChannelID) == "" {
			return "", fmt.Errorf("channel_id is required")
		}
		dir, err := deps.Coord.ProjectFilePath(p.ChannelID, "")
		if err != nil {
			return "", fmt.Errorf("%s has no project folder: %w", p.ChannelID, err)
		}
		return dir, nil
	}
	conn := func() (*connectors.Connector, error) {
		c, ok := connectors.ByID(p.ID)
		if !ok {
			return nil, fmt.Errorf("no connector %q", p.ID)
		}
		return c, nil
	}
	switch method {
	case "connectors.list":
		out := ConnectorsList{Categories: connectors.Categories}
		if p.ChannelID != "" {
			if dir, err := projectDir(); err == nil {
				out.ProjectDir = dir
				_, out.Missing, _ = connectors.ProjectEnv(dir, store.Lookup)
			}
		}
		out.Connectors, err = connectors.Views(store, out.ProjectDir)
		if err != nil {
			return nil, err
		}
		return json.Marshal(out)
	case "connectors.set":
		c, err := conn()
		if err != nil {
			return nil, err
		}
		for env := range p.Values {
			for _, f := range c.Fields {
				if f.Env == env && f.Managed {
					return nil, fmt.Errorf("%s is set by signing in, not typed", env)
				}
			}
		}
		if err := store.Set(c.ID, p.Values, "dashboard"); err != nil {
			return nil, err
		}
		return json.Marshal(map[string]bool{"ok": true})
	case "connectors.unset":
		c, err := conn()
		if err != nil {
			return nil, err
		}
		if err := store.Unset(c.ID); err != nil {
			return nil, err
		}
		return json.Marshal(map[string]bool{"ok": true})
	case "connectors.verify":
		c, err := conn()
		if err != nil {
			return nil, err
		}
		detail, err := connectors.Verify(ctx, c.ID, store.Lookup)
		if err != nil {
			return json.Marshal(VerifyResult{OK: false, Detail: err.Error()})
		}
		return json.Marshal(VerifyResult{OK: true, Detail: detail})
	case "connectors.enable":
		dir, err := projectDir()
		if err != nil {
			return nil, err
		}
		changed, err := connectors.SetEnabled(dir, p.ID, p.On)
		if err != nil {
			return nil, err
		}
		return json.Marshal(map[string]bool{"ok": true, "changed": changed})
	}
	return nil, fmt.Errorf("unknown method: %s", method)
}

// ── Google sign-in ────────────────────────────────────────────────────────

// requestOrigin is the address the browser used to reach the dashboard,
// through the tunnel or not — Google must send it back to the same place.
func requestOrigin(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	host := r.Header.Get("X-Forwarded-Host")
	if host == "" {
		host = r.Host
	}
	return scheme + "://" + host
}

// ConnectorsOAuthHandler serves StartPath and CallbackPath. Both need a
// login: a sign-in stores a key every project on the machine can use.
func ConnectorsOAuthHandler(deps Deps, am *auth.Manager) http.HandlerFunc {
	return am.RequireAuthExceptLocal(func(w http.ResponseWriter, r *http.Request) {
		store, err := secretsStore(deps)
		if err != nil {
			oauthPage(w, http.StatusInternalServerError, "", err.Error())
			return
		}
		switch r.URL.Path {
		case connectors.StartPath:
			c, ok := connectors.ByID(r.URL.Query().Get("id"))
			if !ok || c.Auth != connectors.AuthOAuth {
				oauthPage(w, http.StatusBadRequest, "", "no Google connector "+r.URL.Query().Get("id"))
				return
			}
			clientID := store.Lookup("GOOGLE_CLIENT_ID")
			if clientID == "" || store.Lookup("GOOGLE_CLIENT_SECRET") == "" {
				oauthPage(w, http.StatusPreconditionFailed, c.ID,
					"Google OAuth client chưa được thiết lập. Mở Connectors → "+c.Title+" và nhập Client ID / Client secret trước.")
				return
			}
			redirect := requestOrigin(r) + connectors.CallbackPath
			st := connectors.NewState(connectors.Pending{Connector: c.ID, Project: r.URL.Query().Get("project"), Redirect: redirect})
			http.Redirect(w, r, connectors.AuthURL(c, clientID, redirect, st), http.StatusFound)
		case connectors.CallbackPath:
			q := r.URL.Query()
			pend, ok := connectors.TakeState(q.Get("state"))
			if !ok {
				oauthPage(w, http.StatusBadRequest, "", "Phiên đăng nhập đã hết hạn hoặc không hợp lệ — bấm Kết nối lại.")
				return
			}
			if e := q.Get("error"); e != "" {
				oauthPage(w, http.StatusBadRequest, pend.Connector, "Google từ chối: "+e)
				return
			}
			c, _ := connectors.ByID(pend.Connector)
			refresh, err := connectors.Exchange(r.Context(), store.Lookup("GOOGLE_CLIENT_ID"), store.Lookup("GOOGLE_CLIENT_SECRET"),
				q.Get("code"), pend.Redirect)
			if err != nil {
				oauthPage(w, http.StatusBadGateway, c.ID, err.Error())
				return
			}
			if err := store.Set(c.ID, map[string]string{c.TokenField(): refresh}, "google sign-in"); err != nil {
				oauthPage(w, http.StatusInternalServerError, c.ID, err.Error())
				return
			}
			detail, verr := connectors.Verify(r.Context(), c.ID, store.Lookup)
			if verr != nil {
				detail = "đã lưu, nhưng kiểm tra lỗi: " + verr.Error()
			}
			log.Printf("connectors: %s connected (%s)", c.ID, detail)
			oauthPage(w, http.StatusOK, c.ID, "✓ Đã kết nối "+c.Title+" — "+detail)
		default:
			http.NotFound(w, r)
		}
	})
}

// oauthPage is the small page a sign-in ends on. Opened as a popup by the
// panel, it tells its opener and closes; opened from a chat link, it stays
// and says what happened.
func oauthPage(w http.ResponseWriter, code int, id, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	ok := code == http.StatusOK
	payload, _ := json.Marshal(map[string]any{"type": "bomclaw-connector", "id": id, "ok": ok, "message": msg})
	color := "#b91c1c"
	if ok {
		color = "#047857"
	}
	fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Connectors</title>
<body style="font:15px system-ui,sans-serif;max-width:32rem;margin:15vh auto;padding:0 16px;color:#111;background:#fff">
<p style="color:%s">%s</p><p style="color:#666;font-size:13px">Có thể đóng tab này.</p>
<script>try{if(window.opener){window.opener.postMessage(%s,location.origin);%s}}catch(e){}</script>`,
		color, html.EscapeString(msg), payload, map[bool]string{true: "setTimeout(()=>window.close(),900)", false: ""}[ok])
}

// SaveOrigin records where the dashboard is reached from outside, so tools
// in other processes (`bomclaw mcp connectors`) can hand out links to it.
func SaveOrigin(coordPath, origin string) {
	if origin == "" {
		return
	}
	if err := connectors.SaveOrigin(secrets.PathBeside(coordPath), origin); err != nil {
		log.Printf("connectors: %v", err)
	}
}
