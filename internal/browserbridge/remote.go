package browserbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// The hub of an agent that is not the master: it holds no browser and relays
// every call to the master gateway's /api/browser/call, naming itself so the
// extension gives it a tab group of its own. The master accepts the request
// because it comes straight from loopback (auth.RequireAuthExceptLocal).

type remoteRequest struct {
	Agent  string          `json:"agent"`
	Action string          `json:"action"`
	Params json.RawMessage `json:"params"`
}

type remoteResponse struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

func (h *Hub) remoteCall(ctx context.Context, action string, params json.RawMessage) (json.RawMessage, error) {
	body, _ := json.Marshal(remoteRequest{Agent: h.opts.AgentID, Action: action, Params: params})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.opts.Master+"/api/browser/call", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("the master agent at %s is not answering: %w", h.opts.Master, err)
	}
	defer resp.Body.Close()
	var out remoteResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("the master agent at %s answered %s, not a browser result", h.opts.Master, resp.Status)
	}
	switch {
	case out.OK:
		return out.Result, nil
	case resp.StatusCode == http.StatusServiceUnavailable:
		return nil, ErrNoBrowser
	case resp.StatusCode == http.StatusForbidden:
		return nil, ErrEvalDisabled
	case out.Error == "":
		return nil, errors.New(resp.Status)
	}
	// The master's text already names the action ("click: no element …").
	return nil, errors.New(out.Error)
}

// remoteStatus is the master's view of the browser, under this agent's name.
func (h *Hub) remoteStatus() Status {
	st := Status{}
	// Status is polled by screens; a master that is down must not hang them.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.opts.Master+"/api/browser/status", nil)
	if err == nil {
		if resp, err := h.client.Do(req); err == nil {
			_ = json.NewDecoder(resp.Body).Decode(&st)
			resp.Body.Close()
		}
	}
	st.AgentID, st.AgentName, st.Master = h.opts.AgentID, h.opts.AgentName, h.opts.Master
	return st
}
