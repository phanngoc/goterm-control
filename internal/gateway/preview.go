package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ngocp/goterm-control/internal/auth"
	"github.com/ngocp/goterm-control/internal/coord"
)

// Previewing a project: press a button, get the running thing.
//
// A project says how it runs in bomclaw.json at its root — the convention the
// `preview` skill teaches agents:
//
//	{"preview": {"command": "npm run dev -- --port $PORT --host $HOST --base $BASE_PATH"}}
//	{"preview": {"static": "board"}}
//
// A command is run by the dashboard process — the one that stays up while
// agents restart — on a free port, and served back through /preview/<channel>/
// behind the dashboard login, WebSocket upgrades included so a dev server's
// hot reload works through the tunnel. A static folder needs no process: it is
// the project's own files, served by the existing /project/ route.
//
// Who can start one is who can log in to the dashboard, which already includes
// a terminal. The command itself comes from the project folder, which is the
// agents' to write: preview runs nothing an agent could not already run.

// PreviewPrefix is where previews are served.
const PreviewPrefix = "/preview/"

// PreviewManifest is the file a project declares its preview in.
const PreviewManifest = "bomclaw.json"

// PreviewSpec is bomclaw.json's "preview" object.
type PreviewSpec struct {
	// Command starts a dev server listening on $HOST:$PORT.
	Command string `json:"command,omitempty"`
	// Setup runs before Command on every start (npm install, pip install…).
	Setup string `json:"setup,omitempty"`
	// Cwd is where both run, relative to the project folder.
	Cwd string `json:"cwd,omitempty"`
	// Base says what path the server expects. "keep" (default): requests
	// arrive as $BASE_PATH/…, for servers told their base (Vite --base, Next
	// basePath). "strip": the prefix is removed first, for servers that only
	// know "/" and use relative links (python -m http.server, a plain Flask).
	Base string `json:"base,omitempty"`
	// Env adds variables for Setup and Command.
	Env map[string]string `json:"env,omitempty"`
	// Static serves a folder instead of running anything.
	Static string `json:"static,omitempty"`
}

// PreviewStatus is what the editor's preview panel shows.
type PreviewStatus struct {
	ChannelID string `json:"channel_id"`
	Declared  bool   `json:"declared"`
	Kind      string `json:"kind,omitempty"`  // "server" | "static"
	State     string `json:"state"`           // "none" | "stopped" | "starting" | "running" | "exited"
	URL       string `json:"url,omitempty"`   // where to point the iframe
	Port      int    `json:"port,omitempty"`  // the dev server's own port, for the curious
	Command   string `json:"command,omitempty"`
	StartedAt string `json:"started_at,omitempty"`
	ExitCode  *int   `json:"exit_code,omitempty"`
	Error     string `json:"error,omitempty"`
}

const (
	previewLogBytes = 128 << 10
	previewReadyFor = 5 * time.Minute // setup (npm install) can be slow the first time
)

// PreviewManager runs and proxies project dev servers. One per dashboard.
type PreviewManager struct {
	coord *coord.DB
	mu    sync.Mutex
	runs  map[string]*previewRun
}

type previewRun struct {
	spec    PreviewSpec
	port    int
	proc    *os.Process
	started time.Time
	logs    *ringLog
	done    chan struct{}

	mu    sync.Mutex
	state string
	exit  *int
	err   string
}

func (r *previewRun) setState(s string) {
	r.mu.Lock()
	r.state = s
	r.mu.Unlock()
}

func NewPreviewManager(cdb *coord.DB) *PreviewManager {
	return &PreviewManager{coord: cdb, runs: map[string]*previewRun{}}
}

// basePath is the address a channel's preview lives at.
func basePath(channelID string) string {
	return PreviewPrefix + url.PathEscape(channelID) + "/"
}

// spec reads a project's bomclaw.json. A project with no file, or no preview
// in it, has declared nothing — which is an answer, not an error.
func (m *PreviewManager) spec(channelID string) (*PreviewSpec, error) {
	path, err := m.coord.ProjectFilePath(channelID, PreviewManifest)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var mf struct {
		Preview *PreviewSpec `json:"preview"`
	}
	if err := json.Unmarshal(raw, &mf); err != nil {
		return nil, fmt.Errorf("%s: %w", PreviewManifest, err)
	}
	if mf.Preview == nil {
		return nil, nil
	}
	s := mf.Preview
	if s.Command == "" && s.Static == "" {
		return nil, fmt.Errorf("%s: preview needs a command or a static folder", PreviewManifest)
	}
	if s.Base != "" && s.Base != "keep" && s.Base != "strip" {
		return nil, fmt.Errorf("%s: preview.base is \"keep\" or \"strip\", not %q", PreviewManifest, s.Base)
	}
	return s, nil
}

