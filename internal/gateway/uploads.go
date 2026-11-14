package gateway

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// Files dropped or pasted into a dashboard message box.
//
// Telegram already delivers a photo to an agent as a file on disk plus a line
// telling it to Read that path; the dashboard does the same. The browser
// POSTs the file here, gets back an absolute path under ~/goterm-shared/uploads
// — the one folder every agent's sandbox may read, whichever gateway took the
// upload — and the message carries that path. GET serves the same file back so
// the room can show the picture instead of only its name.

// UploadsPrefix is the route this is mounted at.
const UploadsPrefix = "/api/uploads/"

// MaxUploadBytes caps one file. A screenshot is a few MB; this leaves room for
// a phone photo without letting a stray drop of a video fill the disk.
const MaxUploadBytes = 25 << 20

// UploadsDir is where dropped files land.
func UploadsDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "goterm-shared", "uploads")
}

type uploadResult struct {
	Path string `json:"path"` // absolute, for the agent
	URL  string `json:"url"`  // for the browser
	Name string `json:"name"`
	Size int64  `json:"size"`
	Type string `json:"type"`
}

// UploadsHandler takes POST /api/uploads/ (raw body, file name in the
// X-Filename header) and serves GET /api/uploads/<day>/<file>.
func UploadsHandler(dir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			saveUpload(w, r, dir)
		case http.MethodGet, http.MethodHead:
			serveUpload(w, r, dir)
		default:
			http.Error(w, "GET or POST", http.StatusMethodNotAllowed)
		}
	}
}

func saveUpload(w http.ResponseWriter, r *http.Request, dir string) {
	name := uploadName(r.Header.Get("X-Filename"))
	if r.ContentLength > MaxUploadBytes {
		http.Error(w, "file too large (max 25 MB)", http.StatusRequestEntityTooLarge)
		return
	}
	day := time.Now().Format("2006-01-02")
	dayDir := filepath.Join(dir, day)
	if err := os.MkdirAll(dayDir, 0o755); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var rnd [4]byte
	_, _ = rand.Read(rnd[:])
	file := hex.EncodeToString(rnd[:]) + "_" + name
	full := filepath.Join(dayDir, file)

	f, err := os.OpenFile(full, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	n, err := io.Copy(f, io.LimitReader(r.Body, MaxUploadBytes+1))
	cerr := f.Close()
	if err == nil {
		err = cerr
	}
	if err == nil && n > MaxUploadBytes {
		_ = os.Remove(full)
		http.Error(w, "file too large (max 25 MB)", http.StatusRequestEntityTooLarge)
		return
	}
	if err != nil || n == 0 {
		_ = os.Remove(full)
		if err == nil {
			http.Error(w, "empty file", http.StatusBadRequest)
		} else {
			http.Error(w, err.Error(), http.StatusBadRequest)
		}
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(uploadResult{
		Path: full,
		URL:  UploadsPrefix + day + "/" + url.PathEscape(file),
		Name: name,
		Size: n,
		Type: r.Header.Get("Content-Type"),
	})
}

func serveUpload(w http.ResponseWriter, r *http.Request, dir string) {
	rel := strings.TrimPrefix(r.URL.Path, UploadsPrefix)
	// Exactly <day>/<file>: no listing, no climbing out.
	parts := strings.Split(rel, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" ||
		strings.HasPrefix(parts[0], ".") || strings.HasPrefix(parts[1], ".") ||
		path.Clean(rel) != rel {
		http.NotFound(w, r)
		return
	}
	full := filepath.Join(dir, parts[0], parts[1])
	st, err := os.Stat(full)
	if err != nil || st.IsDir() {
		http.NotFound(w, r)
		return
	}
	// A dropped .html or .svg must not run as a page on the dashboard's origin.
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; img-src 'self'; style-src 'unsafe-inline'; sandbox")
	w.Header().Set("Cache-Control", "private, max-age=86400")
	http.ServeFile(w, r, full)
}

// uploadName keeps a sender's file name readable but harmless: no directory
// parts, no leading dot, only safe characters, and not too long.
func uploadName(s string) string {
	if dec, err := url.QueryUnescape(s); err == nil {
		s = dec
	}
	s = filepath.Base(strings.ReplaceAll(s, "\\", "/"))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '-', r == '_':
			b.WriteRune(r)
		case r == ' ':
			b.WriteRune('_')
		}
	}
	out := strings.TrimLeft(b.String(), "._")
	if len(out) > 80 {
		ext := filepath.Ext(out)
		if len(ext) > 10 {
			ext = ""
		}
		out = out[:80-len(ext)] + ext
	}
	if out == "" || out == "/" {
		out = "file"
	}
	return out
}
