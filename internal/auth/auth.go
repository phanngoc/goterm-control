// Package auth implements username/password authentication for the gateway
// dashboard: bcrypt credentials in SQLite, opaque session tokens in HttpOnly
// cookies, per-IP login rate limiting, and a WebSocket Origin check.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ngocp/goterm-control/internal/storage"
	"golang.org/x/crypto/bcrypt"
)

// CookieName is the session cookie set on successful login.
const CookieName = "bomclaw_session"

const bcryptCost = 12

// Config controls dashboard authentication.
type Config struct {
	Enabled    bool
	PublicHost string        // public hostname (for WS Origin checks), e.g. "bot.bomclaw.org"
	SessionTTL time.Duration // login session lifetime
}

// Manager owns credential verification and web sessions.
type Manager struct {
	cfg   Config
	users *storage.UserStore
	rl    *rateLimiter
}

func NewManager(cfg Config, users *storage.UserStore) *Manager {
	if cfg.SessionTTL <= 0 {
		cfg.SessionTTL = 7 * 24 * time.Hour
	}
	return &Manager{
		cfg:   cfg,
		users: users,
		rl:    newRateLimiter(5, time.Minute), // 5 login attempts per IP per minute
	}
}

// Enabled reports whether auth is enforced. Safe on nil.
func (m *Manager) Enabled() bool { return m != nil && m.cfg.Enabled }

// HashPassword bcrypt-hashes a plaintext password (used by the user CLI).
func HashPassword(password string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	return string(b), err
}

// --- HTTP handlers ---

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// HandleLogin verifies credentials and sets the session cookie.
func (m *Manager) HandleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	ip := clientIP(r)
	if !m.rl.allow(ip) {
		http.Error(w, `{"error":"too many attempts, try again later"}`, http.StatusTooManyRequests)
		return
	}

	var req loginRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		http.Error(w, `{"error":"bad request"}`, http.StatusBadRequest)
		return
	}

	user, err := m.users.GetUser(strings.TrimSpace(req.Username))
	if err != nil {
		log.Printf("auth: get user: %v", err)
		http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
		return
	}
	// Constant-shape failure: run bcrypt even when the user doesn't exist so
	// usernames can't be enumerated by response timing.
	hash := "$2a$12$invalidinvalidinvalidinvalidinvalidinvalidinvalidinva"
	if user != nil {
		hash = user.PasswordHash
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.Password)) != nil || user == nil {
		log.Printf("auth: failed login for %q from %s", req.Username, ip)
		http.Error(w, `{"error":"invalid credentials"}`, http.StatusUnauthorized)
		return
	}

	token, tokenHash, err := newToken()
	if err != nil {
		http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
		return
	}
	if err := m.users.CreateWebSession(tokenHash, user.ID, time.Now().Add(m.cfg.SessionTTL)); err != nil {
		log.Printf("auth: create session: %v", err)
		http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   isHTTPS(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(m.cfg.SessionTTL.Seconds()),
	})
	log.Printf("auth: %s logged in from %s", user.Username, ip)
	writeJSON(w, map[string]string{"username": user.Username, "role": user.Role})
}

// HandleLogout deletes the current session and clears the cookie.
func (m *Manager) HandleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(CookieName); err == nil {
		_ = m.users.DeleteWebSession(hashToken(c.Value))
	}
	http.SetCookie(w, &http.Cookie{
		Name: CookieName, Value: "", Path: "/", HttpOnly: true, MaxAge: -1,
		Secure: isHTTPS(r), SameSite: http.SameSiteLaxMode,
	})
	writeJSON(w, map[string]bool{"ok": true})
}

// HandleMe reports the logged-in user, or 401. When auth is disabled it
// returns 200 with enabled=false so the SPA skips the login screen.
func (m *Manager) HandleMe(w http.ResponseWriter, r *http.Request) {
	if !m.Enabled() {
		writeJSON(w, map[string]any{"enabled": false, "username": "", "role": "admin"})
		return
	}
	user := m.UserFromRequest(r)
	if user == nil {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}
	writeJSON(w, map[string]any{"enabled": true, "username": user.Username, "role": user.Role})
}

// UserFromRequest resolves the session cookie to a user, or nil.
func (m *Manager) UserFromRequest(r *http.Request) *storage.User {
	c, err := r.Cookie(CookieName)
	if err != nil || c.Value == "" {
		return nil
	}
	user, err := m.users.GetWebSession(hashToken(c.Value))
	if err != nil {
		log.Printf("auth: session lookup: %v", err)
		return nil
	}
	return user
}

// RequireAuth wraps a handler with a session check. When auth is disabled it
// passes through unchanged (local-only setups keep working with no users).
func (m *Manager) RequireAuth(next http.HandlerFunc) http.HandlerFunc {
	if !m.Enabled() {
		return next
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if m.UserFromRequest(r) == nil {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

// IsLocalDirect reports whether the request came straight from loopback
// without any proxy/tunnel forwarding headers. Tunnel traffic always carries
// CF-Connecting-IP / X-Forwarded-* (added by cloudflared), so it can never
// masquerade as local-direct.
func IsLocalDirect(r *http.Request) bool {
	if r.Header.Get("CF-Connecting-IP") != "" ||
		r.Header.Get("X-Forwarded-For") != "" ||
		r.Header.Get("X-Forwarded-Proto") != "" {
		return false
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// RequireAuthExceptLocal is RequireAuth with a carve-out for direct loopback
// clients (the menu bar tray, local curl). Use only for read-only endpoints.
func (m *Manager) RequireAuthExceptLocal(next http.HandlerFunc) http.HandlerFunc {
	if !m.Enabled() {
		return next
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if m.UserFromRequest(r) == nil && !IsLocalDirect(r) {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

// CheckOrigin validates a WebSocket Origin header: same-host requests,
// localhost, and the configured public host are allowed.
func (m *Manager) CheckOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true // non-browser client
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	host := u.Hostname()
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return true
	}
	if host == r.Host || host == hostOnly(r.Host) {
		return true
	}
	if m != nil && m.cfg.PublicHost != "" && strings.EqualFold(host, m.cfg.PublicHost) {
		return true
	}
	return false
}

// --- helpers ---

func newToken() (token, tokenHash string, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	token = base64.RawURLEncoding.EncodeToString(raw)
	return token, hashToken(token), nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// clientIP prefers the Cloudflare/proxy headers set by the tunnel.
func clientIP(r *http.Request) string {
	if ip := r.Header.Get("CF-Connecting-IP"); ip != "" {
		return ip
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if first, _, ok := strings.Cut(xff, ","); ok {
			return strings.TrimSpace(first)
		}
		return strings.TrimSpace(xff)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func hostOnly(hostport string) string {
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return h
	}
	return hostport
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
