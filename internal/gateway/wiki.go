package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"os"
	"strings"

	"github.com/ngocp/goterm-control/internal/coord"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

// A room's wiki: listed, read and saved over channels.wiki, hosted over
// /wiki/<channel>/<page>. See coord/wiki.go for where the pages live.

// WikiPrefix is the route hosted pages are served under.
const WikiPrefix = "/wiki/"

type wikiParams struct {
	ChannelID string  `json:"channel_id"`
	Op        string  `json:"op"` // list | read | save | delete | rename
	Path      string  `json:"path,omitempty"`
	To        string  `json:"to,omitempty"`
	Body      *string `json:"body,omitempty"`
	BaseMtime string  `json:"base_mtime,omitempty"`
}

func handleChannelWiki(deps Deps, params json.RawMessage) (json.RawMessage, error) {
	if deps.Coord == nil {
		return nil, errNoCoord()
	}
	var p wikiParams
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}
	if p.ChannelID == "" {
		return nil, fmt.Errorf("channel_id is required")
	}
	switch p.Op {
	case "", "list":
		pages, err := deps.Coord.ListWiki(p.ChannelID)
		if err != nil {
			return nil, err
		}
		dir, _ := deps.Coord.WikiDir(p.ChannelID)
		return json.Marshal(map[string]any{"pages": pages, "dir": dir, "base_url": WikiPrefix + p.ChannelID + "/"})
	case "read":
		body, mtime, err := deps.Coord.ReadWiki(p.ChannelID, p.Path)
		if err != nil {
			return nil, err
		}
		return json.Marshal(map[string]any{"path": p.Path, "body": body, "mtime": mtime, "format": coord.WikiFormat(p.Path)})
	case "save":
		if p.Body == nil {
			return nil, fmt.Errorf("body is required to save")
		}
		mtime, err := deps.Coord.WriteWiki(p.ChannelID, p.Path, *p.Body, p.BaseMtime)
		if err != nil {
			return nil, err
		}
		return json.Marshal(map[string]any{"path": p.Path, "mtime": mtime, "saved": true})
	case "delete":
		if err := deps.Coord.DeleteWiki(p.ChannelID, p.Path); err != nil {
			return nil, err
		}
		return json.Marshal(map[string]any{"path": p.Path, "done": true})
	case "rename":
		if err := deps.Coord.RenameWiki(p.ChannelID, p.Path, p.To); err != nil {
			return nil, err
		}
		return json.Marshal(map[string]any{"path": p.To, "done": true})
	}
	return nil, fmt.Errorf("unknown op %q", p.Op)
}

var wikiMarkdown = goldmark.New(goldmark.WithExtensions(extension.GFM))

// WikiHandler hosts a room's pages: /wiki/<channel>/ is an index, a .md page
// is rendered to HTML (?raw=1 for the source), anything else is served as is.
//
// Every response is sandboxed (CSP sandbox, no same-origin): a page an agent
// wrote can run its own scripts, but cannot act as the person signed in to
// the dashboard on this origin.
func WikiHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps.Coord == nil {
			http.Error(w, "no coordination database", http.StatusServiceUnavailable)
			return
		}
		rest := strings.TrimPrefix(r.URL.Path, WikiPrefix)
		channelID, rel, _ := strings.Cut(rest, "/")
		if channelID == "" {
			http.Error(w, "usage: "+WikiPrefix+"<channel-id>/<page>", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Security-Policy",
			"sandbox allow-scripts allow-popups allow-forms allow-modals allow-downloads; frame-ancestors 'self'")
		w.Header().Set("X-Content-Type-Options", "nosniff")

		full, err := deps.Coord.WikiPath(channelID, rel)
		if err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
		info, err := os.Stat(full)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if info.IsDir() {
			serveWikiIndex(w, r, deps, channelID, rel)
			return
		}
		if coord.WikiFormat(rel) == "md" && r.URL.Query().Get("raw") == "" {
			src, err := os.ReadFile(full)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			var out bytes.Buffer
			if err := wikiMarkdown.Convert(src, &out); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			title := strings.TrimSuffix(rel[strings.LastIndex(rel, "/")+1:], ".md")
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprintf(w, wikiPageHead, html.EscapeString(title))
			w.Write(out.Bytes())
			fmt.Fprint(w, `</article></body></html>`)
			return
		}
		if coord.WikiFormat(rel) == "md" {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		}
		http.ServeFile(w, r, full)
	}
}

func serveWikiIndex(w http.ResponseWriter, r *http.Request, deps Deps, channelID, rel string) {
	pages, err := deps.Coord.ListWiki(channelID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	prefix := strings.Trim(rel, "/")
	if prefix != "" {
		prefix += "/"
	}
	name := channelID
	if c, err := deps.Coord.GetChannel(channelID); err == nil && c.Name != "" {
		name = c.Name
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, wikiPageHead, "Wiki · "+html.EscapeString(name))
	fmt.Fprintf(w, "<h1>Wiki · %s</h1><ul>", html.EscapeString(name))
	n := 0
	for _, p := range pages {
		if !strings.HasPrefix(p.Path, prefix) || p.Format == "other" {
			continue
		}
		n++
		fmt.Fprintf(w, `<li><a href="%s%s/%s">%s</a> <small>%s · %s</small></li>`,
			WikiPrefix, channelID, escapePath(p.Path), html.EscapeString(p.Title),
			html.EscapeString(p.Path), p.Mtime[:10])
	}
	if n == 0 {
		fmt.Fprint(w, "<li><em>Chưa có trang nào.</em></li>")
	}
	fmt.Fprint(w, `</ul></article></body></html>`)
}

func escapePath(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = strings.ReplaceAll(html.EscapeString(s), " ", "%20")
	}
	return strings.Join(segs, "/")
}

const wikiPageHead = `<!doctype html><html lang="vi"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1"><title>%s</title>
<style>
:root{color-scheme:dark}
body{margin:0;background:#0b0f17;color:#e5e7eb;font:16px/1.65 -apple-system,"Segoe UI",Roboto,sans-serif}
article{max-width:860px;margin:0 auto;padding:32px 20px 80px}
h1,h2,h3{line-height:1.3;color:#f3f4f6}h1{font-size:1.8em}h2{border-bottom:1px solid #1f2937;padding-bottom:.3em}
a{color:#7dd3fc}code{background:#1f2937;padding:.1em .35em;border-radius:4px;font-size:.9em;color:#c4b5fd}
pre{background:#111827;padding:12px 14px;border-radius:8px;overflow:auto}pre code{background:none;padding:0;color:#e5e7eb}
table{border-collapse:collapse;display:block;overflow:auto}th,td{border:1px solid #374151;padding:6px 10px}th{background:#111827}
blockquote{margin:0;padding:0 1em;border-left:3px solid #4b5563;color:#9ca3af}img{max-width:100%%}
small{color:#6b7280}li{margin:.3em 0}hr{border:0;border-top:1px solid #1f2937}
</style></head><body><article>`