// Status reports a channel's preview without changing anything.
func (m *PreviewManager) Status(channelID string) PreviewStatus {
	st := PreviewStatus{ChannelID: channelID, State: "none"}
	spec, err := m.spec(channelID)
	if err != nil {
		st.Error = err.Error()
		return st
	}
	if spec == nil {
		return st
	}
	st.Declared = true
	if spec.Command == "" {
		st.Kind, st.State = "static", "running"
		st.URL = ProjectPrefix + url.PathEscape(channelID) + "/"
		if dir := strings.Trim(filepath.ToSlash(filepath.Clean(spec.Static)), "/"); dir != "" && dir != "." {
			for _, seg := range strings.Split(dir, "/") {
				st.URL += url.PathEscape(seg) + "/"
			}
		}
		return st
	}
	st.Kind, st.State, st.Command = "server", "stopped", spec.Command
	st.URL = basePath(channelID)
	m.mu.Lock()
	run := m.runs[channelID]
	m.mu.Unlock()
	if run == nil {
		return st
	}
	run.mu.Lock()
	defer run.mu.Unlock()
	st.State, st.Port, st.ExitCode, st.Error = run.state, run.port, run.exit, run.err
	st.StartedAt = run.started.UTC().Format(time.RFC3339)
	return st
}

// Start (re)starts a channel's dev server. A static preview has nothing to
// start and answers with its status.
func (m *PreviewManager) Start(channelID string) (PreviewStatus, error) {
	spec, err := m.spec(channelID)
	if err != nil {
		return PreviewStatus{}, err
	}
	if spec == nil {
		return PreviewStatus{}, fmt.Errorf("this project declares no preview — add %s (see the preview skill)", PreviewManifest)
	}
	if spec.Command == "" {
		return m.Status(channelID), nil
	}
	m.Stop(channelID)

	dir, err := m.coord.ProjectFilePath(channelID, spec.Cwd)
	if err != nil {
		return PreviewStatus{}, err
	}
	port, err := freePort()
	if err != nil {
		return PreviewStatus{}, err
	}
	script := spec.Command
	if spec.Setup != "" {
		script = spec.Setup + " && " + spec.Command
	}
	sh := os.Getenv("SHELL")
	if sh == "" {
		sh = "/bin/sh"
	}
	// A login shell for the same reason as the terminal: under launchd this
	// process has almost no PATH, and node lives wherever the profile says.
	cmd := exec.Command(sh, "-lc", script)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"PORT="+strconv.Itoa(port),
		"HOST=127.0.0.1",
		"BASE_PATH="+basePath(channelID),
		"BROWSER=none", // create-react-app and friends open a browser otherwise
		"BOMCLAW_PREVIEW=1",
		"BOMCLAW_PROJECT="+channelID,
	)
	for k, v := range spec.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	logs := &ringLog{max: previewLogBytes}
	cmd.Stdout, cmd.Stderr = logs, logs
	setProcessGroup(cmd)
	fmt.Fprintf(logs, "$ %s\n  (cwd %s, PORT=%d, BASE_PATH=%s)\n", script, dir, port, basePath(channelID))
	if err := cmd.Start(); err != nil {
		return PreviewStatus{}, err
	}
	run := &previewRun{spec: *spec, port: port, proc: cmd.Process, started: time.Now(), logs: logs, done: make(chan struct{}), state: "starting"}
	m.mu.Lock()
	m.runs[channelID] = run
	m.mu.Unlock()
	log.Printf("preview: %s started on :%d — %s", channelID, port, script)

	go func() {
		err := cmd.Wait()
		code := -1
		if cmd.ProcessState != nil {
			code = cmd.ProcessState.ExitCode()
		}
		run.mu.Lock()
		run.state, run.exit = "exited", &code
		if err != nil && run.err == "" {
			run.err = err.Error()
		}
		run.mu.Unlock()
		fmt.Fprintf(logs, "\n[exited with code %d]\n", code)
		close(run.done)
		log.Printf("preview: %s exited (%d)", channelID, code)
	}()
	go func() {
		deadline := time.Now().Add(previewReadyFor)
		for time.Now().Before(deadline) {
			select {
			case <-run.done:
				return
			case <-time.After(300 * time.Millisecond):
			}
			c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
			if err == nil {
				c.Close()
				run.setState("running")
				return
			}
		}
		run.mu.Lock()
		if run.state == "starting" {
			run.err = fmt.Sprintf("nothing listened on $PORT (%d) within %s — does the command use $PORT and $HOST?", port, previewReadyFor)
		}
		run.mu.Unlock()
	}()
	return m.Status(channelID), nil
}

// Stop ends a channel's dev server and everything it started.
func (m *PreviewManager) Stop(channelID string) {
	m.mu.Lock()
	run := m.runs[channelID]
	delete(m.runs, channelID)
	m.mu.Unlock()
	if run == nil {
		return
	}
	select {
	case <-run.done:
		return
	default:
	}
	killGroup(run.proc, false)
	select {
	case <-run.done:
	case <-time.After(3 * time.Second):
		killGroup(run.proc, true)
		<-run.done
	}
}

