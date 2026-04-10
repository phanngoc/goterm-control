package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

const wsHandshakeTimeout = 5 * time.Second

// CdpSendFn sends a CDP method call and returns the raw JSON result.
type CdpSendFn func(method string, params map[string]any) (json.RawMessage, error)

type cdpRequest struct {
	ID     int            `json:"id"`
	Method string         `json:"method"`
	Params map[string]any `json:"params,omitempty"`
}

type cdpResponse struct {
	ID     int              `json:"id"`
	Result json.RawMessage  `json:"result,omitempty"`
	Error  *cdpError        `json:"error,omitempty"`
}

type cdpError struct {
	Message string `json:"message"`
}

// WithCdpSocket opens a WebSocket to wsURL, creates a CdpSendFn,
// executes fn, then closes the connection. This is the core primitive
// that all CDP operations use.
func WithCdpSocket(ctx context.Context, wsURL string, fn func(CdpSendFn) error) error {
	dialer := websocket.Dialer{
		HandshakeTimeout: wsHandshakeTimeout,
	}
	conn, _, err := dialer.DialContext(ctx, wsURL, http.Header{})
	if err != nil {
		return fmt.Errorf("cdp dial: %w", err)
	}

	var (
		nextID  atomic.Int64
		pending sync.Map          // map[int]chan cdpResponse
		writeMu sync.Mutex        // gorilla/websocket requires serialized writes
		done    = make(chan struct{})
	)

	// Reader goroutine: dispatch responses to pending requests.
	go func() {
		defer close(done)
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				// Connection closed or error — reject all pending.
				pending.Range(func(key, val any) bool {
					ch := val.(chan cdpResponse)
					select {
					case ch <- cdpResponse{Error: &cdpError{Message: "CDP socket closed"}}:
					default:
					}
					return true
				})
				return
			}
			var resp cdpResponse
			if json.Unmarshal(msg, &resp) != nil {
				continue
			}
			if val, ok := pending.LoadAndDelete(resp.ID); ok {
				ch := val.(chan cdpResponse)
				ch <- resp
			}
		}
	}()

	send := func(method string, params map[string]any) (json.RawMessage, error) {
		id := int(nextID.Add(1))
		ch := make(chan cdpResponse, 1)
		pending.Store(id, ch)

		req := cdpRequest{ID: id, Method: method, Params: params}
		writeMu.Lock()
		werr := conn.WriteJSON(req)
		writeMu.Unlock()
		if werr != nil {
			pending.Delete(id)
			return nil, fmt.Errorf("cdp write %s: %w", method, werr)
		}

		select {
		case resp := <-ch:
			if resp.Error != nil {
				return nil, fmt.Errorf("cdp %s: %s", method, resp.Error.Message)
			}
			return resp.Result, nil
		case <-ctx.Done():
			pending.Delete(id)
			return nil, ctx.Err()
		}
	}

	fnErr := fn(send)

	// Close WebSocket and wait for reader to exit.
	conn.Close()
	<-done

	return fnErr
}
