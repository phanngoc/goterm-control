package gateway

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ngocp/goterm-control/internal/coord"
)

// PokeAgent tells the gateway behind wsAddr to check the shared task queue now
// instead of waiting out its poll interval.
//
// This is a doorbell, not a delivery: the task itself is already committed to
// the shared database. A failure here — peer down, wrong address — costs
// latency and nothing else, because the peer's own poll finds the work anyway.
// Callers should log it at most.
//
// It goes over plain HTTP to /api/tasks/poke rather than the /ws socket. The
// socket requires a dashboard login cookie whenever gateway.auth is on, which
// is the shipped default, so every cross-agent poke was being refused with a
// 401 and hand-offs always waited the full poll interval. The HTTP route sits
// behind the same rule as /api/status: a login session, or a direct loopback
// caller — and two agents on one machine are loopback to each other.
func PokeAgent(wsAddr string) error {
	target, err := PokeURL(wsAddr)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Post(target, "application/json", strings.NewReader("{}"))
	if err != nil {
		return fmt.Errorf("poke %s: %w", target, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("poke %s: HTTP %d", target, resp.StatusCode)
	}
	return nil
}

// PokeURL derives the doorbell endpoint from the address an agent registered
// (ws://127.0.0.1:18789/ws → http://127.0.0.1:18789/api/tasks/poke). Agents
// advertise their WebSocket address in the shared database; the HTTP route
// lives on the same host and port.
func PokeURL(wsAddr string) (string, error) {
	if strings.TrimSpace(wsAddr) == "" {
		return "", fmt.Errorf("no address")
	}
	u, err := url.Parse(wsAddr)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("bad agent address %q", wsAddr)
	}
	switch u.Scheme {
	case "ws", "http":
		u.Scheme = "http"
	case "wss", "https":
		u.Scheme = "https"
	default:
		return "", fmt.Errorf("bad agent address %q: scheme %s", wsAddr, u.Scheme)
	}
	u.Path, u.RawQuery, u.Fragment = "/api/tasks/poke", "", ""
	return u.String(), nil
}

// PokeHandler is the HTTP side of the doorbell. poke is the local runner's
// Poke; a nil-safe no-op when this agent does not claim tasks, so the route can
// always be mounted and a peer never gets a 404 for ringing.
func PokeHandler(poke func()) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"POST required"}`, http.StatusMethodNotAllowed)
			return
		}
		if poke != nil {
			poke()
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(pokeResult)
	}
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
	NotifyAgents(cdb, task.AssignedTo, task.CreatedBy, "about "+task.ID)
}

// NotifyAgents rings the agents that could act: only `only` when set, else
// every online agent except `skip`. why is for the log line.
func NotifyAgents(cdb *coord.DB, only, skip, why string) {
	if cdb == nil {
		return
	}
	agents, err := cdb.ListAgents()
	if err != nil {
		return
	}
	for _, a := range agents {
		if !a.Online || a.ID == skip {
			continue
		}
		if only != "" && a.ID != only {
			continue
		}
		if err := PokeAgent(a.WSAddr); err != nil {
			log.Printf("gateway: could not ring %s %s (%v) — its poll will pick the work up", a.ID, why, err)
		}
	}
}

// pokeResult is the ack sent back to a doorbell.
var pokeResult, _ = json.Marshal(map[string]bool{"poked": true})
