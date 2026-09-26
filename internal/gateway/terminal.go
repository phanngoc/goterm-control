package gateway

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"sync/atomic"

	"github.com/gorilla/websocket"
	"github.com/ngocp/goterm-control/internal/auth"
	"github.com/ngocp/goterm-control/internal/coord"
)

// A terminal in a project's folder, for the dashboard's editor.
//
// This is a login shell on this machine, reachable from the public dashboard.
// Nothing here pretends to confine it: it starts in the project folder and can
// go anywhere the user can. What guards it is who may open one:
//
//   - a login session, always — unlike /api/status and the peer routes, a
//     direct loopback caller is not exempt, because "a local process" is not
//     a reason to hand out a shell;
//   - the same WebSocket Origin check as /ws, so another site cannot open one
//     with the owner's cookie;
//   - a cap on how many run at once.
//
// Wire format: binary frames carry terminal bytes both ways; a text frame
// from the browser is a control message, {"type":"resize","cols":N,"rows":N}.

// TerminalPath is where the dashboard mounts it.
const TerminalPath = "/api/term"

// MaxTerminals caps concurrent shells, so a page stuck reconnecting in a loop
// cannot fill the process table.
const MaxTerminals = 16

var liveTerminals atomic.Int32

type termControl struct {
	Type string `json:"type"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
}

// TerminalHandler serves /api/term?channel=<id>&cols=N&rows=N.
func TerminalHandler(cdb *coord.DB, authMgr *auth.Manager) http.HandlerFunc {
	upgrader := websocket.Upgrader{CheckOrigin: authMgr.CheckOrigin}
	return func(w http.ResponseWriter, r *http.Request) {
		who := "local"
		if authMgr.Enabled() {
			u := authMgr.UserFromRequest(r)
			if u == nil {
				http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
				return
			}
			who = u.Username
		}
		if cdb == nil {
			http.Error(w, "coordination is disabled", http.StatusServiceUnavailable)
			return
		}
		channel := r.URL.Query().Get("channel")
		dir, err := cdb.ProjectFilePath(channel, "")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if n := liveTerminals.Add(1); n > MaxTerminals {
			liveTerminals.Add(-1)
			http.Error(w, fmt.Sprintf("too many terminals open (%d)", MaxTerminals), http.StatusTooManyRequests)
			return
		}
		defer liveTerminals.Add(-1)

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return // Upgrade has already answered
		}
		defer conn.Close()

		cols, _ := strconv.Atoi(r.URL.Query().Get("cols"))
		rows, _ := strconv.Atoi(r.URL.Query().Get("rows"))
		sh, err := startShell(dir, channel, cols, rows)
		if err != nil {
			_ = conn.WriteMessage(websocket.TextMessage, []byte("\r\nterminal: "+err.Error()+"\r\n"))
			return
		}
		log.Printf("terminal: %s opened a shell in %s (%s) from %s", who, dir, channel, r.RemoteAddr)
		defer func() {
			sh.Close()
			log.Printf("terminal: %s's shell in %s closed", who, channel)
		}()

		// Shell → browser. When the shell exits the socket is closed, which is
		// what tells the page the session is over.
		done := make(chan struct{})
		go func() {
			defer close(done)
			buf := make([]byte, 32<<10)
			for {
				n, err := sh.Read(buf)
				if n > 0 {
					if werr := conn.WriteMessage(websocket.BinaryMessage, buf[:n]); werr != nil {
						return
					}
				}
				if err != nil {
					_ = conn.WriteMessage(websocket.CloseMessage,
						websocket.FormatCloseMessage(websocket.CloseNormalClosure, "shell exited"))
					return
				}
			}
		}()

		// Browser → shell.
		go func() {
			for {
				kind, msg, err := conn.ReadMessage()
				if err != nil {
					sh.Close() // ends the read loop above
					return
				}
				if kind == websocket.BinaryMessage {
					if _, err := sh.Write(msg); err != nil {
						return
					}
					continue
				}
				var c termControl
				if json.Unmarshal(msg, &c) == nil && c.Type == "resize" {
					sh.Resize(c.Cols, c.Rows)
				}
			}
		}()
		<-done
	}
}

// shell is a running terminal session; see terminal_unix.go.
type shell interface {
	Read(p []byte) (int, error)
	Write(p []byte) (int, error)
	Resize(cols, rows int)
	Close()
}
