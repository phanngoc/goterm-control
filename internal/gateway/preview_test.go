//go:build !windows

package gateway

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/ngocp/goterm-control/internal/coord"
)

func previewProject(t *testing.T, files map[string]string) (*PreviewManager, *coord.Channel) {
	t.Helper()
	db := testCoordDB(t)
	p, err := db.CreateProject("radar", "", "owner", t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		full := filepath.Join(p.Workspace, filepath.FromSlash(name))
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	m := NewPreviewManager(db)
	t.Cleanup(m.StopAll)
	return m, p
}

func waitRunning(t *testing.T, m *PreviewManager, ch string) PreviewStatus {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		st := m.Status(ch)
		if st.State == "running" {
			return st
		}
		if st.State == "exited" {
			t.Fatalf("preview exited: %+v\n%s", st, m.Logs(ch))
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("preview never came up:\n%s", m.Logs(ch))
	return PreviewStatus{}
}

func previewGet(t *testing.T, h http.Handler, path string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	return rec.Code, rec.Body.String()
}

func needPython(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not installed")
	}
}

func TestAProjectWithoutAManifestDeclaresNothing(t *testing.T) {
	m, p := previewProject(t, nil)
	if st := m.Status(p.ID); st.Declared || st.State != "none" || st.Error != "" {
		t.Fatalf("status = %+v", st)
	}
	if _, err := m.Start(p.ID); err == nil || !strings.Contains(err.Error(), PreviewManifest) {
		t.Fatalf("start without a manifest = %v, want a pointer to %s", err, PreviewManifest)
	}
}

func TestABadManifestSaysWhatIsWrong(t *testing.T) {
	for body, want := range map[string]string{
		`{"preview": {}}`:                               "command or a static",
		`{"preview": {"command":"x","base":"sideways"}}`: "keep",
		`{nope`: PreviewManifest,
	} {
		m, p := previewProject(t, map[string]string{PreviewManifest: body})
		if st := m.Status(p.ID); !strings.Contains(st.Error, want) {
			t.Errorf("%s → %q, want it to mention %q", body, st.Error, want)
		}
	}
}

func TestAStaticPreviewPointsAtTheProjectsOwnFiles(t *testing.T) {
	m, p := previewProject(t, map[string]string{PreviewManifest: `{"preview":{"static":"board"}}`, "board/index.html": "hi"})
	st := m.Status(p.ID)
	if st.Kind != "static" || st.State != "running" || st.URL != ProjectPrefix+url.PathEscape(p.ID)+"/board/" {
		t.Fatalf("static status = %+v", st)
	}
}

func TestAServerPreviewStripsItsPrefix(t *testing.T) {
	needPython(t)
	m, p := previewProject(t, map[string]string{
		PreviewManifest: `{"preview":{"command":"exec python3 -m http.server $PORT --bind $HOST","base":"strip"}}`,
		"index.html":    "<h1>radar board</h1>",
	})
	if _, err := m.Start(p.ID); err != nil {
		t.Fatal(err)
	}
	st := waitRunning(t, m, p.ID)
	h := m.Handler(nil)

	if code, body := previewGet(t, h, st.URL); code != 200 || !strings.Contains(body, "radar board") {
		t.Fatalf("GET %s = %d %q", st.URL, code, body)
	}
	if code, _ := previewGet(t, h, strings.TrimSuffix(st.URL, "/")); code != http.StatusFound {
		t.Errorf("the address without a slash should redirect, got %d", code)
	}
}

func TestAServerPreviewKeepsItsPrefixByDefault(t *testing.T) {
	needPython(t)
	m, p := previewProject(t, map[string]string{
		PreviewManifest: `{"preview":{"command":"exec python3 -m http.server $PORT --bind $HOST"}}`,
	})
	// A server told its base serves under it; python serves the folder tree,
	// so put the page where the kept prefix points.
	base := filepath.Join(p.Workspace, "preview", p.ID)
	_ = os.MkdirAll(base, 0o755)
	_ = os.WriteFile(filepath.Join(base, "index.html"), []byte("kept"), 0o644)
	if _, err := m.Start(p.ID); err != nil {
		t.Fatal(err)
	}
	st := waitRunning(t, m, p.ID)
	if code, body := previewGet(t, m.Handler(nil), st.URL); code != 200 || body != "kept" {
		t.Fatalf("GET %s = %d %q", st.URL, code, body)
	}
}

func TestTheServerGetsItsPortHostAndBase(t *testing.T) {
	m, p := previewProject(t, map[string]string{
		PreviewManifest: `{"preview":{"setup":"echo setup-ran","command":"echo \"port=$PORT host=$HOST base=$BASE_PATH extra=$EXTRA\"; sleep 30","env":{"EXTRA":"yes"}}}`,
	})
	if _, err := m.Start(p.ID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(m.Logs(p.ID), "extra=yes") && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	logs := m.Logs(p.ID)
	for _, want := range []string{"setup-ran", "host=127.0.0.1", "base=" + basePath(p.ID), "extra=yes", "port="} {
		if !strings.Contains(logs, want) {
			t.Errorf("logs lack %q:\n%s", want, logs)
		}
	}
}

func TestStoppingAPreviewEndsWhatItStarted(t *testing.T) {
	needPython(t)
	// The shell starts python as a child rather than exec'ing it — the shape
	// of npm starting node — so only a group kill reaches it.
	m, p := previewProject(t, map[string]string{
		PreviewManifest: `{"preview":{"command":"python3 -m http.server $PORT --bind $HOST; echo after"}}`,
	})
	if _, err := m.Start(p.ID); err != nil {
		t.Fatal(err)
	}
	st := waitRunning(t, m, p.ID)
	m.Stop(p.ID)
	if c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", st.Port), time.Second); err == nil {
		c.Close()
		t.Fatalf("port %d still answers after stop", st.Port)
	}
	if s := m.Status(p.ID); s.State != "stopped" {
		t.Fatalf("status after stop = %+v", s)
	}
}

func TestANotRunningPreviewSaysSo(t *testing.T) {
	m, p := previewProject(t, map[string]string{PreviewManifest: `{"preview":{"command":"true"}}`})
	code, body := previewGet(t, m.Handler(nil), basePath(p.ID))
	if code != http.StatusServiceUnavailable || !strings.Contains(body, "chưa chạy") {
		t.Fatalf("GET before start = %d %q", code, body)
	}
}

// Hot reload is a WebSocket back to the dev server; it has to survive the proxy.
func TestAWebSocketPassesThroughThePreview(t *testing.T) {
	up := websocket.Upgrader{}
	dev := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "" && strings.Contains(r.Header.Get("Cookie"), "bomclaw_session") {
			http.Error(w, "saw the dashboard session", 500)
			return
		}
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		_, msg, _ := c.ReadMessage()
		_ = c.WriteMessage(websocket.TextMessage, append([]byte("hmr:"+r.URL.Path+":"), msg...))
	}))
	defer dev.Close()
	port := dev.Listener.Addr().(*net.TCPAddr).Port

	m, p := previewProject(t, map[string]string{PreviewManifest: `{"preview":{"command":"true"}}`})
	run := &previewRun{port: port, started: time.Now(), logs: &ringLog{max: 1024}, done: make(chan struct{}), state: "running"}
	m.runs[p.ID] = run
	defer delete(m.runs, p.ID)

	front := httptest.NewServer(m.Handler(nil))
	defer front.Close()
	wsURL := "ws" + strings.TrimPrefix(front.URL, "http") + basePath(p.ID) + "@vite/client"
	c, resp, err := websocket.DefaultDialer.Dial(wsURL, http.Header{"Cookie": {"bomclaw_session=secret; theme=dark"}})
	if err != nil {
		body := ""
		if resp != nil {
			b, _ := io.ReadAll(resp.Body)
			body = string(b)
		}
		t.Fatalf("dial through preview: %v %s", err, body)
	}
	defer c.Close()
	_ = c.WriteMessage(websocket.TextMessage, []byte("ping"))
	_, msg, err := c.ReadMessage()
	if err != nil || string(msg) != "hmr:"+basePath(p.ID)+"@vite/client:ping" {
		t.Fatalf("echo = %q, %v", msg, err)
	}
}

func TestDropCookieKeepsTheAppsOwnCookies(t *testing.T) {
	h := http.Header{"Cookie": {"a=1; bomclaw_session=secret", "b=2"}}
	dropCookie(h, "bomclaw_session")
	if got := h.Get("Cookie"); got != "a=1; b=2" {
		t.Fatalf("Cookie = %q", got)
	}
}
