// Package browserbridge is the gateway side of the BomClaw Browser Bridge: a
// Chrome extension installed in the user's own browser keeps a WebSocket open
// to the gateway, and the agent drives that browser — with the user's logged-in
// sessions — by sending it actions through the Hub.
//
// The wire protocol is one JSON object per frame (see docs/browser-bridge.md):
//
//	ext → gw  {"type":"hello","token":…,"client":…,"browser":…}
//	gw → ext  {"type":"welcome","agent":…,"name":…}          (or close 4001)
//	gw → ext  {"type":"call","id":…,"action":…,"params":{…}}
//	ext → gw  {"type":"result","id":…,"ok":true,"result":…}
//	ext → gw  {"type":"result","id":…,"ok":false,"error":…}
//	either    {"type":"ping"} / {"type":"pong"}
package browserbridge

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

const (
	helloTimeout    = 5 * time.Second
	pingInterval    = 20 * time.Second
	liveness        = 60 * time.Second
	maxCallTimeout  = 2 * time.Minute
	defaultCallWait = 30 * time.Second

	// Close codes the extension understands.
	closeBadHandshake = 4000
	closeUnauthorized = 4001
	closeReplaced     = 4002
	closeTimeout      = 4003
)

var (
	// ErrNoBrowser is returned when no extension is connected. Its text is
	// what the agent will read, so it says what to do about it.
	ErrNoBrowser = errors.New("no browser is connected — install the BomClaw Browser Bridge extension and pair it with the token from `bomclaw browser token`")
	// ErrDisconnected is returned for calls in flight when the browser drops.
	ErrDisconnected = errors.New("the browser disconnected while the action was running")
	// ErrEvalDisabled is returned for eval when config turned it off.
	ErrEvalDisabled = errors.New("running JavaScript in the browser is disabled by config (browser.extension.allow_eval)")
)

// ActionError is a failure reported by the browser itself: the ref was not
// found, the tab is gone, the page refused the script.
type ActionError struct {
	Action  string
	Message string
}

func (e *ActionError) Error() string { return e.Action + ": " + e.Message }

// Options configures a Hub.
type Options struct {
	Token        string // pairing secret the extension must present; required
	AgentID      string
	AgentName    string
	CallTimeout  time.Duration // per action; default 30s ("wait" adds its own duration)
	AllowEval    bool
	BlockedHosts []string // hosts the agent may never navigate to; "*.example.com" allowed
	Logf         func(format string, args ...any)
}

// Status describes the connected browser, for `bomclaw browser status` and
// the dashboard.
type Status struct {
	Connected   bool   `json:"connected"`
	Client      string `json:"client,omitempty"`
	Browser     string `json:"browser,omitempty"`
	ConnectedAt string `json:"connected_at,omitempty"`
	LastSeen    string `json:"last_seen,omitempty"`
}

type frame struct {
	Type    string          `json:"type"`
	ID      string          `json:"id,omitempty"`
	Token   string          `json:"token,omitempty"`
	Client  string          `json:"client,omitempty"`
	Browser string          `json:"browser,omitempty"`
	Agent   string          `json:"agent,omitempty"`
	Name    string          `json:"name,omitempty"`
	Action  string          `json:"action,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	OK      bool            `json:"ok,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   string          `json:"error,omitempty"`
}

// Hub accepts the extension's connection and relays actions to it. One
// browser at a time: a newer connection replaces the older, so re-pairing or
// a reloaded extension never leaves a stale socket holding the slot.
type Hub struct {
	opts     Options
	upgrader websocket.Upgrader
	seq      atomic.Uint64

	mu   sync.Mutex
	conn *conn
}

// New creates a Hub. It panics on an empty token: an unauthenticated bridge
// would hand anyone who can reach the port control of the user's browser.
func New(opts Options) *Hub {
	if opts.Token == "" {
		panic("browserbridge: a pairing token is required")
	}
	if opts.CallTimeout <= 0 {
		opts.CallTimeout = defaultCallWait
	}
	if opts.Logf == nil {
		opts.Logf = log.Printf
	}
	return &Hub{
		opts: opts,
		upgrader: websocket.Upgrader{
			// The extension's Origin is chrome-extension://<id>; the gateway's
			// own dashboard origins are loopback. Nothing else may connect here.
			CheckOrigin: allowOrigin,
		},
	}
}

func allowOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true // non-browser client (tests, curl)
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	switch u.Scheme {
	case "chrome-extension", "moz-extension":
		return true
	}
	h := u.Hostname()
	return h == "localhost" || h == "127.0.0.1" || h == "::1"
}

// ServeHTTP is the /ext endpoint: upgrade, verify the hello token, then keep
// the connection until the extension leaves or stops answering pings.
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ws, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}

	_ = ws.SetReadDeadline(time.Now().Add(helloTimeout))
	var hello frame
	if err := ws.ReadJSON(&hello); err != nil || hello.Type != "hello" {
		closeWith(ws, closeBadHandshake, "expected a hello frame")
		return
	}
	if !h.tokenMatches(hello.Token) {
		h.opts.Logf("browser bridge: rejected a connection from %s (wrong pairing token)", r.RemoteAddr)
		closeWith(ws, closeUnauthorized, "unauthorized")
		return
	}
	_ = ws.SetReadDeadline(time.Time{})

	c := newConn(ws, hello)
	if err := c.send(frame{Type: "welcome", Agent: h.opts.AgentID, Name: h.opts.AgentName}); err != nil {
		c.close(closeBadHandshake, "welcome failed")
		return
	}

	h.mu.Lock()
	old := h.conn
	h.conn = c
	h.mu.Unlock()
	if old != nil {
		old.close(closeReplaced, "replaced by a newer connection")
	}
	h.opts.Logf("browser bridge: %s connected", c.describe())

	go h.keepAlive(c)
	h.readLoop(c)

	h.mu.Lock()
	if h.conn == c {
		h.conn = nil
	}
	h.mu.Unlock()
	c.failPending()
	h.opts.Logf("browser bridge: %s disconnected", c.describe())
}

func (h *Hub) tokenMatches(presented string) bool {
	return subtle.ConstantTimeCompare([]byte(presented), []byte(h.opts.Token)) == 1
}

func (h *Hub) readLoop(c *conn) {
	defer c.close(websocket.CloseNormalClosure, "")
	for {
		var f frame
		if err := c.ws.ReadJSON(&f); err != nil {
			return
		}
		c.touch()
		switch f.Type {
		case "result":
			c.deliver(f)
		case "ping":
			_ = c.send(frame{Type: "pong"})
		}
	}
}

// keepAlive pings so the extension's service worker is kept awake (Chrome
// unloads idle workers) and drops a connection that has gone silent.
func (h *Hub) keepAlive(c *conn) {
	t := time.NewTicker(pingInterval)
	defer t.Stop()
	for {
		select {
		case <-c.closed:
			return
		case <-t.C:
			if time.Since(c.lastSeen()) > liveness {
				c.close(closeTimeout, "no answer to pings")
				return
			}
			if err := c.send(frame{Type: "ping"}); err != nil {
				return
			}
		}
	}
}

// Connected reports whether a browser is attached right now.
func (h *Hub) Connected() bool { return h.current() != nil }

// Status describes the attached browser.
func (h *Hub) Status() Status {
	c := h.current()
	if c == nil {
		return Status{}
	}
	return Status{
		Connected:   true,
		Client:      c.client,
		Browser:     c.browser,
		ConnectedAt: c.connectedAt.Format(time.RFC3339),
		LastSeen:    c.lastSeen().Format(time.RFC3339),
	}
}

func (h *Hub) current() *conn {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.conn
}

