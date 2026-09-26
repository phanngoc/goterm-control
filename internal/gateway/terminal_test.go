//go:build !windows

package gateway

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/ngocp/goterm-control/internal/auth"
)

func terminalServer(t *testing.T, authMgr *auth.Manager) (*httptest.Server, string, string) {
	t.Helper()
	db := testCoordDB(t)
	p, err := db.CreateProject("radar", "", "owner", t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(TerminalHandler(db, authMgr))
	t.Cleanup(ts.Close)
	return ts, p.ID, p.Workspace
}

func termURL(ts *httptest.Server, channel string) string {
	return "ws" + strings.TrimPrefix(ts.URL, "http") + "?cols=100&rows=30&channel=" + channel
}

// readUntil collects terminal output until it contains want.
func readTermUntil(t *testing.T, c *websocket.Conn, want string) string {
	t.Helper()
	var out strings.Builder
	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
	for !strings.Contains(out.String(), want) {
		_, msg, err := c.ReadMessage()
		if err != nil {
			t.Fatalf("waiting for %q: %v\noutput so far:\n%s", want, err, out.String())
		}
		out.Write(msg)
	}
	return out.String()
}

func TestTheTerminalRunsAShellInTheProjectFolder(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh") // no profile noise
	ts, ch, dir := terminalServer(t, nil)
	c, _, err := websocket.DefaultDialer.Dial(termURL(ts, ch), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	_ = c.WriteMessage(websocket.TextMessage, []byte(`{"type":"resize","cols":120,"rows":40}`))
	_ = c.WriteMessage(websocket.BinaryMessage, []byte("echo \"at:$(pwd)\"; echo \"sum:$((40+2))\"; stty size\n"))
	out := readTermUntil(t, c, "sum:42")
	real, _ := filepath.EvalSymlinks(dir)
	if !strings.Contains(out, "at:"+real) && !strings.Contains(out, "at:"+dir) {
		t.Errorf("shell did not start in the project folder %s:\n%s", dir, out)
	}
	readTermUntil(t, c, "40 120") // the resize reached the PTY

	// exit ends the session and the server closes the socket.
	_ = c.WriteMessage(websocket.BinaryMessage, []byte("exit\n"))
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		if _, _, err := c.ReadMessage(); err != nil {
			break
		}
	}
}

func TestClosingTheTabKillsWhatTheShellStarted(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	ts, ch, dir := terminalServer(t, nil)
	c, _, err := websocket.DefaultDialer.Dial(termURL(ts, ch), nil)
	if err != nil {
		t.Fatal(err)
	}
	pidFile := filepath.Join(dir, "child.pid")
	_ = c.WriteMessage(websocket.BinaryMessage, []byte("sleep 300 & echo $! > child.pid; echo started\n"))
	readTermUntil(t, c, "started")
	time.Sleep(200 * time.Millisecond)
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	c.Close() // the browser tab goes away

	var pid int
	for _, r := range strings.TrimSpace(string(raw)) {
		pid = pid*10 + int(r-'0')
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if p, _ := os.FindProcess(pid); p.Signal(nil) != nil {
			return // gone
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("background job %d survived the terminal closing", pid)
}

func TestTheTerminalNeedsALoginEvenFromLoopback(t *testing.T) {
	ts, ch, _ := terminalServer(t, auth.NewManager(auth.Config{Enabled: true}, nil))
	_, resp, err := websocket.DefaultDialer.Dial(termURL(ts, ch), nil)
	if err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("an unauthenticated loopback caller got a shell (resp=%v, err=%v)", resp, err)
	}
}

func TestTheTerminalRefusesAnotherSitesOrigin(t *testing.T) {
	ts, ch, _ := terminalServer(t, nil)
	_, resp, err := websocket.DefaultDialer.Dial(termURL(ts, ch), http.Header{"Origin": {"https://evil.example"}})
	if err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a cross-site page opened a terminal (resp=%v, err=%v)", resp, err)
	}
}

func TestTheTerminalNeedsAProject(t *testing.T) {
	ts, _, _ := terminalServer(t, nil)
	_, resp, err := websocket.DefaultDialer.Dial(termURL(ts, "ch_nope"), nil)
	if err == nil || resp == nil || resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("a terminal opened for no project (resp=%v, err=%v)", resp, err)
	}
}
