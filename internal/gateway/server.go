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
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true }, // allow all origins for dev
}

// Server is a WebSocket JSON-RPC server for remote control of the agent.
// StreamSendHandler handles streaming "send" requests, emitting partial events.
type StreamSendHandler func(ctx context.Context, req Request, emit func(StreamEvent))

type Server struct {
	addr          string
	dashboardDir  string
	handler       MethodHandler
	streamHandler StreamSendHandler
	httpSrv       *http.Server
	startedAt     time.Time
	mu            sync.Mutex
	clients       map[*websocket.Conn]bool
}

// MethodHandler processes RPC method calls.
type MethodHandler func(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error)

func NewServer(addr string, handler MethodHandler, streamHandler StreamSendHandler, dashboardDir string) *Server {
	return &Server{
		addr:          addr,
		dashboardDir:  dashboardDir,
		handler:       handler,
		streamHandler: streamHandler,
		clients:       make(map[*websocket.Conn]bool),
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
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("gateway: ws upgrade failed: %v", err)
		return
	}

	s.mu.Lock()
	s.clients[conn] = true
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.clients, conn)
		s.mu.Unlock()
		conn.Close()
	}()

	log.Printf("gateway: client connected from %s", r.RemoteAddr)

	// Write mutex — gorilla websocket doesn't allow concurrent writes
	var writeMu sync.Mutex
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
			// For "send", use streaming handler that sends partial events
			if req.Method == "send" && s.streamHandler != nil {
				s.streamHandler(context.Background(), req, func(ev StreamEvent) {
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

			result, err := s.handler(context.Background(), req.Method, req.Params)

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
