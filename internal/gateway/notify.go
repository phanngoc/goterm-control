package gateway

import (
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/gorilla/websocket"

	"github.com/ngocp/goterm-control/internal/coord"
)

// PokeAgent tells the gateway at wsAddr to check the shared task queue now
// instead of waiting out its poll interval.
//
// This is a doorbell, not a delivery: the task itself is already committed to
// the shared database. A failure here — peer down, or its dashboard auth
// refusing an unauthenticated socket — costs latency and nothing else, because
// the peer's own poll will find the work anyway. Callers should log it at most.
func PokeAgent(wsAddr string) error {
	if wsAddr == "" {
		return fmt.Errorf("no address")
	}
	dialer := websocket.Dialer{HandshakeTimeout: 3 * time.Second}
	ws, _, err := dialer.Dial(wsAddr, nil)
	if err != nil {
		return fmt.Errorf("dial %s: %w", wsAddr, err)
	}
	defer ws.Close()

	req := Request{ID: "poke", Method: "tasks.poke"}
	if err := ws.WriteJSON(req); err != nil {
		return fmt.Errorf("write: %w", err)
	}

	// Read the ack so the peer has actually processed it before we hang up;
	// without this the close can race the server's read.
	_ = ws.SetReadDeadline(time.Now().Add(3 * time.Second))
	var resp Response
	if err := ws.ReadJSON(&resp); err != nil {
		return fmt.Errorf("read: %w", err)
	}
	if resp.Error != nil {
		return fmt.Errorf("peer: %s", resp.Error.Message)
	}
	return nil
}

// NotifyTaskCreated rings whoever could pick a new task up. A task addressed to
// one agent rings only that agent; an unassigned task rings every other online
// agent, since any of them may claim it.
//
// Best effort by design — see PokeAgent. The peer's poll is what guarantees
// delivery; this only shortens the wait.
func NotifyTaskCreated(cdb *coord.DB, task *coord.Task) {
	if cdb == nil || task == nil {
		return
	}
	agents, err := cdb.ListAgents()
	if err != nil {
		return
	}
	for _, a := range agents {
		if !a.Online || a.ID == task.CreatedBy {
			continue
		}
		if task.AssignedTo != "" && a.ID != task.AssignedTo {
			continue
		}
		if err := PokeAgent(a.WSAddr); err != nil {
			log.Printf("gateway: could not ring %s about %s (%v) — its poll will pick the task up",
				a.ID, task.ID, err)
		}
	}
}

// pokeResult is the ack sent back to a doorbell.
var pokeResult, _ = json.Marshal(map[string]bool{"poked": true})
