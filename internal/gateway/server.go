package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"golang.org/x/net/websocket"
)

// Server is a WebSocket JSON-RPC server for remote control of the agent.
type Server struct {
	addr      string
	handler   MethodHandler
	httpSrv   *http.Server
	startedAt time.Time
	mu        sync.Mutex
	clients   map[*websocket.Conn]bool
}

// MethodHandler processes RPC method calls.
type MethodHandler func(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error)

// NewServer creates a gateway server.
func NewServer(addr string, handler MethodHandler) *Server {
	return &Server{
		addr:    addr,
		handler: handler,
		clients: make(map[*websocket.Conn]bool),
	}
}

// Start begins listening for WebSocket connections. Blocks until ctx is canceled.
func (s *Server) Start(ctx context.Context) error {
	s.startedAt = time.Now()

	mux := http.NewServeMux()
	mux.Handle("/ws", websocket.Handler(s.handleWS))
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"status":"ok","uptime":"%s"}`, time.Since(s.startedAt).Round(time.Second))
	})

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

// Uptime returns the server uptime.
func (s *Server) Uptime() time.Duration {
	return time.Since(s.startedAt)
}

func (s *Server) handleWS(ws *websocket.Conn) {
	s.mu.Lock()
	s.clients[ws] = true
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.clients, ws)
		s.mu.Unlock()
		ws.Close()
	}()

	log.Printf("gateway: client connected from %s", ws.Request().RemoteAddr)

	for {
		var req Request
		if err := websocket.JSON.Receive(ws, &req); err != nil {
			break
		}

		result, err := s.handler(context.Background(), req.Method, req.Params)

		var resp Response
		resp.ID = req.ID
		if err != nil {
			resp.Error = &RPCError{Code: -1, Message: err.Error()}
		} else {
			resp.Result = result
		}

		if err := websocket.JSON.Send(ws, resp); err != nil {
			break
		}
	}

	log.Printf("gateway: client disconnected")
}
