package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/ngocp/goterm-control/internal/auth"
)

// Server is a WebSocket JSON-RPC server for remote control of the agent.
// StreamSendHandler handles streaming "send" requests, emitting partial events.
type StreamSendHandler func(ctx context.Context, req Request, emit func(StreamEvent))

type Server struct {
	addr          string
	dashboardDir  string
	handler       MethodHandler
	streamHandler StreamSendHandler
	auth          *auth.Manager // nil-safe; enforces dashboard auth when enabled
	upgrader      websocket.Upgrader
	httpSrv       *http.Server
	startedAt     time.Time
	mu            sync.Mutex
	// clients maps each open dashboard socket to its write mutex — gorilla
	// forbids concurrent writes, and Broadcast writes from outside the
	// connection's own goroutine.
	clients map[*websocket.Conn]*sync.Mutex
}

// MethodHandler processes RPC method calls.
type MethodHandler func(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error)

func NewServer(addr string, handler MethodHandler, streamHandler StreamSendHandler, dashboardDir string, authMgr *auth.Manager) *Server {
	return &Server{
		addr:          addr,
		dashboardDir:  dashboardDir,
		handler:       handler,
		streamHandler: streamHandler,
		auth:          authMgr,
		upgrader: websocket.Upgrader{
			// Same-host, localhost, and the configured public host only —
			// blocks cross-site WebSocket hijacking from arbitrary origins.
			CheckOrigin: authMgr.CheckOrigin,
		},
		clients: make(map[*websocket.Conn]*sync.Mutex),
	}
}

// Broadcast sends one frame to every connected dashboard client.
//
// A conversation can move on a channel the browser is not part of — a Telegram
// turn, a task an agent claimed — and nothing else would tell an open dashboard
// showing that session to refresh. A client whose write fails is left to its
// own read loop to notice and drop; a slow or dead socket must not stall the
// others, so each write holds only that client's mutex.
func (s *Server) Broadcast(v any) {
	s.mu.Lock()
	targets := make(map[*websocket.Conn]*sync.Mutex, len(s.clients))
	for c, mu := range s.clients {
		targets[c] = mu
	}
	s.mu.Unlock()

	for c, mu := range targets {
		mu.Lock()
		_ = c.WriteJSON(v)
		mu.Unlock()
	}
}

// Start begins listening for WebSocket + HTTP connections.
func (s *Server) Start(ctx context.Context) error {
	s.startedAt = time.Now()

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", s.handleWS)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"status":"ok","uptime":"%s"}`, time.Since(s.startedAt).Round(time.Second))
	})
	// Dashboard auth endpoints (username/password → session cookie).
	if s.auth != nil {
		mux.HandleFunc("/api/login", s.auth.HandleLogin)
		mux.HandleFunc("/api/logout", s.auth.HandleLogout)
		mux.HandleFunc("/api/me", s.auth.HandleMe)
	}

	// Plain HTTP mirror of the "status" RPC method — lets simple pollers
	// (menu bar tray) avoid the WebSocket handshake. Requires a login
	// session when auth is enabled, except for direct loopback clients
	// (the tray); tunnel traffic always carries forwarding headers.
	mux.HandleFunc("/api/status", s.auth.RequireAuthExceptLocal(func(w http.ResponseWriter, r *http.Request) {
		res, err := s.handler(r.Context(), "status", nil)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(res)
	}))

	if s.dashboardDir != "" {
		fs := http.FileServer(http.Dir(s.dashboardDir))
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			// SPA fallback: serve index.html for routes like /chat/xxx, /status
			path := r.URL.Path
			if path != "/" && path != "" {
				// Check if it's a real file (js, css, etc.)
				if _, err := os.Stat(s.dashboardDir + path); err != nil {
					// Not a file — serve index.html for client-side routing
					r.URL.Path = "/"
				}
			}
			fs.ServeHTTP(w, r)
		})
		log.Printf("gateway: serving dashboard from %s", s.dashboardDir)
	}

	s.httpSrv = &http.Server{Addr: s.addr, Handler: mux}

	log.Printf("gateway: listening on %s", s.addr)

	go func() {
		<-ctx.Done()
		s.httpSrv.Close()
	}()

	err := s.httpSrv.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

func (s *Server) Uptime() time.Duration {
	return time.Since(s.startedAt)
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	// Resolve the login session BEFORE upgrading; browsers send cookies on
	// the WS handshake.
	if s.auth.Enabled() && s.auth.UserFromRequest(r) == nil {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}

	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("gateway: ws upgrade failed: %v", err)
		return
	}

	// Write mutex — gorilla websocket doesn't allow concurrent writes. Shared
	// with Broadcast through the clients map.
	writeMu := &sync.Mutex{}

	s.mu.Lock()
	s.clients[conn] = writeMu
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.clients, conn)
		s.mu.Unlock()
		conn.Close()
	}()

	log.Printf("gateway: client connected from %s", r.RemoteAddr)

	writeJSON := func(v any) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return conn.WriteJSON(v)
	}

	// Keep-alive: send ping every 30s so connection doesn't drop
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			writeMu.Lock()
			err := conn.WriteMessage(websocket.PingMessage, nil)
			writeMu.Unlock()
			if err != nil {
				return
			}
		}
	}()

	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			break
		}

		var req Request
		if err := json.Unmarshal(msg, &req); err != nil {
			continue
		}

		// Handle requests async so long-running sends don't block the read loop
		go func(req Request) {
			ctx := context.Background()

			// For "send", use streaming handler that sends partial events
			if req.Method == "send" && s.streamHandler != nil {
				s.streamHandler(ctx, req, func(ev StreamEvent) {
					ev.ID = req.ID
					if ev.Type == "response" {
						// Final response — send as proper Response
						writeJSON(Response{
							ID:     req.ID,
							Result: json.RawMessage(ev.Data),
						})
					} else {
						writeJSON(ev)
					}
				})
				return
			}

			result, err := s.handler(ctx, req.Method, req.Params)

			var resp Response
			resp.ID = req.ID
			if err != nil {
				resp.Error = &RPCError{Code: -1, Message: err.Error()}
			} else {
				resp.Result = result
			}

			if err := writeJSON(resp); err != nil {
				log.Printf("gateway: write error: %v", err)
			}
		}(req)
	}

	log.Printf("gateway: client disconnected")
}
