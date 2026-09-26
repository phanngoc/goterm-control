package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// The dashboard in a process of its own.
//
// It used to be served by agent 1's gateway, so every restart of agent 1 — a
// deploy, a model change, the restart button — took the dashboard down with
// it, including the page that was asking for the restart. Most of what the
// dashboard shows does not live in any agent: traces, tasks, rooms, notes,
// schedules and skills are rows in the shared database, which any process can
// read. Those are answered here, and keep working while every agent is down.
//
// What does live in an agent — a chat turn, its sessions and transcripts, its
// status, its credential pool — is relayed to one agent over loopback. The
// relay is per browser socket and passes frames through untouched, so request
// ids, stream events and the agent's broadcasts reach the browser exactly as
// they did when the browser was connected to the agent directly.

// Relay configures a Server that answers some methods itself and forwards the
// rest to an agent's gateway.
type Relay struct {
	// Local answers the methods in DetachedMethods.
	Local MethodHandler

	// Upstream returns the WebSocket address of the agent that answers
	// everything else. Asked on every (re)connect, so an agent that came back
	// on a different port is found again.
	Upstream func() (string, error)

	// Retry is how long to wait between attempts to reach the agent. Zero
	// means two seconds.
	Retry time.Duration
}

// DetachedMethods are answered by the dashboard process from the shared
// database. Anything not listed is forwarded — a method added to the agent
// later reaches the agent without this list having to know about it.
var DetachedMethods = map[string]bool{
	"admin.overview":  true,
	"admin.settings":  true,
	"admin.set_model": true,
	"admin.restart":   true,

	"traces.list": true,
	"traces.get":  true,

	"tasks.list":    true,
	"tasks.get":     true,
	"tasks.create":  true,
	"tasks.cancel":  true,
	"tasks.project": true,
	"tasks.resume":  true,
	"tasks.unblock": true,

	"messages.list": true,
	"messages.send": true,

	"channels.list":     true,
	"channels.messages": true,
	"channels.post":     true,
	"channels.files":    true,
	"channels.brief":    true,
	"channels.create":   true,
	"channels.read":     true,
	"channels.gateways": true,
	"channels.bind":     true,
	"channels.unbind":   true,

	"skills.list":    true,
	"skills.get":     true,
	"skills.install": true,
	"skills.remove":  true,
	"skills.copy":    true,

	"artifacts.list": true,
	"artifacts.get":  true,

	"notes.list": true,
	"notes.add":  true,

	"schedules.list":   true,
	"schedules.get":    true,
	"schedules.create": true,
	"schedules.toggle": true,
	"schedules.delete": true,
	"schedules.run":    true,

	"preview.status": true,
	"preview.start":  true,
	"preview.stop":   true,
	"preview.logs":   true,
}

// SetRelay turns this server into a dashboard server. Call before Start.
func (s *Server) SetRelay(r *Relay) { s.relay = r }

// serveRelay is handleWS's read loop for a dashboard server.
func (s *Server) serveRelay(conn *websocket.Conn, r *http.Request, writeMu *sync.Mutex) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	writeRaw := func(b []byte) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return conn.WriteMessage(websocket.TextMessage, b)
	}
	writeJSON := func(v any) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return conn.WriteJSON(v)
	}

	// The browser's login cookie is carried upstream. Both processes read the
	// same user store, so the agent accepts the session the dashboard already
	// checked, and no second credential exists.
	hdr := http.Header{}
	if c := r.Header.Get("Cookie"); c != "" {
		hdr.Set("Cookie", c)
	}
	up := &upstreamLink{relay: s.relay, header: hdr, pending: map[string]bool{}}
	go up.run(ctx, writeRaw, writeJSON)

	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var req Request
		if err := json.Unmarshal(msg, &req); err != nil {
			continue
		}
		if DetachedMethods[req.Method] {
			go func(req Request) {
				result, err := s.relay.Local(ctx, req.Method, req.Params)
				resp := Response{ID: req.ID, Result: result}
				if err != nil {
					resp = Response{ID: req.ID, Error: &RPCError{Code: -1, Message: err.Error()}}
				}
				if err := writeJSON(resp); err != nil {
					log.Printf("dashboard: write error: %v", err)
				}
			}(req)
			continue
		}
		if err := up.send(req.ID, msg); err != nil {
			_ = writeJSON(Response{ID: req.ID, Error: &RPCError{Code: -1, Message: err.Error()}})
		}
	}
}

// upstreamLink is one browser socket's connection to the agent.
type upstreamLink struct {
	relay  *Relay
	header http.Header

	mu      sync.Mutex
	conn    *websocket.Conn
	lastErr error
	// pending holds ids forwarded and not yet answered, so a request caught
	// by the agent going away is answered with that, instead of leaving a
	// spinner waiting for a reply that died with the process.
	pending map[string]bool
}

func (u *upstreamLink) send(id string, msg []byte) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.conn == nil {
		if u.lastErr != nil {
			return fmt.Errorf("agent not reachable: %v", u.lastErr)
		}
		return fmt.Errorf("agent not reachable yet")
	}
	if id != "" {
		u.pending[id] = true
	}
	if err := u.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
		delete(u.pending, id)
		return fmt.Errorf("agent not reachable: %v", err)
	}
	return nil
}

// run keeps the link up for as long as the browser socket lives, relaying
// every frame the agent sends. A restart of the agent is a gap, not an end.
func (u *upstreamLink) run(ctx context.Context, writeRaw func([]byte) error, writeJSON func(any) error) {
	retry := u.relay.Retry
	if retry <= 0 {
		retry = 2 * time.Second
	}
	dialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	for ctx.Err() == nil {
		c, err := u.dial(ctx, &dialer)
		if err != nil {
			u.mu.Lock()
			u.lastErr = err
			u.mu.Unlock()
			select {
			case <-ctx.Done():
				return
			case <-time.After(retry):
			}
			continue
		}

		u.mu.Lock()
		u.conn, u.lastErr = c, nil
		u.mu.Unlock()

		done := make(chan struct{})
		go func() {
			select {
			case <-ctx.Done():
				c.Close()
			case <-done:
			}
		}()
		err = u.pump(c, writeRaw)
		close(done)
		c.Close()

		u.mu.Lock()
		u.conn, u.lastErr = nil, err
		lost := u.pending
		u.pending = map[string]bool{}
		u.mu.Unlock()
		for id := range lost {
			_ = writeJSON(Response{ID: id, Error: &RPCError{Code: -1, Message: fmt.Sprintf("agent connection dropped: %v", err)}})
		}
	}
}

func (u *upstreamLink) dial(ctx context.Context, d *websocket.Dialer) (*websocket.Conn, error) {
	addr, err := u.relay.Upstream()
	if err != nil {
		return nil, err
	}
	c, resp, err := d.DialContext(ctx, addr, u.header)
	if err != nil {
		if resp != nil {
			return nil, fmt.Errorf("%s: HTTP %d", addr, resp.StatusCode)
		}
		return nil, err
	}
	return c, nil
}

// pump copies frames from the agent to the browser until the agent goes away.
func (u *upstreamLink) pump(c *websocket.Conn, writeRaw func([]byte) error) error {
	for {
		_, msg, err := c.ReadMessage()
		if err != nil {
			return err
		}
		// A final answer carries the id and no type; stream events carry both.
		var head struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		}
		if json.Unmarshal(msg, &head) == nil && head.ID != "" && head.Type == "" {
			u.mu.Lock()
			delete(u.pending, head.ID)
			u.mu.Unlock()
		}
		if err := writeRaw(msg); err != nil {
			return err
		}
	}
}