// StopAll is for the dashboard's own shutdown: a dev server must not outlive
// the process that was proxying it and holding its port number.
func (m *PreviewManager) StopAll() {
	m.mu.Lock()
	ids := make([]string, 0, len(m.runs))
	for id := range m.runs {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	for _, id := range ids {
		m.Stop(id)
	}
}

// Logs returns the dev server's recent output.
func (m *PreviewManager) Logs(channelID string) string {
	m.mu.Lock()
	run := m.runs[channelID]
	m.mu.Unlock()
	if run == nil {
		return ""
	}
	return run.logs.String()
}

// Handler serves /preview/<channel>/… by proxying to that channel's dev
// server. Behind the dashboard login, like everything else a project serves.
func (m *PreviewManager) Handler(authMgr *auth.Manager) http.HandlerFunc {
	return authMgr.RequireAuthExceptLocal(func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, PreviewPrefix)
		escaped, _, _ := strings.Cut(strings.TrimPrefix(r.URL.EscapedPath(), PreviewPrefix), "/")
		channelID, err := url.PathUnescape(escaped)
		if err != nil || channelID == "" {
			http.NotFound(w, r)
			return
		}
		if !strings.Contains(rest, "/") {
			// /preview/<channel> → /preview/<channel>/, or relative URLs in
			// the page resolve one level too high.
			http.Redirect(w, r, basePath(channelID), http.StatusFound)
			return
		}
		m.mu.Lock()
		run := m.runs[channelID]
		m.mu.Unlock()
		state := ""
		if run != nil {
			run.mu.Lock()
			state = run.state
			run.mu.Unlock()
		}
		if run == nil || state != "running" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><body style="font:14px system-ui;background:#1e1e1e;color:#aaa;display:grid;place-items:center;height:90vh">Preview chưa chạy (%s) — bấm Chạy trong panel Preview.</body>`, orNone(state))
			return
		}
		target := &url.URL{Scheme: "http", Host: fmt.Sprintf("127.0.0.1:%d", run.port)}
		strip := run.spec.Base == "strip"
		prefix := basePath(channelID)
		proxy := &httputil.ReverseProxy{
			Rewrite: func(pr *httputil.ProxyRequest) {
				pr.SetURL(target) // also sets Host: dev servers that check it (Vite) accept 127.0.0.1
				pr.SetXForwarded()
				if strip {
					p := "/" + strings.TrimPrefix(pr.In.URL.Path, prefix)
					pr.Out.URL.Path, pr.Out.URL.RawPath = p, ""
				}
				dropCookie(pr.Out.Header, auth.CookieName)
			},
			FlushInterval: -1, // server-sent events and streamed responses arrive as sent
			ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
				http.Error(w, "preview: "+err.Error(), http.StatusBadGateway)
			},
		}
		proxy.ServeHTTP(w, r)
	})
}

func orNone(s string) string {
	if s == "" {
		return "stopped"
	}
	return s
}

// dropCookie removes one cookie from a Cookie header. The dashboard's session
// is no business of the app being previewed.
func dropCookie(h http.Header, name string) {
	var keep []string
	for _, line := range h.Values("Cookie") {
		for _, part := range strings.Split(line, ";") {
			p := strings.TrimSpace(part)
			if p == "" || strings.HasPrefix(p, name+"=") {
				continue
			}
			keep = append(keep, p)
		}
	}
	h.Del("Cookie")
	if len(keep) > 0 {
		h.Set("Cookie", strings.Join(keep, "; "))
	}
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// ringLog keeps the last max bytes written to it.
type ringLog struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func (l *ringLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf = append(l.buf, p...)
	if over := len(l.buf) - l.max; over > 0 {
		l.buf = append([]byte(nil), l.buf[over:]...)
	}
	return len(p), nil
}

func (l *ringLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return string(l.buf)
}

// --- RPC -------------------------------------------------------------------

type previewParams struct {
	ChannelID string `json:"channel_id"`
}

func handlePreview(deps Deps, method string, params json.RawMessage) (json.RawMessage, error) {
	if deps.Preview == nil {
		return nil, fmt.Errorf("previews are run by the dashboard process (bomclaw dashboard), not by an agent's gateway")
	}
	var p previewParams
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}
	if p.ChannelID == "" {
		return nil, fmt.Errorf("channel_id is required")
	}
	switch method {
	case "preview.status":
		return json.Marshal(deps.Preview.Status(p.ChannelID))
	case "preview.start":
		st, err := deps.Preview.Start(p.ChannelID)
		if err != nil {
			return nil, err
		}
		return json.Marshal(st)
	case "preview.stop":
		deps.Preview.Stop(p.ChannelID)
		return json.Marshal(deps.Preview.Status(p.ChannelID))
	case "preview.logs":
		return json.Marshal(map[string]string{"logs": deps.Preview.Logs(p.ChannelID)})
	}
	return nil, fmt.Errorf("unknown method: %s", method)
}
