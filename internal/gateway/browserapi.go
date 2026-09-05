package gateway

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/ngocp/goterm-control/internal/browserbridge"
)

// BrowserAPI is the plain-HTTP face of the Browser Bridge, for the `bomclaw
// browser` CLI. The agent's shell runs on this machine, so the routes are
// mounted behind the same rule as /api/status: a login session, or a direct
// loopback caller. /ws was deliberately not widened for this — it keeps its
// cookie-only rule for the tunnel.
type BrowserAPI struct {
	Hub    *browserbridge.Hub
	Token  string // pairing token, served to loopback callers for `bomclaw browser token`
	ExtURL string // where the extension should connect, e.g. ws://127.0.0.1:18789/ext
}

type browserCallRequest struct {
	Action string          `json:"action"`
	Params json.RawMessage `json:"params"`
}

type browserCallResponse struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// HandleCall runs one browser action: POST {"action":…,"params":{…}}.
// A missing browser is 503 so the CLI can give the user a distinct exit code;
// an action the browser itself refused is 200 with ok:false, because the
// request was fine and the answer is the useful part.
func (a *BrowserAPI) HandleCall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"ok":false,"error":"POST required"}`, http.StatusMethodNotAllowed)
		return
	}
	var req browserCallRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeBrowserJSON(w, http.StatusBadRequest, browserCallResponse{Error: "invalid request body: " + err.Error()})
		return
	}
	result, err := a.Hub.Call(r.Context(), req.Action, req.Params)
	if err != nil {
		status := http.StatusOK
		switch {
		case errors.Is(err, browserbridge.ErrNoBrowser):
			status = http.StatusServiceUnavailable
		case errors.Is(err, browserbridge.ErrEvalDisabled):
			status = http.StatusForbidden
		}
		writeBrowserJSON(w, status, browserCallResponse{Error: err.Error()})
		return
	}
	writeBrowserJSON(w, http.StatusOK, browserCallResponse{OK: true, Result: result})
}

// HandleStatus reports whether a browser is attached.
func (a *BrowserAPI) HandleStatus(w http.ResponseWriter, r *http.Request) {
	writeBrowserJSON(w, http.StatusOK, a.Hub.Status())
}

// HandleToken hands out what the user pastes into the extension popup.
func (a *BrowserAPI) HandleToken(w http.ResponseWriter, r *http.Request) {
	writeBrowserJSON(w, http.StatusOK, map[string]string{"token": a.Token, "url": a.ExtURL})
}

func writeBrowserJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
