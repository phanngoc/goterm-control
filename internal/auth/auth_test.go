package auth

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ngocp/goterm-control/internal/storage"
)

func testManager(t *testing.T) (*Manager, *storage.UserStore) {
	t.Helper()
	db, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	users := storage.NewUserStore(db)
	hash, err := HashPassword("secret123")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if err := users.CreateUser("ngoc", hash, "admin"); err != nil {
		t.Fatalf("create user: %v", err)
	}

	m := NewManager(Config{Enabled: true, PublicHost: "bot.bomclaw.org", SessionTTL: time.Hour}, users)
	return m, users
}

func doLogin(t *testing.T, m *Manager, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/login", strings.NewReader(body))
	req.Header.Set("X-Forwarded-For", "203.0.113.7")
	w := httptest.NewRecorder()
	m.HandleLogin(w, req)
	return w
}

func TestLoginSuccessAndSession(t *testing.T) {
	m, _ := testManager(t)

	w := doLogin(t, m, `{"username":"ngoc","password":"secret123"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("login = %d, want 200 (%s)", w.Code, w.Body.String())
	}

	cookies := w.Result().Cookies()
	var sess *http.Cookie
	for _, c := range cookies {
		if c.Name == CookieName {
			sess = c
		}
	}
	if sess == nil || sess.Value == "" {
		t.Fatal("session cookie not set")
	}
	if !sess.HttpOnly {
		t.Error("cookie must be HttpOnly")
	}

	// Cookie resolves to the user.
	req := httptest.NewRequest("GET", "/api/me", nil)
	req.AddCookie(sess)
	if u := m.UserFromRequest(req); u == nil || u.Username != "ngoc" {
		t.Fatalf("UserFromRequest = %v, want ngoc", u)
	}

	// Logout invalidates it.
	lw := httptest.NewRecorder()
	lreq := httptest.NewRequest("POST", "/api/logout", nil)
	lreq.AddCookie(sess)
	m.HandleLogout(lw, lreq)
	if u := m.UserFromRequest(req); u != nil {
		t.Error("session still valid after logout")
	}
}

func TestLoginWrongPassword(t *testing.T) {
	m, _ := testManager(t)
	if w := doLogin(t, m, `{"username":"ngoc","password":"wrong"}`); w.Code != http.StatusUnauthorized {
		t.Errorf("wrong password = %d, want 401", w.Code)
	}
	if w := doLogin(t, m, `{"username":"ghost","password":"secret123"}`); w.Code != http.StatusUnauthorized {
		t.Errorf("unknown user = %d, want 401", w.Code)
	}
}

func TestLoginRateLimit(t *testing.T) {
	m, _ := testManager(t)
	var last int
	for i := 0; i < 6; i++ {
		last = doLogin(t, m, `{"username":"ngoc","password":"wrong"}`).Code
	}
	if last != http.StatusTooManyRequests {
		t.Errorf("6th attempt = %d, want 429", last)
	}
}

func TestRequireAuth(t *testing.T) {
	m, _ := testManager(t)
	handler := m.RequireAuth(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})

	// No cookie → 401.
	w := httptest.NewRecorder()
	handler(w, httptest.NewRequest("GET", "/api/status", nil))
	if w.Code != http.StatusUnauthorized {
		t.Errorf("no cookie = %d, want 401", w.Code)
	}

	// Disabled manager passes through.
	off := NewManager(Config{Enabled: false}, nil)
	w2 := httptest.NewRecorder()
	off.RequireAuth(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})(w2, httptest.NewRequest("GET", "/api/status", nil))
	if w2.Code != http.StatusTeapot {
		t.Errorf("disabled auth = %d, want passthrough 418", w2.Code)
	}
}

func TestCheckOrigin(t *testing.T) {
	m, _ := testManager(t)

	mk := func(origin, host string) *http.Request {
		r := httptest.NewRequest("GET", "/ws", nil)
		r.Host = host
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		return r
	}

	cases := []struct {
		origin, host string
		want         bool
	}{
		{"", "127.0.0.1:18789", true},                            // non-browser
		{"http://127.0.0.1:18789", "127.0.0.1:18789", true},      // local dashboard
		{"https://bot.bomclaw.org", "bot.bomclaw.org", true},     // public host
		{"https://evil.example.com", "bot.bomclaw.org", false},   // cross-site
		{"https://bot.bomclaw.org.evil.com", "x", false},         // suffix trick
	}
	for _, tc := range cases {
		if got := m.CheckOrigin(mk(tc.origin, tc.host)); got != tc.want {
			t.Errorf("CheckOrigin(origin=%q) = %v, want %v", tc.origin, got, tc.want)
		}
	}

	// Nil manager (auth disabled): same-host + localhost still allowed, everything else rejected.
	var nilMgr *Manager
	if !nilMgr.CheckOrigin(mk("http://localhost:5173", "127.0.0.1:18789")) {
		t.Error("nil manager should allow localhost origin")
	}
	if nilMgr.CheckOrigin(mk("https://evil.example.com", "127.0.0.1:18789")) {
		t.Error("nil manager should reject cross-site origin")
	}
}
