package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWikiRPCAndHosting(t *testing.T) {
	db := testCoordDB(t)
	p, err := db.CreateProject("docs", "", "owner", t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	deps := Deps{Coord: db}
	rpc := func(params map[string]any) map[string]any {
		t.Helper()
		params["channel_id"] = p.ID
		raw, _ := json.Marshal(params)
		out, err := handleChannelWiki(deps, raw)
		if err != nil {
			t.Fatalf("%v: %v", params["op"], err)
		}
		var m map[string]any
		_ = json.Unmarshal(out, &m)
		return m
	}

	rpc(map[string]any{"op": "save", "path": "gdd.md", "body": "# Thiết kế\n\n| a | b |\n|---|---|\n| 1 | 2 |\n\n<script>alert(1)</script>"})
	rpc(map[string]any{"op": "save", "path": "page.html", "body": "<!doctype html><title>Trang</title><p>hi</p>"})
	list := rpc(map[string]any{"op": "list"})
	if pages, _ := list["pages"].([]any); len(pages) != 2 {
		t.Fatalf("list: %v", list)
	}
	if got := rpc(map[string]any{"op": "read", "path": "gdd.md"}); !strings.HasPrefix(got["body"].(string), "# Thiết kế") {
		t.Fatalf("read: %v", got)
	}

	host := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		WikiHandler(deps)(w, httptest.NewRequest(http.MethodGet, path, nil))
		return w
	}
	md := host(WikiPrefix + p.ID + "/gdd.md")
	body := md.Body.String()
	if md.Code != 200 || !strings.Contains(body, "<table>") || !strings.Contains(body, "<h1>Thiết kế</h1>") {
		t.Fatalf("md not rendered: %d %s", md.Code, body)
	}
	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Fatal("raw HTML in markdown passed through")
	}
	if !strings.Contains(md.Header().Get("Content-Security-Policy"), "sandbox") {
		t.Fatal("hosted page is not sandboxed")
	}
	if raw := host(WikiPrefix + p.ID + "/gdd.md?raw=1"); !strings.HasPrefix(raw.Body.String(), "# Thiết kế") {
		t.Fatalf("raw: %s", raw.Body)
	}
	if h := host(WikiPrefix + p.ID + "/page.html"); !strings.Contains(h.Body.String(), "<p>hi</p>") {
		t.Fatalf("html: %s", h.Body)
	}
	if idx := host(WikiPrefix + p.ID + "/"); !strings.Contains(idx.Body.String(), "Trang") || !strings.Contains(idx.Body.String(), "gdd.md") {
		t.Fatalf("index: %s", idx.Body)
	}
	if bad := host(WikiPrefix + p.ID + "/../AGENTS.md"); bad.Code == 200 {
		t.Fatalf("escaped the wiki: %d", bad.Code)
	}
}
