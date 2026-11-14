package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUploadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	h := UploadsHandler(dir)

	req := httptest.NewRequest(http.MethodPost, UploadsPrefix, strings.NewReader("\x89PNG fake"))
	req.Header.Set("X-Filename", "../../Ảnh chụp màn hình.png")
	req.Header.Set("Content-Type", "image/png")
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST: %d %s", rec.Code, rec.Body)
	}
	var res uploadResult
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(res.Path, dir+string(filepath.Separator)) {
		t.Fatalf("saved outside the uploads dir: %s", res.Path)
	}
	if b, _ := os.ReadFile(res.Path); string(b) != "\x89PNG fake" {
		t.Fatalf("content: %q", b)
	}
	if !strings.HasSuffix(res.Name, ".png") || strings.Contains(res.Name, "/") {
		t.Fatalf("name: %q", res.Name)
	}

	get := httptest.NewRecorder()
	h(get, httptest.NewRequest(http.MethodGet, res.URL, nil))
	if get.Code != http.StatusOK || get.Body.String() != "\x89PNG fake" {
		t.Fatalf("GET %s: %d %q", res.URL, get.Code, get.Body)
	}
	if get.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("missing nosniff")
	}
}

func TestUploadRejects(t *testing.T) {
	dir := t.TempDir()
	h := UploadsHandler(dir)

	empty := httptest.NewRecorder()
	h(empty, httptest.NewRequest(http.MethodPost, UploadsPrefix, strings.NewReader("")))
	if empty.Code != http.StatusBadRequest {
		t.Fatalf("empty body: %d", empty.Code)
	}

	big := httptest.NewRequest(http.MethodPost, UploadsPrefix, strings.NewReader(strings.Repeat("x", MaxUploadBytes+1)))
	rec := httptest.NewRecorder()
	h(rec, big)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("too large: %d", rec.Code)
	}

	for _, p := range []string{UploadsPrefix, UploadsPrefix + "2026-01-01", UploadsPrefix + "../x/y", UploadsPrefix + "a/../../b"} {
		r := httptest.NewRecorder()
		h(r, httptest.NewRequest(http.MethodGet, "http://x"+p, nil))
		if r.Code != http.StatusNotFound {
			t.Fatalf("GET %s: %d", p, r.Code)
		}
	}
}

func TestUploadName(t *testing.T) {
	for in, want := range map[string]string{
		"image.png":           "image.png",
		"../../etc/passwd":    "passwd",
		".env":                "env",
		"my photo (1).JPG":    "my_photo_1.JPG",
		"":                    "file",
		"%2E%2E%2Fsecret.txt": "secret.txt",
	} {
		if got := uploadName(in); got != want {
			t.Errorf("uploadName(%q) = %q, want %q", in, got, want)
		}
	}
}
