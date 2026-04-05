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

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true }, // allow all origins for dev
}

// Server is a WebSocket JSON-RPC server for remote control of the agent.
type Server struct {
	addr         string
	dashboardDir string
	handler      MethodHandler
	httpSrv      *http.Server
	startedAt    time.Time
	mu           sync.Mutex
	clients      map[*websocket.Conn]bool
}

// MethodHandler processes RPC method calls.
type MethodHandler func(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error)

func NewServer(addr string, handler MethodHandler, dashboardDir string) *Server {
	return &Server{
		addr:         addr,
		dashboardDir: dashboardDir,
		handler:      handler,
		clients:      make(map[*websocket.Conn]bool),
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
		mux.Handle("/", http.FileServer(http.Dir(s.dashboardDir)))
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

	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			break
		}

		var req Request
		if err := json.Unmarshal(msg, &req); err != nil {
			continue
		}

		result, err := s.handler(context.Background(), req.Method, req.Params)

		var resp Response
		resp.ID = req.ID
		if err != nil {
			resp.Error = &RPCError{Code: -1, Message: err.Error()}
		} else {
			resp.Result = result
		}

		respBytes, _ := json.Marshal(resp)
		if err := conn.WriteMessage(websocket.TextMessage, respBytes); err != nil {
			break
		}
	}

	log.Printf("gateway: client disconnected")
}