// Call sends one action to the browser and waits for its answer. Policy is
// applied here, before anything reaches the extension, so a disabled eval or
// a blocked URL fails the same way whether it came from the CLI, the
// dashboard or a peer agent.
func (h *Hub) Call(ctx context.Context, action string, params json.RawMessage) (json.RawMessage, error) {
	if action == "" {
		return nil, errors.New("action is required")
	}
	if len(params) == 0 {
		params = json.RawMessage(`{}`)
	}
	if err := h.authorize(action, params); err != nil {
		return nil, err
	}
	c := h.current()
	if c == nil {
		return nil, ErrNoBrowser
	}

	id := fmt.Sprintf("%d", h.seq.Add(1))
	ch := c.register(id)
	defer c.unregister(id)

	if err := c.send(frame{Type: "call", ID: id, Action: action, Params: params}); err != nil {
		return nil, fmt.Errorf("send to browser: %w", err)
	}

	wait := h.callTimeout(action, params)
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case f := <-ch:
		if !f.OK {
			return nil, &ActionError{Action: action, Message: f.Error}
		}
		return f.Result, nil
	case <-c.closed:
		return nil, ErrDisconnected
	case <-timer.C:
		return nil, fmt.Errorf("%s: the browser did not answer within %s", action, wait)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (h *Hub) authorize(action string, params json.RawMessage) error {
	var p struct {
		URL    string `json:"url"`
		Action string `json:"action"`
	}
	_ = json.Unmarshal(params, &p)
	switch action {
	case "eval":
		if !h.opts.AllowEval {
			return ErrEvalDisabled
		}
	case "navigate":
		return CheckNavigateURL(p.URL, h.opts.BlockedHosts)
	case "tabs":
		if p.Action == "open" {
			return CheckNavigateURL(p.URL, h.opts.BlockedHosts)
		}
	}
	return nil
}

// callTimeout gives "wait" the time it asked for on top of the base timeout;
// everything else gets the configured default.
func (h *Hub) callTimeout(action string, params json.RawMessage) time.Duration {
	wait := h.opts.CallTimeout
	if action == "wait" {
		var p struct {
			Ms int `json:"ms"`
		}
		_ = json.Unmarshal(params, &p)
		if p.Ms > 0 {
			wait += time.Duration(p.Ms) * time.Millisecond
		}
	}
	if wait > maxCallTimeout {
		wait = maxCallTimeout
	}
	return wait
}

// --- connection ---

type conn struct {
	ws          *websocket.Conn
	client      string
	browser     string
	connectedAt time.Time
	seen        atomic.Int64 // unix nanos

	writeMu sync.Mutex

	pendMu  sync.Mutex
	pending map[string]chan frame

	closed    chan struct{}
	closeOnce sync.Once
}

func newConn(ws *websocket.Conn, hello frame) *conn {
	c := &conn{
		ws:          ws,
		client:      hello.Client,
		browser:     hello.Browser,
		connectedAt: time.Now(),
		pending:     make(map[string]chan frame),
		closed:      make(chan struct{}),
	}
	if c.client == "" {
		c.client = "browser extension"
	}
	c.touch()
	return c
}

func (c *conn) describe() string {
	if c.browser == "" {
		return c.client
	}
	return c.client + " (" + shorten(c.browser, 60) + ")"
}

func (c *conn) touch()              { c.seen.Store(time.Now().UnixNano()) }
func (c *conn) lastSeen() time.Time { return time.Unix(0, c.seen.Load()) }

func (c *conn) send(f frame) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_ = c.ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return c.ws.WriteJSON(f)
}

func (c *conn) register(id string) chan frame {
	ch := make(chan frame, 1)
	c.pendMu.Lock()
	c.pending[id] = ch
	c.pendMu.Unlock()
	return ch
}

func (c *conn) unregister(id string) {
	c.pendMu.Lock()
	delete(c.pending, id)
	c.pendMu.Unlock()
}

func (c *conn) deliver(f frame) {
	c.pendMu.Lock()
	ch, ok := c.pending[f.ID]
	c.pendMu.Unlock()
	if ok {
		select {
		case ch <- f:
		default:
		}
	}
}

// failPending is called once the read loop has ended; callers blocked in Call
// are released through the closed channel, this just makes it explicit.
func (c *conn) failPending() {
	c.pendMu.Lock()
	for id := range c.pending {
		delete(c.pending, id)
	}
	c.pendMu.Unlock()
}

func (c *conn) close(code int, reason string) {
	c.closeOnce.Do(func() {
		close(c.closed)
		closeWith(c.ws, code, reason)
	})
}

func closeWith(ws *websocket.Conn, code int, reason string) {
	msg := websocket.FormatCloseMessage(code, reason)
	_ = ws.WriteControl(websocket.CloseMessage, msg, time.Now().Add(time.Second))
	_ = ws.Close()
}

func shorten(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
